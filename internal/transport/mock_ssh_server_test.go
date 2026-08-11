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

	mu       sync.Mutex
	stopping bool
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
