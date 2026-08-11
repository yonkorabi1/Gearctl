package drivers

import (
	"testing"
	"time"

	"github.com/onu-platform/olt-cli-gateway/internal/olterr"
	"github.com/onu-platform/olt-cli-gateway/internal/transport"
)

// fakeShellSession is a no-network ShellSession double so RawDriver's
// command-ordering, fail-fast-on-timeout, and output-passthrough logic can
// be tested without opening a real SSH/Telnet connection (those transports
// are exercised separately, against mock servers).
type fakeShellSession struct {
	responses map[string]fakeResponse
	sent      []string
	closed    bool
}

type fakeResponse struct {
	output    string
	truncated bool
	err       error
}

func (f *fakeShellSession) Open(transport.ConnectionParams) error { return nil }

func (f *fakeShellSession) SendAndWaitForPrompt(command string, _ time.Duration) (string, bool, error) {
	f.sent = append(f.sent, command)
	resp, ok := f.responses[command]
	if !ok {
		return "", false, nil
	}
	if resp.err != nil {
		return "", false, resp.err
	}
	return resp.output, resp.truncated, nil
}

func (f *fakeShellSession) Close() error {
	f.closed = true
	return nil
}

func TestRawDriverRunCommandsOrderingAndOutput(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show version":   {output: "v1.0"},
		"show interface": {output: "up", truncated: true},
	}}
	d := &RawDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}

	results, err := d.RunCommands([]string{"show version", "show interface"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Output == nil || *results[0].Output != "v1.0" {
		t.Errorf("results[0].Output = %v, want \"v1.0\"", results[0].Output)
	}
	if results[1].Output == nil || *results[1].Output != "up" || !results[1].Truncated {
		t.Errorf("results[1] = %+v, want output=up truncated=true", results[1])
	}
	if len(fake.sent) != 2 || fake.sent[0] != "show version" || fake.sent[1] != "show interface" {
		t.Errorf("commands sent out of order: %v", fake.sent)
	}
}

func TestRawDriverFailsFastOnTimeout(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"slow command": {err: olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt")},
	}}
	d := &RawDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}

	results, err := d.RunCommands([]string{"slow command", "never reached"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (fail-fast should stop after the timeout)", len(results))
	}
	if results[0].Error != "timeout" {
		t.Errorf("results[0].Error = %q, want \"timeout\"", results[0].Error)
	}
	if results[0].Output != nil {
		t.Errorf("results[0].Output = %v, want nil", results[0].Output)
	}
	if len(fake.sent) != 1 {
		t.Errorf("expected only the first command to be sent, got %v", fake.sent)
	}
}

func TestRawDriverClassifiesNonTimeoutErrorAsSessionClosed(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"cmd": {err: olterr.New(olterr.TypeProtocol, "session closed while waiting for prompt")},
	}}
	d := &RawDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}

	results, err := d.RunCommands([]string{"cmd"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results[0].Error != "session_closed" {
		t.Errorf("results[0].Error = %q, want \"session_closed\"", results[0].Error)
	}
}

func TestRawDriverRunCommandsBeforeConnect(t *testing.T) {
	d := NewRawDriver()
	_, err := d.RunCommands([]string{"show version"})
	if err == nil {
		t.Fatal("expected an error when RunCommands is called before Connect")
	}
}

func TestRawDriverDisconnectClosesSession(t *testing.T) {
	fake := &fakeShellSession{}
	d := &RawDriver{session: fake}
	if err := d.Disconnect(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fake.closed {
		t.Error("expected the underlying session to be closed")
	}
	// Safe to call again (session already nil) -- mirrors ShellSession.Close's contract.
	if err := d.Disconnect(); err != nil {
		t.Fatalf("unexpected error on second Disconnect: %v", err)
	}
}

func TestRawDriverVendorIsWildcard(t *testing.T) {
	if got := NewRawDriver().Vendor(); got != "*" {
		t.Errorf("Vendor() = %q, want \"*\"", got)
	}
}

func TestResolveAlwaysReturnsRawDriver(t *testing.T) {
	for _, vendor := range []string{"", "huawei", "zte", "unknown-vendor"} {
		d := Resolve(vendor)
		if d.Vendor() != "*" {
			t.Errorf("Resolve(%q).Vendor() = %q, want \"*\"", vendor, d.Vendor())
		}
	}
}
