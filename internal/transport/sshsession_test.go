package transport

import (
	"net"
	"strconv"
	"testing"
	"time"
)

func startMockSSHServer(t *testing.T, username, password string, responses map[string]string) (host string, port int) {
	t.Helper()
	server, err := newMockSSHServer(username, password, responses)
	if err != nil {
		t.Fatalf("failed to start mock SSH server: %v", err)
	}
	go server.serve()
	t.Cleanup(server.stop)

	h, p, err := net.SplitHostPort(server.addr())
	if err != nil {
		t.Fatalf("failed to parse mock server addr: %v", err)
	}
	portNum, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("failed to parse mock server port: %v", err)
	}
	return h, portNum
}

func TestOpenSSHSessionRunsCommands(t *testing.T) {
	host, port := startMockSSHServer(t, "admin", "s3cret", map[string]string{
		"show version": "OLT-Gateway-Mock v1.0",
	})

	session, err := OpenSSHSession(ConnectionParams{
		Host:                host,
		Port:                port,
		Protocol:            "ssh",
		Username:            "admin",
		Password:            "s3cret",
		ConnectTimeoutMs:    2000,
		PerCommandTimeoutMs: 2000,
	})
	if err != nil {
		t.Fatalf("OpenSSHSession failed: %v", err)
	}
	defer session.Close()

	output, truncated, err := session.SendAndWaitForPrompt("show version", 2*time.Second)
	if err != nil {
		t.Fatalf("SendAndWaitForPrompt failed: %v", err)
	}
	if truncated {
		t.Error("did not expect truncation")
	}
	if output != "OLT-Gateway-Mock v1.0" {
		t.Errorf("got output %q, want %q", output, "OLT-Gateway-Mock v1.0")
	}
}

// TestOpenSSHSessionRequestsWideEnoughPty is a regression test: confirmed
// live against a real Juniper vMX that RequestPty's (term, h, w, ...)
// argument order -- HEIGHT then WIDTH -- had been passed backwards here
// (80, 40 read like "80 columns, 40 rows" but actually negotiated 80 ROWS
// x 40 COLUMNS), giving a much narrower effective terminal than intended.
// At 40 columns, Junos's CLI redraws/line-wraps any command whose echoed
// "prompt + command" text exceeds that width (e.g. "rabi> show
// interfaces terse | display json", 43 characters), splicing leftover
// redraw bytes into the captured output and breaking JSON parsing. See
// sshsession.go's own doc comment on this call for the full story.
func TestOpenSSHSessionRequestsWideEnoughPty(t *testing.T) {
	server, err := newMockSSHServer("admin", "s3cret", nil)
	if err != nil {
		t.Fatalf("failed to start mock SSH server: %v", err)
	}
	go server.serve()
	t.Cleanup(server.stop)

	h, p, err := net.SplitHostPort(server.addr())
	if err != nil {
		t.Fatalf("failed to parse mock server addr: %v", err)
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("failed to parse mock server port: %v", err)
	}

	session, err := OpenSSHSession(ConnectionParams{
		Host:                h,
		Port:                port,
		Protocol:            "ssh",
		Username:            "admin",
		Password:            "s3cret",
		ConnectTimeoutMs:    2000,
		PerCommandTimeoutMs: 2000,
	})
	if err != nil {
		t.Fatalf("OpenSSHSession failed: %v", err)
	}
	defer session.Close()

	columns, rows := server.ptyDimensions()
	if columns != 80 {
		t.Errorf("negotiated pty Columns = %d, want 80 -- a narrower terminal risks line-wrap redraws corrupting output on longer commands", columns)
	}
	if rows != 40 {
		t.Errorf("negotiated pty Rows = %d, want 40", rows)
	}
}

func TestOpenSSHSessionWrongPasswordFails(t *testing.T) {
	host, port := startMockSSHServer(t, "admin", "s3cret", nil)

	_, err := OpenSSHSession(ConnectionParams{
		Host:                host,
		Port:                port,
		Protocol:            "ssh",
		Username:            "admin",
		Password:            "wrong-password",
		ConnectTimeoutMs:    2000,
		PerCommandTimeoutMs: 2000,
	})
	if err == nil {
		t.Fatal("expected an error for a wrong password, got nil")
	}
}

func TestOpenSSHSessionUnreachableHostFails(t *testing.T) {
	_, err := OpenSSHSession(ConnectionParams{
		Host:                "127.0.0.1",
		Port:                1, // nothing listens on port 1
		Protocol:            "ssh",
		Username:            "admin",
		Password:            "s3cret",
		ConnectTimeoutMs:    500,
		PerCommandTimeoutMs: 500,
	})
	if err == nil {
		t.Fatal("expected a connection error, got nil")
	}
}
