package transport

import (
	"bufio"
	"net"
	"strings"
	"sync"
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
}

// mockTelnetServer is an in-process, hand-rolled Telnet server (no
// third-party library, matching the gateway's own transport) used to
// exercise OpenTelnetSession end-to-end: IAC negotiation, the login
// state machine, prompt capture, and command execution.
type mockTelnetServer struct {
	listener net.Listener
	opts     mockTelnetServerOpts

	mu       sync.Mutex
	stopping bool
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
		if !m.waitForTtypeIsReply(conn) {
			return // client never answered -- stay silent forever, like the real bug.
		}
	}

	if _, err := conn.Write([]byte("login: ")); err != nil {
		return
	}
	reader := bufio.NewReader(conn)
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

// waitForTtypeIsReply reads raw bytes off the connection until it sees a
// complete IAC SB TTYPE IS ... IAC SE subnegotiation reply. Deliberately
// does NOT use telnetIacParser here -- this is the server side, standing
// in for a real device, and must stay independent of the client code
// under test.
func (m *mockTelnetServer) waitForTtypeIsReply(conn net.Conn) bool {
	needle := []byte{iacByte, sbByte, telOptTTYPE, ttypeIS}
	var seen []byte
	buf := make([]byte, 256)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			seen = append(seen, buf[:n]...)
			if idx := indexOf(seen, needle); idx != -1 {
				// Also require the closing IAC SE somewhere after it.
				tail := seen[idx+len(needle):]
				if indexOf(tail, []byte{iacByte, seByte}) != -1 {
					return true
				}
			}
		}
		if err != nil {
			return false
		}
	}
}

func indexOf(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
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

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
