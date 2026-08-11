package transport

import (
	"strings"
	"testing"
	"time"

	"github.com/onu-platform/olt-cli-gateway/internal/olterr"
)

// TestStreamShellSessionTracksPromptAcrossModeChange is the end-to-end
// regression test for the real-hardware finding: a command (e.g.
// "config") can change the device's prompt itself. The NEXT command must
// be matched against the NEW prompt, not the one captured at connect
// time, or every subsequent command times out even though the device is
// responding fine -- exactly what happened live against a real OLT where
// "show" commands only worked after "config".
func TestStreamShellSessionTracksPromptAcrossModeChange(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		switch {
		case written == crlf:
			// The initial connect-time prompt capture.
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("\r\n\r\nOLT>")
			}()
		case strings.Contains(written, "config"):
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("config\r\nEntering configuration mode\r\nOLT(config)#")
			}()
		case strings.Contains(written, "show version"):
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("show version\r\nv1.0\r\nOLT(config)#")
			}()
		}
	})

	session := NewStreamShellSession(source, func() error { return nil })
	if err := session.Open(ConnectionParams{ConnectTimeoutMs: 2000}); err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	out1, _, err := session.SendAndWaitForPrompt("config", 3*time.Second)
	if err != nil {
		t.Fatalf("\"config\" command failed: %v", err)
	}
	if out1 != "Entering configuration mode" {
		t.Errorf("got output %q, want %q", out1, "Entering configuration mode")
	}

	// This is the part that broke live: without re-deriving the prompt
	// after "config" changed it, this call would time out waiting for the
	// old "OLT>" prompt that never reappears.
	out2, _, err := session.SendAndWaitForPrompt("show version", 3*time.Second)
	if err != nil {
		t.Fatalf("\"show version\" command failed (prompt tracking likely broken): %v", err)
	}
	if out2 != "v1.0" {
		t.Errorf("got output %q, want %q", out2, "v1.0")
	}
}

