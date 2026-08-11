package transport

import (
	"bufio"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
)

// mockTelnetServerOpts configures a mockTelnetServer's behavior.
type mockTelnetServerOpts struct {
	username  string
	password  string
	responses map[string]string
	prompt    string

	// requireTtypeNegotiation reproduces the real-hardware bug found during
	// Phase 1 testing: the server sends IAC DO TTYPE, then IAC SB TTYPE
	// SEND IAC SE, and refuses to show the login prompt until it receives a
	// proper IAC SB TTYPE IS <name> IAC SE reply. A client that blanket-
	// refuses (WONT) every negotiation hangs forever against this server,
	// exactly as the real OLT did.
	requireTtypeNegotiation bool

	// requireNawsNegotiation reproduces a real-hardware bug found via
	// packet capture: the server sends IAC DO NAWS up front (alongside DO
	// TTYPE) and, even after TTYPE negotiates correctly, never shows the
	// password prompt unless it also gets a real IAC SB NAWS <w> <h> IAC SE
	// reply -- a client that refuses NAWS outright (the previous behavior)
	// gets stuck exactly like the real OLT did.
	requireNawsNegotiation bool

	// silentConnections reproduces the second real-hardware quirk found via
	// packet capture: the first N connections accepted are held open but
	// get NOTHING written to them at all (as if blocked on a slow reverse-
	// DNS lookup that never resolves before the client gives up) --
	// connections after that behave normally. Used to test
	// OpenTelnetSession's retry-once-on-silence behavior.
	silentConnections int32
}

// mockTelnetServer is an in-process, hand-rolled Telnet server (no
// third-party library, matching the gateway's own transport) used to
// exercise OpenTelnetSession end-to-end: IAC negotiation, the login
// state machine, prompt capture, and command execution.
type mockTelnetServer struct {
	listener net.Listener
	opts     mockTelnetServerOpts

	mu              sync.Mutex
	stopping        bool
	connectionCount atomic.Int32
}

func newMockTelnetServer(opts mockTelnetServerOpts) (*mockTelnetServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if opts.prompt == "" {
		opts.prompt = "OLT#"
	}
	return &mockTelnetServer{listener: listener, opts: opts}, nil
}

func (m *mockTelnetServer) addr() string {
	return m.listener.Addr().String()
}

func (m *mockTelnetServer) serve() {
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			m.mu.Lock()
			stopping := m.stopping
			m.mu.Unlock()
			if stopping {
				return
			}
			continue
		}
		go m.handleConn(conn)
	}
}

func (m *mockTelnetServer) stop() {
	m.mu.Lock()
	m.stopping = true
	m.mu.Unlock()
	_ = m.listener.Close()
}

