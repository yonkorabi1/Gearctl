package drivers

import "testing"

func TestRawDriverVendorIsWildcard(t *testing.T) {
	if got := NewRawDriver().Vendor(); got != "*" {
		t.Errorf("Vendor() = %q, want \"*\"", got)
	}
}

// TestRawDriverInheritsBaseDriverBehavior is a light smoke test that
// embedding actually wires RawDriver up to BaseDriver's session lifecycle
// (the exhaustive ordering/timeout/session-closed cases are tested once,
// against BaseDriver directly, in basedriver_test.go).
func TestRawDriverInheritsBaseDriverBehavior(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show version": {output: "v1.0"},
	}}
	d := &RawDriver{BaseDriver{session: fake}}

	results, err := d.RunCommands([]string{"show version"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Output == nil || *results[0].Output != "v1.0" {
		t.Errorf("results = %+v, want a single \"v1.0\" result", results)
	}

	if err := d.Disconnect(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fake.closed {
		t.Error("expected the underlying session to be closed")
	}
}
