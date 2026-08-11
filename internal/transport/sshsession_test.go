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
