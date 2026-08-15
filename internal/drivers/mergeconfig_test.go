package drivers

import (
	"reflect"
	"strings"
	"testing"

	"github.com/onu-platform/gearctl/internal/olterr"
	"github.com/onu-platform/gearctl/internal/transport"
)

func TestParseCandidateLinesSkipsBlankBangAndTrailingEnd(t *testing.T) {
	candidate := "hostname R1\n!\ninterface GigabitEthernet1\n description test\n!\n\nend\n"
	got := parseCandidateLines(candidate)
	want := []string{"hostname R1", "interface GigabitEthernet1", "description test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseCandidateLines = %#v, want %#v", got, want)
	}
}

func TestFindMultiLineDirectiveDetectsBannerAndCryptoBlocks(t *testing.T) {
	cases := []struct {
		name      string
		candidate string
	}{
		{"banner motd", "hostname R1\nbanner motd ^C Welcome ^C\n"},
		{"banner login", "banner login ^C Authorized use only ^C\n"},
		{"crypto pki certificate chain", "crypto pki certificate chain TP-self-signed\n certificate self-signed 01\n  quit\n"},
		{"crypto key generate", "crypto key generate rsa modulus 2048\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := findMultiLineDirective(c.candidate); got == "" {
				t.Errorf("findMultiLineDirective(%q) = \"\", want a non-empty match", c.candidate)
			}
		})
	}
}

func TestFindMultiLineDirectiveAllowsOrdinaryConfig(t *testing.T) {
	candidate := "hostname R1\ninterface GigabitEthernet1\n description ordinary change\n no shutdown\n!\nend\n"
	if got := findMultiLineDirective(candidate); got != "" {
		t.Errorf("findMultiLineDirective(%q) = %q, want \"\" (no multi-line directive present)", candidate, got)
	}
}

func TestConfigHashOfIsDeterministicAndSensitiveToChanges(t *testing.T) {
	a := configHashOf("hostname R1\n")
	b := configHashOf("hostname R1\n")
	c := configHashOf("hostname R2\n")
	if a != b {
		t.Errorf("configHashOf is not deterministic: %q != %q for identical input", a, b)
	}
	if a == c {
		t.Errorf("configHashOf did not change for different input: both %q", a)
	}
}

func TestComputeUnifiedDiffShowsAddedAndRemovedLines(t *testing.T) {
	current := "hostname R1\ndescription old\n"
	candidate := "hostname R1\ndescription new\n"
	diff, err := computeUnifiedDiff(current, candidate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(diff, "-description old") || !strings.Contains(diff, "+description new") {
		t.Errorf("diff = %q, want it to show the line changing", diff)
	}
}

func TestDetectRejectionMatchesRealCiscoErrorStrings(t *testing.T) {
	// Real Cisco IOS/IOS-XE rejection message formats (from public Cisco
	// documentation, since these can't be captured live from this
	// session) -- table-driven so new rejection formats can be added
	// without restructuring the test.
	cases := []string{
		"% Invalid input detected at '^' marker.",
		"% Ambiguous command:  \"sh run\"",
		"% Incomplete command.",
		"% Unrecognized command",
		"Command authorization failed.",
		"Command rejected: interface GigabitEthernet1",
	}
	for _, output := range cases {
		t.Run(output, func(t *testing.T) {
			result := transport.CommandResult{Command: "bad command", Output: &output}
			if got := detectRejection(result); got == "" {
				t.Errorf("detectRejection(%q) = \"\", want a non-empty rejection reason", output)
			}
		})
	}
}

func TestDetectRejectionDoesNotFalsePositiveOnSyslogOrOrdinaryOutput(t *testing.T) {
	// Cisco's informational syslog-style messages have no space after
	// "%" -- must NOT be mistaken for a rejection (this is exactly the
	// distinction ciscoRejectionPrefixPattern's "%\s" requirement exists
	// for). Also confirms ordinary successful output isn't flagged.
	cases := []string{
		"%SYS-5-CONFIG_I: Configured from console by vty0 (10.0.0.5)",
		"%LINK-3-UPDOWN: Interface GigabitEthernet1, changed state to up",
		"R1(config)#",
		"interface GigabitEthernet1 configured",
	}
	for _, output := range cases {
		t.Run(output, func(t *testing.T) {
			result := transport.CommandResult{Command: "ok command", Output: &output}
			if got := detectRejection(result); got != "" {
				t.Errorf("detectRejection(%q) = %q, want \"\" (false positive)", output, got)
			}
		})
	}
}

func TestDetectRejectionFlagsTruncatedOutput(t *testing.T) {
	output := "interface GigabitEthernet1"
	result := transport.CommandResult{Command: "cmd", Output: &output, Truncated: true}
	if got := detectRejection(result); got == "" {
		t.Error("detectRejection on truncated output = \"\", want a non-empty reason")
	}
}

// --- applyMergeConfig ---

func TestApplyMergeConfigHappyPath(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	lines := []string{"interface GigabitEthernet1", "description test"}
	applied, rejection, err := d.applyMergeConfig(lines)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rejection != "" {
		t.Errorf("rejection = %q, want \"\"", rejection)
	}
	if applied != len(lines) {
		t.Errorf("applied = %d, want %d", applied, len(lines))
	}
	want := []string{"configure terminal", "interface GigabitEthernet1", "description test", "end"}
	if !reflect.DeepEqual(fake.sent, want) {
		t.Errorf("sent = %v, want %v", fake.sent, want)
	}
}

// TestPredictNextPrompt covers every mode-transition applyMergeConfig
// actually sends -- see predictNextPrompt's doc comment for why "exit"
// and plain child config lines are deliberately NOT among them (not
// commands applyMergeConfig ever sends to leave a mode, and not commands
// that change the prompt at all, respectively).
func TestPredictNextPrompt(t *testing.T) {
	cases := []struct {
		name          string
		currentPrompt string
		cmd           string
		want          string
	}{
		{"configure terminal from exec prompt", "ISR#", "configure terminal", "ISR(config)#"},
		{"configure terminal is case-insensitive", "ISR#", "CONFIGURE TERMINAL", "ISR(config)#"},
		{"interface opener from config prompt", "ISR(config)#", "interface GigabitEthernet1", "ISR(config-if)#"},
		{"router opener from config prompt", "ISR(config)#", "router bgp 65000", "ISR(config-router)#"},
		{"line opener from config prompt", "ISR(config)#", "line vty 0 4", "ISR(config-line)#"},
		{"end returns to the exec prompt", "ISR(config-if)#", "end", "ISR#"},
		{"plain child line has no prediction", "ISR(config-if)#", "description test", ""},
		{"no-prefixed removal has no prediction", "ISR(config-if)#", "no shutdown", ""},
		{"exit has no prediction (applyMergeConfig never sends it)", "ISR(config-if)#", "exit", ""},
		{"empty current prompt yields no prediction", "", "configure terminal", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := predictNextPrompt(c.currentPrompt, c.cmd); got != c.want {
				t.Errorf("predictNextPrompt(%q, %q) = %q, want %q", c.currentPrompt, c.cmd, got, c.want)
			}
		})
	}
}

