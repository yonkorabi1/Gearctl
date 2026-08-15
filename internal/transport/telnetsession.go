package transport

import (
	"bytes"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"time"

	"github.com/onu-platform/gearctl/internal/olterr"
)

const (
	iacByte  = 0xff
	willByte = 0xfb
	wontByte = 0xfc
	doByte   = 0xfd
	dontByte = 0xfe
	sbByte   = 0xfa
	seByte   = 0xf0

	telOptTTYPE = 24
	ttypeIS     = 0
	ttypeSEND   = 1

	telOptNAWS = 31
)

// terminalType is the generic terminal type advertised in response to a
// TTYPE SEND request -- see telnetIacParser's doc comment.
const terminalType = "VT100"

// Fixed window dimensions advertised for NAWS -- this is a headless,
// non-interactive client with no real terminal, but some embedded telnetd
// implementations block waiting for real dimensions rather than accepting
// a refusal gracefully (see telnetIacParser's doc comment, point 3). Any
// plausible fixed size unblocks them; the exact numbers don't matter since
// nothing here actually does column-aware rendering.
const (
	nawsWidth  = 80
	nawsHeight = 24
)

// telnetIacParser is a stateful IAC (Telnet protocol command) parser. It
// strips option-negotiation traffic from the incoming byte stream and
// returns the remaining plain text, replying to negotiation requests on
// the same connection as needed.
//
// Two things a naive per-chunk parser gets wrong, both confirmed live
// against a real OLT during Phase 1 testing:
//
//  1. An IAC sequence -- including a full IAC SB <option> ... IAC SE
//     subnegotiation block, which can be many bytes -- can be split across
//     multiple TCP reads. A parser that only looks within one chunk either
//     mishandles the split (treating a lone option/data byte left over
//     from a previous chunk as literal text) or has to assume it always
//     arrives whole, which real devices don't guarantee. This type carries
//     any incomplete sequence forward via `pending` and resumes on the
//     next chunk.
//  2. Blanket-refusing every negotiation (WONT/DONT to everything) is the
//     simple, safe default for most options, but some embedded device
//     Telnet servers won't proceed to the login prompt at all until they
//     get a real answer to a TTYPE (terminal type) query -- confirmed
//     live: a real OLT sent `IAC DO TTYPE` then `IAC SB TTYPE SEND IAC SE`
//     and then went silent, never showing a login prompt, because
//     refusing TTYPE outright left it stuck waiting. This parser instead
//     agrees to TTYPE (`IAC WILL TTYPE`) and answers a SEND request with a
//     generic terminal type (`IAC SB TTYPE IS "VT100" IAC SE`).
//  3. Same problem, different option: confirmed live, via packet capture,
//     against the same real OLT -- it also sends `IAC DO NAWS` (window
//     size) up front, and refusing it (the previous behavior) is enough
//     to make the device go silent again *after* login, never printing a
//     password prompt, even though TTYPE negotiation completed correctly.
//     A real telnet client always answers NAWS with actual window
//     dimensions, which this minimal client has none of -- so it agrees
//     (`IAC WILL NAWS`) and reports a fixed, plausible size instead of
//     refusing outright.
//
// Everything else is still refused via WONT/DONT, matching a minimal,
// safe terminal.
type telnetIacParser struct {
	pending []byte
	conn    net.Conn
}

func newTelnetIacParser(conn net.Conn) *telnetIacParser {
	return &telnetIacParser{conn: conn}
}

// feed processes newly-received bytes and returns the plain-text portion
// ready for the caller.
func (p *telnetIacParser) feed(chunk []byte) []byte {
	data := append(append([]byte{}, p.pending...), chunk...)
	p.pending = nil
	out := make([]byte, 0, len(data))
	i := 0

	for i < len(data) {
		if data[i] != iacByte {
			out = append(out, data[i])
			i++
			continue
		}

		// From here, data[i] === IAC. Need at least one more byte to know
		// what kind of command this is -- if not available yet, carry the
		// whole thing forward and wait for more data.
		if i+1 >= len(data) {
			p.pending = append([]byte{}, data[i:]...)
			break
		}

		cmd := data[i+1]

		if cmd == iacByte {
			// Escaped literal 0xFF byte.
			out = append(out, iacByte)
			i += 2
			continue
		}

		if cmd >= willByte && cmd <= dontByte {
			// WILL/WONT/DO/DONT + a 1-byte option code.
			if i+2 >= len(data) {
				p.pending = append([]byte{}, data[i:]...)
				break
			}
			option := data[i+2]
			p.replyToNegotiation(cmd, option)
			i += 3
			continue
		}

		if cmd == sbByte {
			// Subnegotiation block: IAC SB <option> <data...> IAC SE. Scan
			// forward for the terminating IAC SE -- if it's not in this
			// chunk yet, carry the whole partial block forward rather
			// than guessing.
			seIndex := findSequence(data, i+2, []byte{iacByte, seByte})
			if seIndex == -1 {
				p.pending = append([]byte{}, data[i:]...)
				break
			}
			if i+2 < len(data) {
				option := data[i+2]
				payload := data[i+3 : seIndex]
				p.replyToSubnegotiation(option, payload)
			}
			i = seIndex + 2
			continue
		}

		// Other 2-byte IAC commands (NOP, AYT, etc.) -- skip.
		i += 2
	}

	return out
}

