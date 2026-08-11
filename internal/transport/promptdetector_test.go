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

	output, err := waitForPrompt(source, "show version", "OLT#", 2*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "output here" {
		t.Fatalf("got output %q, want %q", output, "output here")
	}
}

func TestWaitForPromptTimeout(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		// The device never sends the prompt back.
	})

	_, err := waitForPrompt(source, "show version", "OLT#", 50*time.Millisecond)
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

	_, err := waitForPrompt(source, "show version", "OLT#", 2*time.Second)
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