// TestStreamShellSessionAttemptsEnableForKnownVendor confirms the full
// escalation path for a vendor known to use it: unprivileged prompt +
// enable password configured + an allowlisted vendor -> "enable" gets
// sent, the password prompt gets answered, and the resulting privileged
// prompt is what the NEXT command matches against.
func TestStreamShellSessionAttemptsEnableForKnownVendor(t *testing.T) {
	var sent []string
	crlfAnswered := false // only the very first bare-crlf probe gets a reply
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		sent = append(sent, written)
		switch written {
		case crlf:
			if crlfAnswered {
				return
			}
			crlfAnswered = true
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("\r\n\r\nrouter>")
			}()
		case "enable" + crlf:
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("enable\r\nPassword: ")
			}()
		case "s3cret-enable" + crlf:
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("\r\n\r\nrouter#")
			}()
		case "show version" + crlf:
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("show version\r\nv1.0\r\nrouter#")
			}()
		}
	})

	session := NewStreamShellSession(source, func() error { return nil })
	err := session.Open(ConnectionParams{
		// Comfortably longer than commandQuiescenceMs (2s) -- the
		// "Password: " response never contains a matching prompt
		// substring, so detecting it relies on the quiescence fallback,
		// which needs enough headroom under the hard timeout to win that
		// race.
		ConnectTimeoutMs: 4000,
		Vendor:           "cisco",
		EnablePassword:   "s3cret-enable",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	foundEnable := false
	for _, w := range sent {
		if strings.HasPrefix(w, "enable") {
			foundEnable = true
		}
	}
	if !foundEnable {
		t.Fatal("expected \"enable\" to have been sent for an allowlisted vendor")
	}

	out, _, err := session.SendAndWaitForPrompt("show version", 3*time.Second)
	if err != nil {
		t.Fatalf("\"show version\" failed after enable (prompt tracking likely broken): %v", err)
	}
	if out != "v1.0" {
		t.Errorf("got output %q, want %q", out, "v1.0")
	}
}

// TestStreamShellSessionSkipsEnableForUnknownVendor confirms a vendor NOT
// on the allowlist (Huawei -- confirmed live to have no enable concept)
// never gets an "enable" command sent, even with an unprivileged-looking
// prompt and an enable password configured.
func TestStreamShellSessionSkipsEnableForUnknownVendor(t *testing.T) {
	var sent []string
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		sent = append(sent, written)
		if written == crlf {
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("\r\n\r\nOLT>")
			}()
		}
	})

	session := NewStreamShellSession(source, func() error { return nil })
	err := session.Open(ConnectionParams{
		ConnectTimeoutMs: 500,
		Vendor:           "huawei",
		EnablePassword:   "s3cret-enable",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	for _, w := range sent {
		if strings.HasPrefix(w, "enable") {
			t.Fatalf("did not expect \"enable\" to be sent for a non-allowlisted vendor, but got write %q", w)
		}
	}
}

// TestStreamShellSessionSkipsEnableWhenAlreadyPrivileged confirms an
// allowlisted vendor whose prompt DOESN'T end in ">" (i.e. already
// privileged) also skips the attempt.
func TestStreamShellSessionSkipsEnableWhenAlreadyPrivileged(t *testing.T) {
	var sent []string
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		sent = append(sent, written)
		if written == crlf {
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("\r\n\r\nrouter#")
			}()
		}
	})

	session := NewStreamShellSession(source, func() error { return nil })
	err := session.Open(ConnectionParams{
		ConnectTimeoutMs: 500,
		Vendor:           "cisco",
		EnablePassword:   "s3cret-enable",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	for _, w := range sent {
		if strings.HasPrefix(w, "enable") {
			t.Fatalf("did not expect \"enable\" to be sent when already privileged, but got write %q", w)
		}
	}
}

// TestStreamShellSessionAttemptsEnableWithoutPasswordConfigured confirms
// "enable" IS attempted even when no enable password is configured --
// confirmed live against OLT-WRK-LTP-01: this device accepted "enable"
// with no password step at all, so requiring a configured password as a
// precondition would have skipped escalation entirely on exactly the
// device that needed it.
func TestStreamShellSessionAttemptsEnableWithoutPasswordConfigured(t *testing.T) {
	var sent []string
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		sent = append(sent, written)
		switch written {
		case crlf:
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("\r\n\r\nrouter>")
			}()
		case "enable" + crlf:
			// Succeeds immediately -- no password prompt at all.
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("enable\r\nrouter>")
			}()
		}
	})

	session := NewStreamShellSession(source, func() error { return nil })
	err := session.Open(ConnectionParams{
		ConnectTimeoutMs: 500,
		Vendor:           "hsgq",
		// No EnablePassword set.
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	foundEnable := false
	for _, w := range sent {
		if w == "enable"+crlf {
			foundEnable = true
		}
	}
	if !foundEnable {
		t.Fatal("expected \"enable\" to be sent even without a configured enable password")
	}
}

// TestStreamShellSessionEnableFailsWhenPasswordNeededButNotConfigured
// confirms a clear, surfaced error -- not a session silently left stuck
// at an unanswered password prompt, which would make every subsequent
// command fail with a confusing timeout instead -- when the device DOES
// ask for a password after "enable" but none was configured.
func TestStreamShellSessionEnableFailsWhenPasswordNeededButNotConfigured(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		switch written {
		case crlf:
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("\r\n\r\nrouter>")
			}()
		case "enable" + crlf:
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("enable\r\nPassword: ")
			}()
		}
	})

	session := NewStreamShellSession(source, func() error { return nil })
	err := session.Open(ConnectionParams{
		ConnectTimeoutMs: 4000, // headroom over commandQuiescenceMs, see the "known vendor" test above
		Vendor:           "cisco",
		// No EnablePassword set.
	})
	if err == nil {
		t.Fatal("expected an error when the device asks for a password we don't have")
	}
	cliErr, ok := err.(*olterr.OltCliError)
	if !ok || cliErr.ErrType != olterr.TypeAuth {
		t.Fatalf("got error %v, want an auth-typed OltCliError", err)
	}
}

// TestStreamShellSessionEnableNoOpsWhenDeviceRejectsIt confirms that even
// when an attempt IS made (allowlisted vendor, unprivileged prompt,
// password configured), a device that doesn't actually recognize "enable"
// doesn't break the connection -- it's a no-op, and the session proceeds
// normally with the prompt as re-derived.
func TestStreamShellSessionEnableNoOpsWhenDeviceRejectsIt(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		switch {
		case written == crlf:
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("\r\n\r\nrouter>")
			}()
		case strings.Contains(written, "enable"):
			// Rejected outright -- no password prompt, same prompt reappears.
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("enable\r\n% Unknown command\r\nrouter>")
			}()
		case strings.Contains(written, "show version"):
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("show version\r\nv1.0\r\nrouter>")
			}()
		}
	})

	session := NewStreamShellSession(source, func() error { return nil })
	err := session.Open(ConnectionParams{
		ConnectTimeoutMs: 2000,
		Vendor:           "cisco",
		EnablePassword:   "s3cret-enable",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	out, _, err := session.SendAndWaitForPrompt("show version", 3*time.Second)
	if err != nil {
		t.Fatalf("\"show version\" failed after a rejected enable attempt: %v", err)
	}
	if out != "v1.0" {
		t.Errorf("got output %q, want %q", out, "v1.0")
	}
}
