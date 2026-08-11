package transport

import (
	"net"
	"strconv"
	"testing"
	"time"
)

// fakeConn is a minimal net.Conn double used only to capture what
// telnetIacParser writes back in response to negotiation, without needing
// a real socket pair.
type fakeConn struct {
	net.Conn
	written []byte
}

func (f *fakeConn) Write(b []byte) (int, error) {
	f.written = append(f.written, b...)
	return len(b), nil
}

func TestTelnetIacParserStripsSimpleNegotiationAndReplies(t *testing.T) {
	conn := &fakeConn{}
	parser := newTelnetIacParser(conn)

	out := parser.feed([]byte{'h', 'i', iacByte, doByte, 1, 'b', 'y', 'e'}) // DO ECHO (option 1)
	if string(out) != "hibye" {
		t.Fatalf("got %q, want %q", out, "hibye")
	}
	want := []byte{iacByte, wontByte, 1} // refuses ECHO, since it's not TTYPE
	if string(conn.written) != string(want) {
		t.Fatalf("got reply %v, want %v", conn.written, want)
	}
}

// TestTelnetIacParserRefusesWillOfferWithDont is the regression test for a
// real-hardware bug found via packet capture: a peer's WILL offer (e.g. a
// real OLT sending "IAC WILL ECHO") must be refused with DONT, not WONT --
// WONT is a statement about our own behavior and is a malformed reply to a
// WILL offer, even though the OLT that surfaced this tolerated it.
func TestTelnetIacParserRefusesWillOfferWithDont(t *testing.T) {
	conn := &fakeConn{}
	parser := newTelnetIacParser(conn)

	out := parser.feed([]byte{iacByte, willByte, 1}) // peer offers WILL ECHO
	if len(out) != 0 {
		t.Fatalf("got %q, want no plain-text output", out)
	}
	want := []byte{iacByte, dontByte, 1}
	if string(conn.written) != string(want) {
		t.Fatalf("got reply %v, want %v (DONT, not WONT)", conn.written, want)
	}
}

func TestTelnetIacParserDoesNotReplyToWontOrDont(t *testing.T) {
	conn := &fakeConn{}
	parser := newTelnetIacParser(conn)

	parser.feed([]byte{iacByte, wontByte, 1, iacByte, dontByte, 3})
	if len(conn.written) != 0 {
		t.Fatalf("did not expect any reply to WONT/DONT, got %v", conn.written)
	}
}

func TestTelnetIacParserHandlesNegotiationSplitAcrossChunks(t *testing.T) {
	conn := &fakeConn{}
	parser := newTelnetIacParser(conn)

	out1 := parser.feed([]byte{'a', 'b', 'c', iacByte})
	if string(out1) != "abc" {
		t.Fatalf("got %q, want %q (IAC byte should be held pending)", out1, "abc")
	}
	out2 := parser.feed([]byte{doByte, telOptTTYPE, 'd', 'e', 'f'})
	if string(out2) != "def" {
		t.Fatalf("got %q, want %q", out2, "def")
	}
	// TTYPE negotiation must be accepted (WILL), not refused.
	want := []byte{iacByte, willByte, telOptTTYPE}
	if string(conn.written) != string(want) {
		t.Fatalf("got reply %v, want %v (agree to TTYPE)", conn.written, want)
	}
}