func (m *mockTelnetServer) handleConn(conn net.Conn) {
	defer conn.Close()

	connNum := m.connectionCount.Add(1)
	if connNum <= m.opts.silentConnections {
		// Hold the connection open, write nothing at all, and just block
		// reading until the peer gives up and closes it -- reproduces the
		// exact "connected but the device never said a word" pattern seen
		// live against a real OLT.
		_, _ = io.Copy(io.Discard, conn)
		return
	}

	// One shared reader for the whole connection -- critical for
	// requireTtypeNegotiation and requireNawsNegotiation to compose
	// correctly. The client answers negotiation the moment it sees the
	// bytes, well before "login: " is even written, so if each check read
	// from its own separate reader (or straight off conn), whichever ran
	// first would silently consume bytes the other one still needed,
	// making it hang forever waiting for a reply that already came and
	// went. A single reader ensures nothing gets consumed twice or lost.
	reader := bufio.NewReader(conn)

	if m.opts.requireNawsNegotiation {
		// Sent up front, same as the real OLT capture -- alongside DO
		// TTYPE, before the login prompt.
		if _, err := conn.Write([]byte{iacByte, doByte, telOptNAWS}); err != nil {
			return
		}
	}

	if m.opts.requireTtypeNegotiation {
		// IAC DO TTYPE, then immediately ask the client to SEND its
		// terminal type via subnegotiation -- matches the real OLT's
		// observed sequence exactly. The server withholds the login
		// prompt until it gets back a proper IAC SB TTYPE IS ... IAC SE.
		if _, err := conn.Write([]byte{iacByte, doByte, telOptTTYPE}); err != nil {
			return
		}
		if _, err := conn.Write([]byte{iacByte, sbByte, telOptTTYPE, ttypeSEND, iacByte, seByte}); err != nil {
			return
		}
	}

	if m.opts.requireNawsNegotiation {
		if !m.waitForNawsReply(reader) {
			return // client never answered -- stay silent forever, like the real bug.
		}
	}

	if m.opts.requireTtypeNegotiation {
		if !m.waitForTtypeIsReply(reader) {
			return // client never answered -- stay silent forever, like the real bug.
		}
	}

	if _, err := conn.Write([]byte("login: ")); err != nil {
		return
	}

	username, err := readLine(reader)
	if err != nil || username != m.opts.username {
		return
	}

	if _, err := conn.Write([]byte("\r\nPassword: ")); err != nil {
		return
	}
	password, err := readLine(reader)
	if err != nil || password != m.opts.password {
		return
	}

	if _, err := conn.Write([]byte("\r\n\r\n" + m.opts.prompt)); err != nil {
		return
	}

	for {
		command, err := readLine(reader)
		if err != nil {
			return
		}
		command = strings.TrimSpace(command)
		response, ok := m.opts.responses[command]
		if !ok {
			response = "% Unknown command"
		}
		if _, err := conn.Write([]byte(command + "\r\n" + response + "\r\n" + m.opts.prompt)); err != nil {
			return
		}
	}
}

// waitForTtypeIsReply scans (byte-by-byte, via the connection's one
// shared bufio.Reader -- see handleConn's comment on why sharing it
// matters) for a complete IAC SB TTYPE IS <name...> IAC SE reply.
// Deliberately does NOT use telnetIacParser here -- this is the server
// side, standing in for a real device, and must stay independent of the
// client code under test.
func (m *mockTelnetServer) waitForTtypeIsReply(reader *bufio.Reader) bool {
	if !scanForNeedle(reader, []byte{iacByte, sbByte, telOptTTYPE, ttypeIS}) {
		return false
	}
	// Consume the variable-length terminal-type name up to the closing
	// IAC SE (unlike NAWS, TTYPE's payload has no fixed length).
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return false
		}
		if b != iacByte {
			continue
		}
		b2, err := reader.ReadByte()
		if err != nil {
			return false
		}
		return b2 == seByte
	}
}

// waitForNawsReply scans for a complete IAC SB NAWS <w-hi> <w-lo> <h-hi>
// <h-lo> IAC SE reply.
func (m *mockTelnetServer) waitForNawsReply(reader *bufio.Reader) bool {
	if !scanForNeedle(reader, []byte{iacByte, sbByte, telOptNAWS}) {
		return false
	}
	rest := make([]byte, 6) // width(2) + height(2) + IAC SE(2)
	if _, err := io.ReadFull(reader, rest); err != nil {
		return false
	}
	return rest[4] == iacByte && rest[5] == seByte
}

// scanForNeedle consumes bytes from reader one at a time until it has read
// exactly through a full occurrence of needle, or the connection ends.
// Bytes read but not part of a (possibly abandoned) partial match are
// discarded -- fine here since these mock-server helpers only ever look
// for negotiation replies, never plain text that might matter later.
func scanForNeedle(reader *bufio.Reader, needle []byte) bool {
	matched := 0
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return false
		}
		if b == needle[matched] {
			matched++
			if matched == len(needle) {
				return true
			}
			continue
		}
		if b == needle[0] {
			matched = 1
		} else {
			matched = 0
		}
	}
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
