package transport

import (
	"strings"
	"testing"
	"time"
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