func TestTelnetIacParserHandlesSubnegotiationSplitAcrossChunks(t *testing.T) {
	conn := &fakeConn{}
	parser := newTelnetIacParser(conn)

	out1 := parser.feed([]byte{'x', iacByte, sbByte, telOptTTYPE, ttypeSEND})
	if string(out1) != "x" {
		t.Fatalf("got %q, want %q (incomplete SB...SE should be held pending)", out1, "x")
	}
	out2 := parser.feed([]byte{iacByte, seByte, 'y'})
	if string(out2) != "y" {
		t.Fatalf("got %q, want %q", out2, "y")
	}

	wantPrefix := []byte{iacByte, sbByte, telOptTTYPE, ttypeIS}
	wantSuffix := []byte{iacByte, seByte}
	got := conn.written
	if len(got) < len(wantPrefix)+len(wantSuffix) {
		t.Fatalf("reply too short: %v", got)
	}
	if string(got[:len(wantPrefix)]) != string(wantPrefix) {
		t.Fatalf("got reply prefix %v, want %v", got[:len(wantPrefix)], wantPrefix)
	}
	if string(got[len(got)-len(wantSuffix):]) != string(wantSuffix) {
		t.Fatalf("got reply suffix %v, want %v", got[len(got)-len(wantSuffix):], wantSuffix)
	}
	terminalTypeSent := string(got[len(wantPrefix) : len(got)-len(wantSuffix)])
	if terminalTypeSent != terminalType {
		t.Fatalf("got terminal type %q, want %q", terminalTypeSent, terminalType)
	}
}

func TestTelnetIacParserEscapesDoubledIac(t *testing.T) {
	conn := &fakeConn{}
	parser := newTelnetIacParser(conn)

	out := parser.feed([]byte{'a', iacByte, iacByte, 'b'}) // IAC IAC == literal 0xff byte
	if len(out) != 3 || out[0] != 'a' || out[1] != iacByte || out[2] != 'b' {
		t.Fatalf("got %v, want literal a, 0xff, b", out)
	}
	if len(conn.written) != 0 {
		t.Fatalf("did not expect a reply for an escaped IAC, got %v", conn.written)
	}
}

