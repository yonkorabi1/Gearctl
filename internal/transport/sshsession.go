package transport

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/onu-platform/gearctl/internal/olterr"
)

// OpenSSHSession opens an SSH connection and an interactive shell channel
// (session.Shell(), not a one-shot exec) -- OLT CLIs are stateful
// multi-command sessions (pagination state, privilege level, etc. persist
// across commands within one connection), which a one-shot exec doesn't
// model.
//
// The ssh package handles the actual SSH-protocol authentication (password
// auth) natively -- this file only needs to open the shell channel
// afterward and hand it to StreamShellSession for prompt detection. Some
// OLT CLIs gate a SECOND, in-band prompt after SSH auth succeeds (e.g. an
// "enable"-style privileged-mode password); StreamShellSession.Open()
// doesn't handle that today (no confirmed real-hardware case for it over
// SSH specifically -- see the Telnet transport's login-sequence handling,
// which does need it).
func OpenSSHSession(params ConnectionParams) (ShellSession, error) {
	config := &ssh.ClientConfig{
		User: params.Username,
		Auth: []ssh.AuthMethod{ssh.Password(params.Password)},
		// OLT management IPs don't come with a pre-shared known host key in
		// this platform's model (platform-api resolves the target from
		// inventory, not from an operator manually verifying a
		// fingerprint) -- matches the original TypeScript implementation's
		// ssh2 usage, which also didn't verify host keys.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         time.Duration(params.ConnectTimeoutMs) * time.Millisecond,
		// Some OLT SSH stacks are old enough to need legacy algorithm
		// support -- not enabled by default here since it weakens security
		// for every other target. If a real OLT fails to negotiate, this
		// is the place to add a Config.Ciphers/KeyExchanges override
		// scoped to that vendor's driver once one exists.
	}

	addr := net.JoinHostPort(params.Host, strconv.Itoa(params.Port))
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		errType := olterr.TypeConnection
		if strings.Contains(strings.ToLower(err.Error()), "authenticat") {
			errType = olterr.TypeAuth
		}
		return nil, olterr.New(errType, "SSH connection failed: "+err.Error())
	}

	sshSession, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, olterr.New(olterr.TypeProtocol, "Failed to open shell channel: "+err.Error())
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	// x/crypto/ssh's RequestPty takes (term string, h, w int, ...) --
	// HEIGHT then WIDTH, not the (width, height) order the two bare
	// numbers below might suggest at a glance. This was previously
	// swapped here (RequestPty("vt100", 80, 40, modes), i.e. 80 ROWS x 40
	// COLUMNS instead of the intended 80-COLUMNS-wide terminal) -- a real
	// bug confirmed live against a real Juniper vMX: with an effective
	// 40-column width, Junos's CLI redraws/scrolls (its own "..." +
	// tail-of-line convention, the same class of behavior any line-editor
	// uses once the echoed prompt+command exceeds the terminal width) any
	// command whose "prompt + command text" exceeds ~40 visible columns
	// -- e.g. "rabi> show interfaces | display json" (37 chars) -- which
	// splices leftover redraw bytes into the captured output BEFORE the
	// real response and broke JSON parsing (`invalid character 's'
	// looking for beginning of value`, the echoed tail of "display json"
	// itself). Cisco's own commands tested here happened to all stay
	// under 40 visible columns, which is why this went unnoticed until a
	// longer Junos command ("show interfaces terse | display json", 37
	// chars of command alone) crossed that threshold.
	//
	// 40 ROWS x 80 COLUMNS (correct order below) restores the actually-
	// intended terminal size -- generously tall (more rows before a
	// `--More--`-style pause than the conventional 24) and standard-width
	// (80 columns, wide enough for any realistic single-line command this
	// codebase sends without triggering a line-wrap redraw).
	if err := sshSession.RequestPty("vt100", 40, 80, modes); err != nil {
		sshSession.Close()
		client.Close()
		return nil, olterr.New(olterr.TypeProtocol, "Unable to request a pseudo-terminal: "+err.Error())
	}

	stdin, err := sshSession.StdinPipe()
	if err != nil {
		sshSession.Close()
		client.Close()
		return nil, olterr.New(olterr.TypeProtocol, "Failed to open stdin pipe: "+err.Error())
	}
	stdout, err := sshSession.StdoutPipe()
	if err != nil {
		sshSession.Close()
		client.Close()
		return nil, olterr.New(olterr.TypeProtocol, "Failed to open stdout pipe: "+err.Error())
	}

	if err := sshSession.Shell(); err != nil {
		sshSession.Close()
		client.Close()
		return nil, olterr.New(olterr.TypeProtocol, "Failed to start shell: "+err.Error())
	}

	// stdinMu serializes writes to stdin -- the reader goroutine below
	// answers cursor-position queries (see cursorposition.go) on its own,
	// concurrently with whatever real command the caller is writing via
	// source.Write, and both ultimately write the same stdin pipe.
	var stdinMu sync.Mutex
	writeStdin := func(data []byte) error {
		stdinMu.Lock()
		defer stdinMu.Unlock()
		_, writeErr := stdin.Write(data)
		return writeErr
	}

	chunks := make(chan []byte, 256)
	go func() {
		defer close(chunks)
		buf := make([]byte, 4096)
		var cprTail []byte
		for {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				cprTail = respondToCursorPositionQueries(writeStdin, cprTail, chunk)
				chunks <- chunk
			}
			if readErr != nil {
				return
			}
		}
	}()

	source := &DataSource{
		Chunks: chunks,
		Write: func(data string) error {
			return writeStdin([]byte(data))
		},
	}

	shellSession := NewStreamShellSession(source, func() error {
		sshSession.Close()
		return client.Close()
	})

	if err := shellSession.Open(params); err != nil {
		_ = shellSession.Close()
		return nil, err
	}

	return shellSession, nil
}