// TestApplyMergeConfigPassesPredictedPromptHints is the end-to-end
// regression test for the fix motivated by the NAPALM comparison report:
// without a hint, EVERY mode-transition command below would force
// gearctl's full commandQuiescenceMs (2s) fallback wait, since the
// session's own last-known prompt is guaranteed stale the instant such a
// command is sent (see predictNextPrompt's and
// SendAndWaitForPromptHint's doc comments). Asserts the ACTUAL hint
// runSingle computed and passed down for each command in order --  not
// just that the commands themselves were sent correctly (already covered
// by TestApplyMergeConfigHappyPath).
func TestApplyMergeConfigPassesPredictedPromptHints(t *testing.T) {
	fake := &fakeShellSession{
		responses:     map[string]fakeResponse{},
		currentPrompt: "ISR#",
	}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	lines := []string{"interface GigabitEthernet1", "description test"}
	if _, _, err := d.applyMergeConfig(lines); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantSent := []string{"configure terminal", "interface GigabitEthernet1", "description test", "end"}
	wantHints := []string{"ISR(config)#", "ISR(config-if)#", "", "ISR#"}
	if !reflect.DeepEqual(fake.sent, wantSent) {
		t.Fatalf("sent = %v, want %v", fake.sent, wantSent)
	}
	if !reflect.DeepEqual(fake.hints, wantHints) {
		t.Errorf("hints = %v, want %v (currentPrompt never advances in this fake, so every hint is predicted from the same fixed \"ISR#\" -- a real session's CurrentPrompt() would actually advance between calls, this just confirms runSingle computes and passes SOMETHING non-empty for every mode-transition command, and nothing for the plain child line)", fake.hints, wantHints)
	}
}

func TestApplyMergeConfigStopsOnRejection(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"bad line": {output: "% Invalid input detected at '^' marker."},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	lines := []string{"interface GigabitEthernet1", "bad line", "description never reached"}
	applied, rejection, err := d.applyMergeConfig(lines)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rejection == "" {
		t.Fatal("expected a non-empty rejection reason")
	}
	if applied != 1 {
		t.Errorf("applied = %d, want 1 (only the line before the rejection)", applied)
	}
	want := []string{"configure terminal", "interface GigabitEthernet1", "bad line", "end"}
	if !reflect.DeepEqual(fake.sent, want) {
		t.Errorf("sent = %v, want %v (third candidate line must never be sent, but \"end\" must still fire)", fake.sent, want)
	}
}