func startMockTelnetServer(t *testing.T, opts mockTelnetServerOpts) (host string, port int) {
	t.Helper()
	server, err := newMockTelnetServer(opts)
	if err != nil {
		t.Fatalf("failed to start mock telnet server: %v", err)
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

func TestOpenTelnetSessionLoginAndRunCommand(t *testing.T) {
	host, port := startMockTelnetServer(t, mockTelnetServerOpts{
		username:  "admin",
		password:  "s3cret",
		responses: map[string]string{"show version": "OLT-Gateway-Mock v1.0"},
	})

	session, err := OpenTelnetSession(ConnectionParams{
		Host:                host,
		Port:                port,
		Protocol:            "telnet",
		Username:            "admin",
		Password:            "s3cret",
		ConnectTimeoutMs:    2000,
		PerCommandTimeoutMs: 2000,
	})
	if err != nil {
		t.Fatalf("OpenTelnetSession failed: %v", err)
	}
	defer session.Close()

	output, _, err := session.SendAndWaitForPrompt("show version", 2*time.Second)
	if err != nil {
		t.Fatalf("SendAndWaitForPrompt failed: %v", err)
	}
	if output != "OLT-Gateway-Mock v1.0" {
		t.Errorf("got output %q, want %q", output, "OLT-Gateway-Mock v1.0")
	}
}

// TestOpenTelnetSessionSurvivesTtypeNegotiation is the regression test for
// the real-hardware bug: a device that withholds its login prompt until it
// gets a real TTYPE answer must still succeed against this client, not
// hang until connectTimeoutMs expires.
func TestOpenTelnetSessionSurvivesTtypeNegotiation(t *testing.T) {
	host, port := startMockTelnetServer(t, mockTelnetServerOpts{
		username:                "admin",
		password:                "s3cret",
		responses:               map[string]string{"show version": "OLT-Gateway-Mock v1.0"},
		requireTtypeNegotiation: true,
	})

	session, err := OpenTelnetSession(ConnectionParams{
		Host:                host,
		Port:                port,
		Protocol:            "telnet",
		Username:            "admin",
		Password:            "s3cret",
		ConnectTimeoutMs:    2000,
		PerCommandTimeoutMs: 2000,
	})
	if err != nil {
		t.Fatalf("OpenTelnetSession failed against a TTYPE-requiring server: %v", err)
	}
	defer session.Close()

	output, _, err := session.SendAndWaitForPrompt("show version", 2*time.Second)
	if err != nil {
		t.Fatalf("SendAndWaitForPrompt failed: %v", err)
	}
	if output != "OLT-Gateway-Mock v1.0" {
		t.Errorf("got output %q, want %q", output, "OLT-Gateway-Mock v1.0")
	}
}

// TestOpenTelnetSessionRetriesOnceOnSilentConnection is the regression
// test for the second real-hardware finding (via packet capture): a first
// Telnet connection that goes completely silent shouldn't surface as a
// failure if a second, fresh connection succeeds -- confirmed live against
// a real OLT where the first connection from a given source IP got zero
// bytes back, but a second connection immediately after got the full
// banner and login prompt.
func TestOpenTelnetSessionRetriesOnceOnSilentConnection(t *testing.T) {
	host, port := startMockTelnetServer(t, mockTelnetServerOpts{
		username:          "admin",
		password:          "s3cret",
		responses:         map[string]string{"show version": "OLT-Gateway-Mock v1.0"},
		silentConnections: 1, // the first connection accepted gets nothing written to it
	})

	session, err := OpenTelnetSession(ConnectionParams{
		Host:                host,
		Port:                port,
		Protocol:            "telnet",
		Username:            "admin",
		Password:            "s3cret",
		// Short on purpose -- the first (silent) connection attempt needs
		// to time out quickly for this test to run fast; the retry's
		// connection succeeds near-instantly against the mock server.
		ConnectTimeoutMs:    300,
		PerCommandTimeoutMs: 2000,
	})
	if err != nil {
		t.Fatalf("OpenTelnetSession failed despite the retry: %v", err)
	}
	defer session.Close()

	output, _, err := session.SendAndWaitForPrompt("show version", 2*time.Second)
	if err != nil {
		t.Fatalf("SendAndWaitForPrompt failed: %v", err)
	}
	if output != "OLT-Gateway-Mock v1.0" {
		t.Errorf("got output %q, want %q", output, "OLT-Gateway-Mock v1.0")
	}
}

// TestOpenTelnetSessionDoesNotRetryForeverOnPersistentSilence confirms the
// retry is bounded to exactly one extra attempt, not an unbounded loop --
// a server that's silent on every connection must still fail, not hang.
func TestOpenTelnetSessionDoesNotRetryForeverOnPersistentSilence(t *testing.T) {
	host, port := startMockTelnetServer(t, mockTelnetServerOpts{
		username:          "admin",
		password:          "s3cret",
		silentConnections: 1000, // effectively "always silent" for this test's duration
	})

	_, err := OpenTelnetSession(ConnectionParams{
		Host:                host,
		Port:                port,
		Protocol:            "telnet",
		Username:            "admin",
		Password:            "s3cret",
		ConnectTimeoutMs:    200,
		PerCommandTimeoutMs: 200,
	})
	if err == nil {
		t.Fatal("expected an error when the device is silent on every attempt, got nil")
	}
}

func TestOpenTelnetSessionWrongCredentialsFails(t *testing.T) {
	host, port := startMockTelnetServer(t, mockTelnetServerOpts{
		username: "admin",
		password: "s3cret",
	})

	_, err := OpenTelnetSession(ConnectionParams{
		Host:                host,
		Port:                port,
		Protocol:            "telnet",
		Username:            "admin",
		Password:            "wrong-password",
		ConnectTimeoutMs:    300,
		PerCommandTimeoutMs: 300,
	})
	if err == nil {
		t.Fatal("expected an error for wrong credentials, got nil")
	}
}

func TestOpenTelnetSessionUnreachableHostFails(t *testing.T) {
	_, err := OpenTelnetSession(ConnectionParams{
		Host:                "127.0.0.1",
		Port:                1,
		Protocol:            "telnet",
		Username:            "admin",
		Password:            "s3cret",
		ConnectTimeoutMs:    500,
		PerCommandTimeoutMs: 500,
	})
	if err == nil {
		t.Fatal("expected a connection error, got nil")
	}
}
