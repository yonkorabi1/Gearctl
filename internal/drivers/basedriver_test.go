package drivers

import (
	"testing"
	"time"

	"github.com/onu-platform/gearctl/internal/olterr"
	"github.com/onu-platform/gearctl/internal/transport"
)

// fakeShellSession is a no-network ShellSession double so BaseDriver's
// command-ordering, fail-fast-on-timeout, and output-passthrough logic can
// be tested without opening a real SSH/Telnet connection (those transports
// are exercised separately, against mock servers). Every driver
// (RawDriver, CiscoIOSDriver, ...) embeds BaseDriver, so this logic only
// needs to be tested once, here.
type fakeShellSession struct {
	responses map[string]fakeResponse
	sent      []string
	// hints records the expectedPrompt argument SendAndWaitForPromptHint
	// was called with for each corresponding entry in sent (same index) --
	// lets tests assert a caller actually predicted and passed a hint,
	// not just that the command was sent. currentPrompt lets a test
	// control what CurrentPrompt() reports, the same way responses
	// controls what each command "returns".
	hints         []string
	currentPrompt string
	closed        bool
}

type fakeResponse struct {
	output    string
	truncated bool
	err       error
}

func (f *fakeShellSession) Open(transport.ConnectionParams) error { return nil }

func (f *fakeShellSession) SendAndWaitForPrompt(command string, timeout time.Duration) (string, bool, error) {
	return f.SendAndWaitForPromptHint(command, timeout, "")
}

func (f *fakeShellSession) SendAndWaitForPromptHint(command string, _ time.Duration, expectedPrompt string) (string, bool, error) {
	f.sent = append(f.sent, command)
	f.hints = append(f.hints, expectedPrompt)
	resp, ok := f.responses[command]
	if !ok {
		return "", false, nil
	}
	if resp.err != nil {
		return "", false, resp.err
	}
	return resp.output, resp.truncated, nil
}

func (f *fakeShellSession) CurrentPrompt() string {
	return f.currentPrompt
}

func (f *fakeShellSession) Close() error {
	f.closed = true
	return nil
}

func TestBaseDriverRunCommandsOrderingAndOutput(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show version":   {output: "v1.0"},
		"show interface": {output: "up", truncated: true},
	}}
	d := &BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}

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

func TestBaseDriverFailsFastOnTimeout(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"slow command": {err: olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt")},
	}}
	d := &BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}

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

