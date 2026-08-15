package transport

import (
	"strings"
	"testing"
	"time"

	"github.com/onu-platform/gearctl/internal/olterr"
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

	prompt, err := capturePrompt(source, "", 2000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prompt != "OLT#" {
		t.Fatalf("got prompt %q, want %q", prompt, "OLT#")
	}
}

// TestCapturePromptUsesCarry confirms carry seeds the buffer -- a prior
// call's leftover bytes must be treated as already-arrived data, not
// silently dropped.
func TestCapturePromptUsesCarry(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		// The device sends nothing more -- everything needed is already
		// in the carry.
	})

	prompt, err := capturePrompt(source, "\r\n\r\nOLT#", 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prompt != "OLT#" {
		t.Fatalf("got prompt %q, want %q (from carry alone)", prompt, "OLT#")
	}
}

// TestCapturePromptIgnoresTrailingAsyncLogMessage is the regression test
// for a real-hardware finding: a device pushed an unsolicited, bracketed-
// timestamp system warning to the console AFTER the real prompt already
// appeared, landing in the same quiescence window. A naive "last non-
// blank line is the prompt" heuristic captured the warning instead of the
// real "KRITIPUR-OLT>" prompt, silently breaking everything downstream
// that depends on it (enable-escalation's ">"-suffix check, command
// completion matching).
func TestCapturePromptIgnoresTrailingAsyncLogMessage(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			chunks <- []byte("\r\n\r\nKRITIPUR-OLT>\r\n[1970/01/31 13:23:36]  [system]Warning: Login locally, please check the AAA settings")
		}()
	})

	prompt, err := capturePrompt(source, "", 2000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prompt != "KRITIPUR-OLT>" {
		t.Fatalf("got prompt %q, want %q (the async warning line should be ignored)", prompt, "KRITIPUR-OLT>")
	}
}

func TestLastPromptLikeLine(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "plain trailing prompt, no noise",
			text: "\r\n\r\nOLT#",
			want: "OLT#",
		},
		{
			name: "real prompt followed by an async bracketed-timestamp log line",
			text: "KRITIPUR-OLT>\r\n[1970/01/31 13:23:36]  [system]Warning: Login locally, please check the AAA settings",
			want: "KRITIPUR-OLT>",
		},
		{
			name: "multiple trailing log lines after the real prompt",
			text: "OLT#\r\n[log] one\r\n[log] two",
			want: "OLT#",
		},
		{
			name: "everything looks like noise -- falls back to the literal last line",
			text: "[log] one\r\n[log] two",
			want: "[log] two",
		},
		{
			name: "empty input",
			text: "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastPromptLikeLine(tc.text); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCapturePromptNoOutputTimesOut(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		// The device never responds at all.
	})

	_, err := capturePrompt(source, "", 50)
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

	output, nextPrompt, carry, err := waitForPrompt(source, "show version", "OLT#", "", 2*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "output here" {
		t.Fatalf("got output %q, want %q", output, "output here")
	}
	if nextPrompt != "OLT#" {
		t.Fatalf("got nextPrompt %q, want %q (unchanged on a substring match)", nextPrompt, "OLT#")
	}
	if carry != "" {
		t.Errorf("got carry %q, want \"\" (nothing trailed the match)", carry)
	}
}