func (p *telnetIacParser) replyToNegotiation(cmd, option byte) {
	if option == telOptTTYPE && (cmd == doByte || cmd == willByte) {
		// Agree to supply our terminal type rather than refusing outright
		// -- see the type's doc comment for why this matters for real
		// hardware.
		_, _ = p.conn.Write([]byte{iacByte, willByte, telOptTTYPE})
		return
	}
	if option == telOptNAWS && cmd == doByte {
		// Agree to report window size, then immediately report it --
		// unlike TTYPE, NAWS has no SEND subcommand; the client just sends
		// its dimensions proactively once it agrees. See the type's doc
		// comment (point 3) for why refusing this outright isn't safe.
		_, _ = p.conn.Write([]byte{iacByte, willByte, telOptNAWS})
		_, _ = p.conn.Write([]byte{
			iacByte, sbByte, telOptNAWS,
			0, nawsWidth,
			0, nawsHeight,
			iacByte, seByte,
		})
		return
	}
	// Refuse everything else, using the RFC 854-correct pairing -- caught
	// live against a real OLT (packet capture during Phase 1 testing):
	// this used to reply WONT to a peer's WILL offer, which is backwards
	// (WONT is a statement about OUR OWN behavior, the correct refusal of
	// "I will do X" is "DONT", i.e. "don't do X"). The device tolerated
	// the malformed reply here, but there's no reason to rely on that
	// tolerance holding for every vendor. WONT/DONT from the peer are
	// already negative acknowledgements -- replying to those risks an
	// infinite negotiation loop with some peers, per RFC 854, so we don't.
	switch cmd {
	case willByte:
		_, _ = p.conn.Write([]byte{iacByte, dontByte, option})
	case doByte:
		_, _ = p.conn.Write([]byte{iacByte, wontByte, option})
	}
}

func (p *telnetIacParser) replyToSubnegotiation(option byte, payload []byte) {
	if option == telOptTTYPE && len(payload) > 0 && payload[0] == ttypeSEND {
		msg := []byte{iacByte, sbByte, telOptTTYPE, ttypeIS}
		msg = append(msg, []byte(terminalType)...)
		msg = append(msg, iacByte, seByte)
		_, _ = p.conn.Write(msg)
		return
	}
	// Any other subnegotiation we don't understand -- already consumed by
	// the caller's scan; nothing to reply with.
}