func TestBaseDriverClassifiesNonTimeoutErrorAsSessionClosed(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"cmd": {err: olterr.New(olterr.TypeProtocol, "session closed while waiting for prompt")},
	}}
	d := &BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}

	results, err := d.RunCommands([]string{"cmd"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results[0].Error != "session_closed" {
		t.Errorf("results[0].Error = %q, want \"session_closed\"", results[0].Error)
	}
}

func TestBaseDriverRunCommandsBeforeConnect(t *testing.T) {
	d := &BaseDriver{}
	_, err := d.RunCommands([]string{"show version"})
	if err == nil {
		t.Fatal("expected an error when RunCommands is called before Connect")
	}
}

func TestBaseDriverDisconnectClosesSession(t *testing.T) {
	fake := &fakeShellSession{}
	d := &BaseDriver{session: fake}
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

// TestBaseDriverRunCommandUntilCompleteBeforeConnect mirrors
// TestBaseDriverRunCommandsBeforeConnect for the new method.
func TestBaseDriverRunCommandUntilCompleteBeforeConnect(t *testing.T) {
	d := &BaseDriver{}
	_, err := d.RunCommandUntilComplete("show version", nil)
	if err == nil {
		t.Fatal("expected an error when RunCommandUntilComplete is called before Connect")
	}
}

// TestBaseDriverRunCommandUntilCompleteFallsBackWithoutCompletionAwareSession
// is a regression test for RunCommandUntilComplete's documented fallback:
// fakeShellSession does NOT implement transport.CompletionAwareSession
// (SendAndWaitForPromptUntilComplete), so this must behave exactly like
// RunCommandWithHint -- isComplete is simply never consulted, not an
// error, not a hang.
func TestBaseDriverRunCommandUntilCompleteFallsBackWithoutCompletionAwareSession(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show version": {output: "v1.0"},
	}}
	d := &BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}

	isCompleteCalled := false
	result, err := d.RunCommandUntilComplete("show version", func(string) bool {
		isCompleteCalled = true
		return true
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output == nil || *result.Output != "v1.0" {
		t.Errorf("result.Output = %v, want \"v1.0\"", result.Output)
	}
	if isCompleteCalled {
		t.Error("isComplete should never be invoked when the session doesn't implement transport.CompletionAwareSession")
	}
}

// fakeCompletionAwareSession is a minimal ShellSession +
// transport.CompletionAwareSession double -- unlike fakeShellSession, it
// implements SendAndWaitForPromptUntilComplete, recording the isComplete
// function it was actually called with so a test can assert
// RunCommandUntilComplete really threads it through rather than silently
// dropping it.
type fakeCompletionAwareSession struct {
	response           fakeResponse
	receivedIsComplete func(string) bool
}

func (f *fakeCompletionAwareSession) Open(transport.ConnectionParams) error { return nil }

func (f *fakeCompletionAwareSession) SendAndWaitForPrompt(command string, timeout time.Duration) (string, bool, error) {
	return f.SendAndWaitForPromptUntilComplete(command, timeout, "", nil)
}

func (f *fakeCompletionAwareSession) SendAndWaitForPromptHint(command string, timeout time.Duration, expectedPrompt string) (string, bool, error) {
	return f.SendAndWaitForPromptUntilComplete(command, timeout, expectedPrompt, nil)
}

func (f *fakeCompletionAwareSession) SendAndWaitForPromptUntilComplete(
	_ string, _ time.Duration, _ string, isComplete func(string) bool,
) (string, bool, error) {
	f.receivedIsComplete = isComplete
	if f.response.err != nil {
		return "", false, f.response.err
	}
	return f.response.output, f.response.truncated, nil
}

func (f *fakeCompletionAwareSession) CurrentPrompt() string { return "" }
func (f *fakeCompletionAwareSession) Close() error          { return nil }

// TestBaseDriverRunCommandUntilCompleteDelegatesToCompletionAwareSession
// confirms the happy path: when the underlying session DOES implement
// transport.CompletionAwareSession, RunCommandUntilComplete calls
// SendAndWaitForPromptUntilComplete (not the plain Hint variant) and
// passes the caller's isComplete straight through, unmodified.
func TestBaseDriverRunCommandUntilCompleteDelegatesToCompletionAwareSession(t *testing.T) {
	fake := &fakeCompletionAwareSession{response: fakeResponse{output: `{"a":1}`}}
	d := &BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}

	isComplete := func(s string) bool { return s == `{"a":1}` }
	result, err := d.RunCommandUntilComplete("show interfaces | display json", isComplete)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output == nil || *result.Output != `{"a":1}` {
		t.Errorf("result.Output = %v, want %q", result.Output, `{"a":1}`)
	}
	if fake.receivedIsComplete == nil {
		t.Fatal("expected SendAndWaitForPromptUntilComplete to receive a non-nil isComplete")
	}
	if !fake.receivedIsComplete(`{"a":1}`) {
		t.Error("the isComplete function received wasn't the one actually passed in (or was called incorrectly)")
	}
}

// TestBaseDriverRunCommandUntilCompleteClassifiesTimeoutError mirrors
// RunCommandWithHint's own error-classing test -- same fail-fast-on-
// timeout contract, just reached via the CompletionAwareSession path.
func TestBaseDriverRunCommandUntilCompleteClassifiesTimeoutError(t *testing.T) {
	fake := &fakeCompletionAwareSession{response: fakeResponse{err: olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt")}}
	d := &BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}

	result, err := d.RunCommandUntilComplete("show interfaces | display json", func(string) bool { return true })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "timeout" {
		t.Errorf("result.Error = %q, want \"timeout\"", result.Error)
	}
}