func TestApplyMergeConfigStopsOnConnectionDrop(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"dies here": {err: olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt")},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	lines := []string{"interface GigabitEthernet1", "dies here", "description never reached"}
	applied, rejection, err := d.applyMergeConfig(lines)
	if err == nil {
		t.Fatal("expected a non-nil error for a transport-level failure")
	}
	if rejection != "" {
		t.Errorf("rejection = %q, want \"\" (this is a connection failure, not a semantic rejection)", rejection)
	}
	if applied != 1 {
		t.Errorf("applied = %d, want 1", applied)
	}
	want := []string{"configure terminal", "interface GigabitEthernet1", "dies here"}
	if !reflect.DeepEqual(fake.sent, want) {
		t.Errorf("sent = %v, want %v (\"end\" must NOT be attempted after a connection drop)", fake.sent, want)
	}
}

// --- LoadConfig ---

const sampleCurrentConfig = "hostname R1\ninterface GigabitEthernet1\n description old\n!\nend\n"

func TestLoadConfigDryRunReturnsDiffAndHashWithoutWriting(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleCurrentConfig},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	candidate := "hostname R1\ninterface GigabitEthernet1\n description new\n!\nend\n"
	diff, hash, applied, err := d.LoadConfig(candidate, true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied {
		t.Error("applied = true, want false for a dry run")
	}
	if hash == "" {
		t.Error("configHash = \"\", want a non-empty fingerprint")
	}
	if !strings.Contains(diff, "merge/overlay") {
		t.Errorf("diff = %q, want it to contain the merge-not-replace disclaimer", diff)
	}
	if !strings.Contains(diff, "-  description old") && !strings.Contains(diff, "- description old") {
		t.Errorf("diff = %q, want it to show \"description old\" removed", diff)
	}
	if !strings.Contains(diff, "+  description new") && !strings.Contains(diff, "+ description new") {
		t.Errorf("diff = %q, want it to show \"description new\" added", diff)
	}
	for _, cmd := range fake.sent {
		if cmd == "configure terminal" || cmd == "end" {
			t.Errorf("sent = %v, want no config-mode commands during a dry run", fake.sent)
		}
	}
}

func TestLoadConfigRefusesMultiLineDirectiveBeforeConnecting(t *testing.T) {
	fake := &fakeShellSession{}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	candidate := "hostname R1\nbanner motd ^C Welcome ^C\n"
	if _, _, _, err := d.LoadConfig(candidate, true, ""); err == nil {
		t.Fatal("expected an error for a candidate containing a multi-line directive")
	}
	if len(fake.sent) != 0 {
		t.Errorf("sent = %v, want nothing sent -- refusal must happen before any command is sent", fake.sent)
	}
}

func TestLoadConfigApplyRequiresExpectedConfigHash(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleCurrentConfig},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, _, applied, err := d.LoadConfig(sampleCurrentConfig, false, "")
	if err == nil {
		t.Fatal("expected an error when applying with no expectedConfigHash")
	}
	if applied {
		t.Error("applied = true, want false")
	}
}

func TestLoadConfigApplyRejectsStaleHash(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleCurrentConfig},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, _, applied, err := d.LoadConfig(sampleCurrentConfig, false, "stale-hash-from-an-earlier-dry-run")
	if err == nil {
		t.Fatal("expected an error when expectedConfigHash doesn't match the current config")
	}
	if !strings.Contains(err.Error(), "changed") {
		t.Errorf("err = %v, want it to mention the device config changed", err)
	}
	if applied {
		t.Error("applied = true, want false")
	}
	for _, cmd := range fake.sent {
		if cmd == "configure terminal" {
			t.Error("sent includes \"configure terminal\" -- a stale hash must never reach the apply step")
		}
	}
}

func TestLoadConfigApplySucceedsWithFreshHash(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleCurrentConfig},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	candidate := "hostname R1\ninterface GigabitEthernet1\n description new\n!\nend\n"
	_, hash, _, err := d.LoadConfig(candidate, true, "")
	if err != nil {
		t.Fatalf("unexpected error on dry run: %v", err)
	}

	_, _, applied, err := d.LoadConfig(candidate, false, hash)
	if err != nil {
		t.Fatalf("unexpected error on apply: %v", err)
	}
	if !applied {
		t.Error("applied = false, want true")
	}
	if fake.sent[len(fake.sent)-1] != "end" {
		t.Errorf("sent = %v, want it to end with \"end\"", fake.sent)
	}
}

func TestLoadConfigApplyReportsRejection(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleCurrentConfig},
		"bad line":            {output: "% Invalid input detected at '^' marker."},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	candidate := "bad line\n"
	_, hash, _, err := d.LoadConfig(candidate, true, "")
	if err != nil {
		t.Fatalf("unexpected error on dry run: %v", err)
	}

	_, _, applied, err := d.LoadConfig(candidate, false, hash)
	if err == nil {
		t.Fatal("expected an error when the device rejects a candidate line")
	}
	if applied {
		t.Error("applied = true, want false")
	}
}

func TestLoadConfigApplyReportsConnectionDrop(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleCurrentConfig},
		"dies here":           {err: olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt")},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	candidate := "dies here\n"
	_, hash, _, err := d.LoadConfig(candidate, true, "")
	if err != nil {
		t.Fatalf("unexpected error on dry run: %v", err)
	}

	_, _, applied, err := d.LoadConfig(candidate, false, hash)
	if err == nil {
		t.Fatal("expected an error when the connection drops mid-apply")
	}
	if applied {
		t.Error("applied = true, want false")
	}
}