// findSequence finds the index of a short byte sequence in `data`,
// starting at `from`, or -1.
func findSequence(data []byte, from int, needle []byte) int {
	if from < 0 {
		from = 0
	}
	for i := from; i <= len(data)-len(needle); i++ {
		match := true
		for j := range needle {
			if data[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

var (
	loginPattern    = regexp.MustCompile(`(?i)login[: ]*$`)
	usernamePattern = regexp.MustCompile(`(?i)username[: ]*$`)
	passwordPattern = regexp.MustCompile(`(?i)password[: ]*$`)
)

// diagnosticSnippetMax caps how much of the received-but-unmatched buffer
// to echo back in a timeout error.
const diagnosticSnippetMax = 500

// OpenTelnetSession opens a raw TCP connection and hand-rolls the Telnet
// transport (IAC option negotiation via telnetIacParser, then a plaintext
// login sequence) rather than using a third-party Telnet client library --
// see README.md's transport section for why: most maintained options
// either own the whole prompt-matching loop themselves in a way that
// doesn't compose with the shared prompt-detection logic, or are thin/
// unmaintained wrappers.
//
// Retries once, with an entirely fresh TCP connection, if the login
// sequence times out having received literally zero bytes from the
// device -- confirmed live via packet capture against a real OLT: its
// first Telnet connection from a given source IP after a while goes
// completely silent (most likely blocking on a reverse-DNS lookup of the
// connecting address before it ever prints a banner), while a second,
// near-immediate connection gets an instant response. This turns that
// intermittent false failure into a transparent success for the operator
// instead of requiring them to notice and click Run again. Does NOT retry
// for any other failure (wrong credentials, a login sequence that got
// partway through and then stalled, connection refused, etc.) -- only the
// exact "connected, but the device never said a word" case.
func OpenTelnetSession(params ConnectionParams) (ShellSession, error) {
	session, err := openTelnetSessionAttempt(params)
	if err == nil {
		return session, nil
	}

	cliErr, ok := err.(*olterr.OltCliError)
	if !ok || !cliErr.NoDataReceived {
		return nil, err
	}

	return openTelnetSessionAttempt(params)
}

func openTelnetSessionAttempt(params ConnectionParams) (ShellSession, error) {
	addr := net.JoinHostPort(params.Host, strconv.Itoa(params.Port))
	conn, err := net.DialTimeout("tcp", addr, time.Duration(params.ConnectTimeoutMs)*time.Millisecond)
	if err != nil {
		return nil, olterr.New(olterr.TypeConnection, "Telnet connection failed: "+err.Error())
	}

	parser := newTelnetIacParser(conn)
	chunks := make(chan []byte, 256)
	go func() {
		defer close(chunks)
		buf := make([]byte, 4096)
		for {
			n, readErr := conn.Read(buf)
			if n > 0 {
				stripped := parser.feed(buf[:n])
				if len(stripped) > 0 {
					chunks <- stripped
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	source := &DataSource{
		Chunks: chunks,
		Write: func(data string) error {
			_, writeErr := conn.Write([]byte(data))
			return writeErr
		},
	}

	if err := runLoginSequence(source, params); err != nil {
		conn.Close()
		return nil, err
	}

	session := NewStreamShellSession(source, func() error {
		return conn.Close()
	})

	if err := session.Open(params); err != nil {
		_ = session.Close()
		return nil, err
	}

	return session, nil
}

// runLoginSequence is an expect-style state machine for Telnet's plaintext
// login sequence: login/username prompt -> username, password prompt ->
// password, and an optional third enable-password step. Each step has its
// own timeout.
func runLoginSequence(source *DataSource, params ConnectionParams) error {
	connectTimeout := time.Duration(params.ConnectTimeoutMs) * time.Millisecond

	if _, err := waitForSubstring(source, []*regexp.Regexp{loginPattern, usernamePattern}, connectTimeout); err != nil {
		return err
	}
	if err := source.Write(params.Username + crlf); err != nil {
		return olterr.New(olterr.TypeConnection, "failed to write username: "+err.Error())
	}

	if _, err := waitForSubstring(source, []*regexp.Regexp{passwordPattern}, connectTimeout); err != nil {
		return err
	}
	if err := source.Write(params.Password + crlf); err != nil {
		return olterr.New(olterr.TypeConnection, "failed to write password: "+err.Error())
	}

	if params.EnablePassword != "" {
		// Best-effort: not every device has a third prompt. Use a shorter
		// timeout and don't fail the whole connection if it never appears
		// -- capturePrompt() in StreamShellSession.Open() will just
		// capture whatever prompt is showing once this returns.
		shortTimeout := connectTimeout
		if shortTimeout > 3*time.Second {
			shortTimeout = 3 * time.Second
		}
		if _, err := waitForSubstring(source, []*regexp.Regexp{passwordPattern}, shortTimeout); err == nil {
			_ = source.Write(params.EnablePassword + crlf)
		}
	}

	return nil
}

func waitForSubstring(source *DataSource, patterns []*regexp.Regexp, timeout time.Duration) (int, error) {
	var buffer bytes.Buffer
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case chunk, ok := <-source.Chunks:
			if !ok {
				return -1, olterr.New(olterr.TypeAuth, "session closed while waiting for a login/password prompt")
			}
			buffer.Write(chunk)
			text := buffer.String()
			for idx, p := range patterns {
				if p.MatchString(text) {
					return idx, nil
				}
			}
		case <-timer.C:
			// Real OLT CLIs vary a lot in exact login-prompt wording
			// ("Login:", "User Name:", "login as:", etc.) -- when the
			// generic regex set above doesn't match, echoing back what
			// the device actually sent (rather than a bare "timed out")
			// is the difference between a one-line diagnosis and blind
			// guessing. This is exactly the class of thing that only
			// shows up against real hardware.
			received := "(nothing received at all)"
			noDataReceived := buffer.Len() == 0
			if !noDataReceived {
				snippet := buffer.String()
				if len(snippet) > diagnosticSnippetMax {
					snippet = snippet[:diagnosticSnippetMax]
				}
				received = fmt.Sprintf("%q", snippet)
			}
			err := olterr.New(
				olterr.TypeAuth,
				"Timed out waiting for a login/password prompt. Device sent: "+received,
			)
			err.NoDataReceived = noDataReceived
			return -1, err
		}
	}
}
