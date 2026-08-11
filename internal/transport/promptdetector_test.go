package transport

import (
	"strings"
	"testing"
	"time"

	"github.com/onu-platform/olt-cli-gateway/internal/olterr"
)

// newScriptedSource builds a DataSource whose Write callback is caller-
// controlled -- tests decide what bytes (if any) arrive on the Chunks
// channel, and when, in response to each write. This is the Go-side
// equivalent of the original TypeScript tests' fake DataSource.
func newScriptedSource(onWrite func(written string, chunks chan<- []byte)) *DataSource {
	chunks := make(chan []byte, 16)
	return &DataSource{
		Chunks: chunks,
		Write: func(data string) error {
			onWrite(data, chunks)
			return nil
		},
	}
}

func TestCapturePromptSuccess(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			chunks <- []byte("\r\n\r\nOLT#")
		}()
	})

	prompt, err := capturePrompt(source, 2000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prompt != "OLT#" {
		t.Fatalf("got prompt %q, want %q", prompt, "OLT#")
	}
}

func TestCapturePromptNoOutputTimesOut(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		// The device never responds at all.
	})

	_, err := capturePrompt(source, 50)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	cliErr, ok := err.(*olterr.OltCliError)
	if !ok || cliErr.ErrType != olterr.TypeProtocol {
		t.Fatalf("got error %v, want a protocol-typed OltCliError", err)
	}
}

func TestWaitForPromptSuccess(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		if !strings.Contains(written, "show version") {
			return
		}
		go func() {
			time.Sleep(10 * time.Millisecond)
			chunks <- []byte("show version\r\noutput here\r\nOLT#")
		}()
	})

	output, nextPrompt, err := waitForPrompt(source, "show version", "OLT#", 2*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "output here" {
		t.Fatalf("got output %q, want %q", output, "output here")
	}
	if nextPrompt != "OLT#" {
		t.Fatalf("got nextPrompt %q, want %q (unchanged on a substring match)", nextPrompt, "OLT#")
	}
}

// TestWaitForPromptDetectsChangedPrompt is the regression test for a
// real-hardware finding: a command that changes the prompt itself (e.g.
// entering config mode, "OLT>" -> "OLT(config)#") never makes the OLD
// prompt reappear, so a pure substring match would time out even though
// the device responded fine. waitForPrompt must fall back to a settled-
// output heuristic and report the new prompt for the next call to use.
func TestWaitForPromptDetectsChangedPrompt(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		if !strings.Contains(written, "config") {
			return
		}
		go func() {
			time.Sleep(10 * time.Millisecond)
			chunks <- []byte("config\r\nEntering configuration mode\r\nOLT(config)#")
		}()
	})

	output, nextPrompt, err := waitForPrompt(source, "config", "OLT>", 3*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "Entering configuration mode" {
		t.Fatalf("got output %q, want %q", output, "Entering configuration mode")
	}
	if nextPrompt != "OLT(config)#" {
		t.Fatalf("got nextPrompt %q, want %q", nextPrompt, "OLT(config)#")
	}
}

// TestWaitForPromptFallbackWaitsForOutputToStart confirms the quiescence
// fallback doesn't fire on a device that simply hasn't started responding
// yet (buffer still empty) -- only on output that arrived and then went
// quiet without matching.
func TestWaitForPromptFallbackWaitsForOutputToStart(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		if !strings.Contains(written, "slow") {
			return
		}
		go func() {
			// Longer than commandQuiescenceMs -- if the fallback
			// incorrectly fired on an empty buffer, this test would see a
			// bogus early success instead of the real output below.
			time.Sleep(commandQuiescenceMs*time.Millisecond + 200*time.Millisecond)
			chunks <- []byte("slow\r\nfinally here\r\nOLT#")
		}()
	})

	output, nextPrompt, err := waitForPrompt(source, "slow", "OLT#", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "finally here" {
		t.Fatalf("got output %q, want %q", output, "finally here")
	}
	if nextPrompt != "OLT#" {
		t.Fatalf("got nextPrompt %q, want %q", nextPrompt, "OLT#")
	}
}

func TestWaitForPromptTimeout(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		// The device never sends the prompt back.
	})

	_, _, err := waitForPrompt(source, "show version", "OLT#", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	cliErr, ok := err.(*olterr.OltCliError)
	if !ok || cliErr.ErrType != olterr.TypeTimeout {
		t.Fatalf("got error %v, want a timeout-typed OltCliError", err)
	}
}

func TestWaitForPromptSessionClosed(t *testing.T) {
	chunks := make(chan []byte)
	source := &DataSource{
		Chunks: chunks,
		Write: func(data string) error {
			close(chunks)
			return nil
		},
	}

	_, _, err := waitForPrompt(source, "show version", "OLT#", 2*time.Second)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	cliErr, ok := err.(*olterr.OltCliError)
	if !ok || cliErr.ErrType != olterr.TypeProtocol {
		t.Fatalf("got error %v, want a protocol-typed OltCliError", err)
	}
}

func TestStripEchoAndPrompt(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		command string
		prompt  string
		want    string
	}{
		{
			name:    "strips leading echo and trailing prompt",
			raw:     "show version\r\noutput line 1\r\noutput line 2\r\nOLT#",
			command: "show version",
			prompt:  "OLT#",
			want:    "output line 1\r\noutput line 2",
		},
		{
			name:    "output containing the prompt substring only strips the last occurrence",
			raw:     "cmd\r\nOLT# appears mid-output\r\nOLT#",
			command: "cmd",
			prompt:  "OLT#",
			want:    "OLT# appears mid-output",
		},
		{
			name:    "no command echo present is a no-op for the strip-prefix step",
			raw:     "just output\r\nOLT#",
			command: "not-present",
			prompt:  "OLT#",
			want:    "just output",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripEchoAndPrompt(tc.raw, tc.command, tc.prompt)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