// TestWaitForPromptContinuesThroughPagination is the regression test for
// a real-hardware finding: a long "show" command's output paused at a
// "--More--"-style pagination prompt, which the quiescence fallback
// (added for mode-changing commands) mistook for command completion,
// silently truncating the result at the first page. waitForPrompt must
// recognize the pagination marker, send a continuation keystroke, and
// keep accumulating -- returning output from every page, with the marker
// itself stripped out as navigation noise rather than left embedded in
// the result.
func TestWaitForPromptContinuesThroughPagination(t *testing.T) {
	pageTwoSent := false
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		switch written {
		case "show ont-info all" + crlf:
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("show ont-info all\r\nrow1\r\nrow2\r\n--More--")
			}()
		case paginationContinueKeystroke:
			if pageTwoSent {
				return
			}
			pageTwoSent = true
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("\r\nrow3\r\nrow4\r\nOLT#")
			}()
		}
	})

	output, nextPrompt, _, err := waitForPrompt(source, "show ont-info all", "OLT#", "", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(strings.ToLower(output), "more") {
		t.Errorf("expected the pagination marker to be stripped, got %q", output)
	}
	for _, want := range []string{"row1", "row2", "row3", "row4"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected output to contain %q (from both pages), got %q", want, output)
		}
	}
	if nextPrompt != "OLT#" {
		t.Errorf("got nextPrompt %q, want %q", nextPrompt, "OLT#")
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

	output, nextPrompt, carry, err := waitForPrompt(source, "config", "OLT>", "", 3*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "Entering configuration mode" {
		t.Fatalf("got output %q, want %q", output, "Entering configuration mode")
	}
	if nextPrompt != "OLT(config)#" {
		t.Fatalf("got nextPrompt %q, want %q", nextPrompt, "OLT(config)#")
	}
	if carry != "" {
		t.Errorf("got carry %q, want \"\" (quiescence fallback has nothing left to carry)", carry)
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

	output, nextPrompt, _, err := waitForPrompt(source, "slow", "OLT#", "", 5*time.Second)
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

// TestWaitForPromptCarriesResidualBytesToNextCall is the regression test
// for a real-hardware finding against a live Cisco ISR: a command with
// essentially no real output ("terminal length 0") was followed by a
// stray duplicate prompt redraw landing in the SAME read as its own
// completion. This confirms the fix directly: cmd1's response bundles
// both prompt occurrences in one chunk, cmd1 must still report only its
// own real output plus a non-empty carry (the leftover second prompt),
// and feeding that carry into cmd2's call must NOT corrupt cmd2's own
// result -- proving the residue is absorbed as harmless pre-echo noise,
// not misread as cmd2's completion.
func TestWaitForPromptCarriesResidualBytesToNextCall(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		switch {
		case strings.Contains(written, "cmd1"):
			go func() {
				time.Sleep(10 * time.Millisecond)
				// cmd1's own completion, PLUS a stray duplicate prompt
				// redraw, both arriving in the very same read.
				chunks <- []byte("cmd1\r\noutput1\r\nOLT#\r\nOLT#")
			}()
		case strings.Contains(written, "cmd2"):
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- []byte("cmd2\r\noutput2\r\nOLT#")
			}()
		}
	})

	output1, prompt1, carry1, err := waitForPrompt(source, "cmd1", "OLT#", "", 2*time.Second)
	if err != nil {
		t.Fatalf("cmd1: unexpected error: %v", err)
	}
	if output1 != "output1" {
		t.Fatalf("cmd1: got output %q, want %q", output1, "output1")
	}
	if carry1 == "" {
		t.Fatal("cmd1: expected a non-empty carry (the stray duplicate prompt redraw)")
	}

	output2, _, _, err := waitForPrompt(source, "cmd2", prompt1, carry1, 2*time.Second)
	if err != nil {
		t.Fatalf("cmd2: unexpected error: %v", err)
	}
	if output2 != "output2" {
		t.Fatalf(
			"cmd2: got output %q, want %q -- carried-over residue from cmd1 must be ignored as pre-echo "+
				"noise, not misread as cmd2's own completion",
			output2, "output2",
		)
	}
}

func TestWaitForPromptTimeout(t *testing.T) {
	source := newScriptedSource(func(written string, chunks chan<- []byte) {
		// The device never sends the prompt back.
	})

	_, _, _, err := waitForPrompt(source, "show version", "OLT#", "", 50*time.Millisecond)
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

	_, _, _, err := waitForPrompt(source, "show version", "OLT#", "", 2*time.Second)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	cliErr, ok := err.(*olterr.OltCliError)
	if !ok || cliErr.ErrType != olterr.TypeProtocol {
		t.Fatalf("got error %v, want a protocol-typed OltCliError", err)
	}
}

