package transport

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// mockSSHServer is an in-process SSH server with a scripted interactive
// shell, used to exercise OpenSSHSession end-to-end without any real
// hardware. It accepts password auth for exactly one username/password
// pair and, once a shell is requested, sends an initial prompt banner and
// then answers each newline-terminated command against a canned
// command->response map.
type mockSSHServer struct {
	listener  net.Listener
	username  string
	password  string
	responses map[string]string
	prompt    string

	mu         sync.Mutex
	stopping   bool
	ptyColumns uint32
	ptyRows    uint32
}

// ptyRequestPayload mirrors RFC 4254's pty-req wire format (and
// x/crypto/ssh's own unexported ptyRequestMsg) closely enough for
// ssh.Unmarshal to decode a real client's request into it -- term
// string, then width/height in CHARACTERS, then width/height in PIXELS,
// then the encoded terminal-modes string. Field order matters here (it's
// positional on the wire), field NAMES don't need to match the
// unexported original.
type ptyRequestPayload struct {
	Term     string
	Columns  uint32
	Rows     uint32
	Width    uint32
	Height   uint32
	Modelist string
}

// ptyDimensions returns the Columns/Rows the most recent pty-req actually
// carried -- lets a test assert on what the client negotiated, not just
// that pty-req happened at all.
func (m *mockSSHServer) ptyDimensions() (columns, rows uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ptyColumns, m.ptyRows
}

func newMockSSHServer(username, password string, responses map[string]string) (*mockSSHServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return &mockSSHServer{
		listener:  listener,
		username:  username,
		password:  password,
		responses: responses,
		prompt:    "OLT#",
	}, nil
}

func (m *mockSSHServer) addr() string {
	return m.listener.Addr().String()
}

func (m *mockSSHServer) serve() {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return
	}

	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if c.User() == m.username && string(password) == m.password {
				return nil, nil
			}
			return nil, errors.New("mock ssh: authentication failed")
		},
	}
	config.AddHostKey(signer)

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
		go m.handleConn(conn, config)
	}
}

func (m *mockSSHServer) handleConn(conn net.Conn, config *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		conn.Close()
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go m.serveSession(channel, requests)
	}
}

func (m *mockSSHServer) serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()

	for req := range requests {
		switch req.Type {
		case "pty-req":
			var payload ptyRequestPayload
			if ssh.Unmarshal(req.Payload, &payload) == nil {
				m.mu.Lock()
				m.ptyColumns = payload.Columns
				m.ptyRows = payload.Rows
				m.mu.Unlock()
			}
			_ = req.Reply(true, nil)
		case "shell":
			_ = req.Reply(true, nil)
			go m.runShell(channel)
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

func (m *mockSSHServer) runShell(channel ssh.Channel) {
	_, _ = channel.Write([]byte("\r\n\r\n" + m.prompt))

	scanner := bufio.NewScanner(channel)
	for scanner.Scan() {
		command := strings.TrimSpace(scanner.Text())
		response, ok := m.responses[command]
		if !ok {
			response = "% Unknown command"
		}
		_, _ = channel.Write([]byte(command + "\r\n" + response + "\r\n" + m.prompt))
	}
}

func (m *mockSSHServer) stop() {
	m.mu.Lock()
	m.stopping = true
	m.mu.Unlock()
	_ = m.listener.Close()
}