func TestFindEchoEnd(t *testing.T) {
	cases := []struct {
		name       string
		buffer     string
		command    string
		searchFrom int
		want       int
	}{
		{
			name:       "found at the very start of the search region",
			buffer:     "show version\r\noutput\r\nOLT#",
			command:    "show version",
			searchFrom: 0,
			want:       len("show version"),
		},
		{
			name:       "never searches before searchFrom -- a match only in carry is ignored",
			buffer:     "show version\r\nshow version\r\noutput\r\nOLT#",
			command:    "show version",
			searchFrom: len("show version\r\n"),
			want:       len("show version\r\n") + len("show version"),
		},
		{
			name:       "not found at all",
			buffer:     "just output\r\nOLT#",
			command:    "not-present",
			searchFrom: 0,
			want:       -1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := findEchoEnd(tc.buffer, tc.command, tc.searchFrom); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCleanOutput(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "strips one leading and one trailing line ending",
			text: "\r\noutput line 1\r\noutput line 2\r\n",
			want: "output line 1\r\noutput line 2",
		},
		{
			name: "strips an embedded pagination marker line",
			text: "\r\nrow1\r\n--More--\r\nrow2\r\n",
			want: "row1\r\nrow2",
		},
		{
			name: "no leading/trailing artifacts is a no-op",
			text: "just output",
			want: "just output",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanOutput(tc.text); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStripTrailingPromptOccurrence(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		prompt string
		want   string
	}{
		{
			name:   "strips a single trailing occurrence",
			text:   "\r\noutput line 1\r\noutput line 2\r\n",
			prompt: "OLT#",
			// No occurrence present at all -- untouched.
			want: "\r\noutput line 1\r\noutput line 2\r\n",
		},
		{
			name:   "strips the trailing prompt",
			text:   "\r\noutput here\r\nOLT#",
			prompt: "OLT#",
			want:   "\r\noutput here\r\n",
		},
		// TestStripTrailingPromptOccurrenceStripsRepeats (this case) is
		// the regression test for a real-hardware finding: "show
		// running-config" against a live Cisco ISR intermittently
		// (~1-in-5 runs, not consistent) redraws its own prompt a second
		// time immediately after the first, both landing in the same
		// settled quiescence-fallback burst. A single strip left the
		// inner, first redraw embedded in the returned text as if it were
		// real trailing config content (observed live as a stray "ISR#"
		// baked onto the end of an otherwise-correct config).
		{
			// Trailing "\r\n\r\n" left after the repeat-collapsing loop is
			// intentional here -- this function's only job is removing
			// repeated prompt occurrences; general whitespace cleanup is
			// cleanOutput's responsibility, applied by the caller
			// (waitForPrompt always wraps this call in cleanOutput).
			name:   "repeated trailing prompt redraw is fully stripped, not just the outer occurrence",
			text:   "\r\nline1\r\nline2\r\nend\r\n\r\nOLT#\r\n\r\nOLT#",
			prompt: "OLT#",
			want:   "\r\nline1\r\nline2\r\nend\r\n\r\n",
		},
		{
			name:   "repeated trailing prompt redraw with no separating whitespace at all",
			text:   "contentOLT#OLT#",
			prompt: "OLT#",
			want:   "content",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripTrailingPromptOccurrence(tc.text, tc.prompt); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPaginationMarkerPattern(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"--More--", true},
		{"-- More --", true},
		{"---- More ----", true},
		{"(more)", true},
		{"( More )", true},
		{"Press any key to continue", true},
		{"press any key", true},
		{"row of real data with more columns than usual", false},
		{"3 more ONTs configured", false},
		{"OLT#", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			if got := paginationMarkerPattern.MatchString(tc.line); got != tc.want {
				t.Errorf("paginationMarkerPattern.MatchString(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}
