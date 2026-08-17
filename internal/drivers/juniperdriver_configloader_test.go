package drivers

import (
	"strings"
	"testing"

	"github.com/onu-platform/gearctl/internal/transport"
)

// Real captured Junos response bodies (the text between a command's own
// echo and the next prompt -- same abstraction level as ciscoiosdriver_test.go's
// fixtures) from live testing against the same Juniper vMX (Junos
// 18.2R1.9) this whole driver was verified against.

const sampleConfigurePrivateResponse = `warning: uncommitted changes will be discarded on exit
Entering configuration mode
Users currently editing the configuration:
  root terminal u0 (pid 11221) on since 2026-08-15 19:45:52 UTC, idle 15:13:14
      [edit]`

// sampleShowCompareResponse is real "show | compare" output captured
// live after "set interfaces ge-0/0/0 unit 0 description
// \"gearctl-cfgtest\"" -- Junos's OWN diff, not a heuristic
// reconstruction. Deliberately includes the real trailing "\n\n[edit]"
// noise Junos reprints right before its own next prompt (see
// stripTrailingEditContext's doc comment) -- this fixture is what
// gearctl's transport layer actually hands back BEFORE that stripping,
// so applyJuniperMergeConfig's use of stripTrailingEditContext is
// genuinely exercised by these tests, not just plausible-looking.
const sampleShowCompareResponse = `[edit interfaces ge-0/0/0 unit 0]
+    description gearctl-cfgtest;

[edit]`

// sampleSyntaxErrorResponse is real (simplified to one occurrence --
// confirmed live that a real device can repeat this line multiple times
// with backspace/redraw noise interleaved, see junosSyntaxErrorPattern's
// own doc comment; detection is substring-based so repetition doesn't
// matter) Junos rejection output for a bad "set" command. Includes the
// real trailing "\n\n[edit]" noise (see sampleShowCompareResponse's own
// doc comment) so detectJunosRejection's stripTrailingEditContext use is
// genuinely exercised.
const sampleSyntaxErrorResponse = `          ^
syntax error.

[edit]`

// sampleCommitCheckSemanticFailure is real "commit check" output
// captured live for a SEMANTIC (not syntax) rejection -- setting a
// second logical unit on an interface whose encapsulation only allows
// unit 0. Confirmed live this does NOT match junosSyntaxErrorPattern
// ("syntax error") at all -- only junosCommitErrorPattern's broader
// "error:" convention catches it. Includes the real trailing
// "\n\n[edit]" noise, same as every other config-mode response.
const sampleCommitCheckSemanticFailure = `[edit interfaces ge-0/0/0]
  'unit 1'
    Only unit 0 is valid for this encapsulation
error: configuration check-out failed

[edit]`

func TestJuniperDriverLoadMergeCommandsDryRunShowsRealDiff(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show configuration": {output: sampleShowConfiguration},
		"configure private":  {output: sampleConfigurePrivateResponse},
		`set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"`: {output: ""},
		"show | compare": {output: sampleShowCompareResponse},
		"rollback 0":     {output: ""},
		"exit":           {output: "Exiting configuration mode"},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	diff, reverse, hash, applied, err := d.LoadMergeCommands(
		[]string{`set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"`}, true, "",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied {
		t.Error("applied = true, want false for a dry run")
	}
	if hash == "" {
		t.Error("configHash = \"\", want a non-empty fingerprint")
	}
	if !strings.Contains(diff, "show | compare") {
		t.Errorf("diff = %q, want the disclaimer mentioning show | compare", diff)
	}
	if !strings.Contains(diff, "+    description gearctl-cfgtest;") {
		t.Errorf("diff = %q, want the real captured show|compare content", diff)
	}
	if strings.Contains(diff, "[edit]") {
		t.Errorf("diff = %q, want the trailing bare \"[edit]\" prompt-context noise stripped", diff)
	}
	if len(reverse) != 1 || reverse[0] != "rollback 1" {
		t.Errorf("reverseCommands = %#v, want [\"rollback 1\"]", reverse)
	}

	// Unlike CiscoIOSDriver's dry run (a fully offline text projection,
	// no device interaction beyond GetConfig()), Junos's dry run MUST
	// enter config mode to get a real `show | compare` diff -- there's
	// no local heuristic equivalent to Cisco's projectMergedConfig here.
	// It must still discard and exit cleanly, and must NEVER attempt a
	// commit during a dry run.
	var sawConfigurePrivate, sawRollback0, sawCommit bool
	for _, cmd := range fake.sent {
		switch cmd {
		case "configure private":
			sawConfigurePrivate = true
		case "rollback 0":
			sawRollback0 = true
		case "commit", "commit check":
			sawCommit = true
		}
	}
	if !sawConfigurePrivate {
		t.Error("expected \"configure private\" to have been sent (Junos's diff requires config mode)")
	}
	if !sawRollback0 {
		t.Error("expected \"rollback 0\" to have discarded the dry-run candidate")
	}
	if sawCommit {
		t.Errorf("sent = %v, want no commit/commit-check during a dry run", fake.sent)
	}
}

func TestJuniperDriverLoadMergeCommandsRejectsLineFailsCleanly(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show configuration": {output: sampleShowConfiguration},
		"configure private":  {output: sampleConfigurePrivateResponse},
		"set foo bar baz":    {output: sampleSyntaxErrorResponse},
		"rollback 0":         {output: ""},
		"exit":               {output: "Exiting configuration mode"},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, _, _, applied, err := d.LoadMergeCommands([]string{"set foo bar baz"}, true, "")
	if err == nil {
		t.Fatal("expected an error for a rejected line")
	}
	if applied {
		t.Error("applied = true, want false after a rejection")
	}
	if !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("err = %v, want it to mention the real Junos rejection text", err)
	}
	var sawRollback0, sawExit bool
	for _, cmd := range fake.sent {
		if cmd == "rollback 0" {
			sawRollback0 = true
		}
		if cmd == "exit" {
			sawExit = true
		}
	}
	if !sawRollback0 || !sawExit {
		t.Errorf("sent = %v, want both \"rollback 0\" and \"exit\" as cleanup after a rejection", fake.sent)
	}
}

func TestJuniperDriverLoadMergeCommandsApplyRequiresExpectedConfigHash(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show configuration": {output: sampleShowConfiguration},
		"configure private":  {output: sampleConfigurePrivateResponse},
		`set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"`: {output: ""},
		"show | compare": {output: sampleShowCompareResponse},
		"rollback 0":     {output: ""},
		"exit":           {output: "Exiting configuration mode"},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, _, _, applied, err := d.LoadMergeCommands(
		[]string{`set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"`}, false, "",
	)
	if err == nil {
		t.Fatal("expected an error when applying without expectedConfigHash")
	}
	if applied {
		t.Error("applied = true, want false")
	}
}

func TestJuniperDriverLoadMergeCommandsApplyRejectsStaleHash(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show configuration": {output: sampleShowConfiguration},
		"configure private":  {output: sampleConfigurePrivateResponse},
		`set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"`: {output: ""},
		"show | compare": {output: sampleShowCompareResponse},
		"rollback 0":     {output: ""},
		"exit":           {output: "Exiting configuration mode"},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, _, _, applied, err := d.LoadMergeCommands(
		[]string{`set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"`}, false, "not-the-real-hash",
	)
	if err == nil {
		t.Fatal("expected an error for a stale hash")
	}
	if applied {
		t.Error("applied = true, want false")
	}
}

func TestJuniperDriverLoadMergeCommandsApplySucceeds(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show configuration": {output: sampleShowConfiguration},
		"configure private":  {output: sampleConfigurePrivateResponse},
		`set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"`: {output: ""},
		"show | compare": {output: sampleShowCompareResponse},
		"commit check":   {output: "configuration check succeeds"},
		"commit":         {output: "commit complete"},
		"exit":           {output: "Exiting configuration mode"},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	hash := configHashOf(sampleShowConfiguration)
	diff, reverse, gotHash, applied, err := d.LoadMergeCommands(
		[]string{`set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"`}, false, hash,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !applied {
		t.Error("applied = false, want true")
	}
	if gotHash != hash {
		t.Errorf("configHash = %q, want %q", gotHash, hash)
	}
	if !strings.Contains(diff, "+    description gearctl-cfgtest;") {
		t.Errorf("diff = %q, want the real show|compare content", diff)
	}
	if len(reverse) != 1 || reverse[0] != "rollback 1" {
		t.Errorf("reverseCommands = %#v, want [\"rollback 1\"]", reverse)
	}
	var sawRollback0 bool
	for _, cmd := range fake.sent {
		if cmd == "rollback 0" {
			sawRollback0 = true
		}
	}
	if sawRollback0 {
		t.Errorf("sent = %v, want no \"rollback 0\" on a successful apply", fake.sent)
	}
}

func TestJuniperDriverLoadMergeCommandsApplyCommitCheckFails(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show configuration": {output: sampleShowConfiguration},
		"configure private":  {output: sampleConfigurePrivateResponse},
		`set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"`: {output: ""},
		"show | compare": {output: sampleShowCompareResponse},
		"commit check":   {output: "error: configuration check-out failed"},
		"rollback 0":     {output: ""},
		"exit":           {output: "Exiting configuration mode"},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	hash := configHashOf(sampleShowConfiguration)
	_, _, _, applied, err := d.LoadMergeCommands(
		[]string{`set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"`}, false, hash,
	)
	if err == nil {
		t.Fatal("expected an error when commit check fails")
	}
	if applied {
		t.Error("applied = true, want false")
	}
	var sawCommit, sawRollback0 bool
	for _, cmd := range fake.sent {
		if cmd == "commit" {
			sawCommit = true
		}
		if cmd == "rollback 0" {
			sawRollback0 = true
		}
	}
	if sawCommit {
		t.Error("\"commit\" must never be sent if \"commit check\" already failed")
	}
	if !sawRollback0 {
		t.Error("expected \"rollback 0\" to have discarded the candidate after commit check failed")
	}
}

func TestJuniperDriverLoadMergeCommandsApplyCommitFails(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show configuration": {output: sampleShowConfiguration},
		"configure private":  {output: sampleConfigurePrivateResponse},
		`set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"`: {output: ""},
		"show | compare": {output: sampleShowCompareResponse},
		"commit check":   {output: "configuration check succeeds"},
		"commit":         {output: "error: commit failed for some reason"},
		"rollback 0":     {output: ""},
		"exit":           {output: "Exiting configuration mode"},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	hash := configHashOf(sampleShowConfiguration)
	_, _, _, applied, err := d.LoadMergeCommands(
		[]string{`set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"`}, false, hash,
	)
	if err == nil {
		t.Fatal("expected an error when commit fails")
	}
	if applied {
		t.Error("applied = true, want false")
	}
}

// TestJuniperDriverLoadConfigDelegatesToLoadMergeCommands confirms
// LoadConfig splits its candidate into lines and applies them exactly
// like LoadMergeCommands would with the same lines -- see LoadConfig's
// own doc comment for why candidate must be Junos's flat "set"/"delete"
// form here, unlike CiscoIOSDriver's curly-brace GetConfig()-shaped
// candidate.
func TestJuniperDriverLoadConfigDelegatesToLoadMergeCommands(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show configuration": {output: sampleShowConfiguration},
		"configure private":  {output: sampleConfigurePrivateResponse},
		`set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"`: {output: ""},
		"show | compare": {output: sampleShowCompareResponse},
		"rollback 0":     {output: ""},
		"exit":           {output: "Exiting configuration mode"},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	candidate := "\n" + `set interfaces ge-0/0/0 unit 0 description "gearctl-cfgtest"` + "\n\n"
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
	if !strings.Contains(diff, "+    description gearctl-cfgtest;") {
		t.Errorf("diff = %q, want the real show|compare content", diff)
	}
}

func TestDetectJunosRejectionMatchesRealSyntaxError(t *testing.T) {
	result := transport.CommandResult{Output: strPtr(sampleSyntaxErrorResponse)}
	reason := detectJunosRejection(result)
	if reason == "" {
		t.Error("expected a non-empty rejection reason for real captured syntax-error output")
	}
	if strings.Contains(reason, "[edit]") {
		t.Errorf("reason = %q, want the trailing bare \"[edit]\" prompt-context noise stripped", reason)
	}
}

// TestDetectJunosRejectionMatchesRealCommitCheckSemanticFailure is a
// regression test: confirmed live that a semantic (not syntax) Junos
// rejection -- "error: configuration check-out failed" -- does NOT match
// junosSyntaxErrorPattern at all, and previously slipped through
// detectJunosRejection undetected during the per-line apply loop (only
// this driver's own automatic "commit check" step downstream happened to
// catch it, via a separate "check succeeds" substring check). Also
// confirms the trailing "[edit]" noise is stripped from the reported
// reason, and that the full multi-line explanation ("Only unit 0 is
// valid for this encapsulation") is preserved, not just the last line.
func TestDetectJunosRejectionMatchesRealCommitCheckSemanticFailure(t *testing.T) {
	result := transport.CommandResult{Output: strPtr(sampleCommitCheckSemanticFailure)}
	reason := detectJunosRejection(result)
	if reason == "" {
		t.Fatal("expected a non-empty rejection reason for a real semantic commit-check failure")
	}
	if strings.Contains(reason, "[edit]") {
		t.Errorf("reason = %q, want the trailing bare \"[edit]\" prompt-context noise stripped", reason)
	}
	if !strings.Contains(reason, "Only unit 0 is valid for this encapsulation") {
		t.Errorf("reason = %q, want the real explanatory line preserved, not just the last line", reason)
	}
}

func TestDetectJunosRejectionAcceptsCleanOutput(t *testing.T) {
	result := transport.CommandResult{Output: strPtr("")}
	if reason := detectJunosRejection(result); reason != "" {
		t.Errorf("reason = %q, want \"\" for a clean accepted command", reason)
	}
}

func TestDetectJunosRejectionFlagsTruncatedOutput(t *testing.T) {
	result := transport.CommandResult{Output: strPtr("partial..."), Truncated: true}
	if reason := detectJunosRejection(result); reason == "" {
		t.Error("expected Truncated to be treated as a rejection")
	}
}

// TestJuniperDriverLoadMergeCommandsRecoversFromWhitespaceOnlyCommitCheck
// is a regression test: confirmed live that "commit check" (real
// device-side validation work, unlike a plain config-mode command) can
// take noticeably longer to produce its real confirmation text, and the
// ordinary quiescence-fallback wait was found to conclude "done" on a
// whitespace-only settled buffer before that text arrived -- see
// isNonWhitespace's own doc comment. Uses completionAwareStubSession
// (defined in juniperdriver_test.go) to model exactly that: "commit
// check"'s first attempt is whitespace-only, the second (once
// isNonWhitespace rejects the first) is the real confirmation text.
func TestJuniperDriverLoadMergeCommandsRecoversFromWhitespaceOnlyCommitCheck(t *testing.T) {
	stub := &completionAwareStubSession{
		firstAttempt:  fakeResponse{output: " "},
		secondAttempt: fakeResponse{output: "configuration check succeeds"},
	}
	d := &JuniperDriver{BaseDriver{session: stub, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	result, err := d.RunCommandUntilComplete("commit check", isNonWhitespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outputOrEmpty(result) != "configuration check succeeds" {
		t.Errorf("output = %q, want the eventually-complete confirmation text", outputOrEmpty(result))
	}
	if stub.receivedIsComplete == nil {
		t.Fatal("expected a non-nil isComplete to have been passed through")
	}
	if stub.receivedIsComplete(" ") {
		t.Error("isNonWhitespace(\" \") should be false -- whitespace-only is not complete")
	}
	if !stub.receivedIsComplete("configuration check succeeds") {
		t.Error("isNonWhitespace(real confirmation text) should be true")
	}
}

func TestIsNonWhitespace(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{" ", false},
		{"\r\n\r\n", false},
		{"configuration check succeeds", true},
		{"  x  ", true},
	}
	for _, tc := range cases {
		if got := isNonWhitespace(tc.in); got != tc.want {
			t.Errorf("isNonWhitespace(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestStripTrailingEditContext(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "real captured show|compare output with trailing bare [edit]",
			in:   "[edit interfaces ge-0/0/0 unit 0]\n+    description gearctl-cfgtest;\n\n[edit]",
			want: "[edit interfaces ge-0/0/0 unit 0]\n+    description gearctl-cfgtest;",
		},
		{
			name: "populated [edit <path>] header is content, never stripped even at the very end",
			in:   "some line\n[edit interfaces ge-0/0/0 unit 0]",
			want: "some line\n[edit interfaces ge-0/0/0 unit 0]",
		},
		{
			name: "no trailing [edit] at all -- no-op",
			in:   "plain output, nothing to strip",
			want: "plain output, nothing to strip",
		},
		{
			name: "only the bare [edit] line, nothing else",
			in:   "[edit]",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripTrailingEditContext(tc.in); got != tc.want {
				t.Errorf("stripTrailingEditContext(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestJuniperDriverLoadMergeCommandsRefusesReservedCommandLines is a
// regression test for a real, live-reproduced safety hole: a user
// including "configure private"/"commit check"/"commit" as their OWN
// -merge-cmd lines (an old habit from before this driver auto-managed
// those steps) had "commit" replayed for real during the DRY-RUN pass --
// applyJuniperMergeConfig replays every delta line regardless of
// dryRun -- silently committing a real change before the caller ever
// confirmed "apply". Each of these must now be refused outright, before
// ever calling GetConfig or sending anything to the device.
func TestJuniperDriverLoadMergeCommandsRefusesReservedCommandLines(t *testing.T) {
	reserved := []string{
		"configure",
		"configure private",
		"configure exclusive",
		"configure batch",
		"commit",
		"commit check",
		"commit and-quit",
		"commit confirmed 5",
	}
	for _, line := range reserved {
		t.Run(line, func(t *testing.T) {
			fake := &fakeShellSession{responses: map[string]fakeResponse{
				"show configuration": {output: sampleShowConfiguration},
			}}
			d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

			_, _, _, applied, err := d.LoadMergeCommands([]string{line}, true, "")
			if err == nil {
				t.Fatalf("expected %q to be refused", line)
			}
			if applied {
				t.Error("applied = true, want false")
			}
			if len(fake.sent) != 0 {
				t.Errorf("sent = %v, want NOTHING sent to the device -- must be refused before ever connecting", fake.sent)
			}
		})
	}
}

// TestJuniperDriverLoadMergeCommandsAllowsRollback confirms
// junosReservedCommandLinePattern does NOT match "rollback N" --
// reverseCommands' own documented, legitimate use (see
// LoadMergeCommands' doc comment) depends on this being ordinary,
// unrestricted delta content.
func TestJuniperDriverLoadMergeCommandsAllowsRollback(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show configuration": {output: sampleShowConfiguration},
		"configure private":  {output: sampleConfigurePrivateResponse},
		"rollback 1":         {output: "load complete"},
		"show | compare":     {output: sampleShowCompareResponse},
		"rollback 0":         {output: ""},
		"exit":               {output: "Exiting configuration mode"},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, _, _, _, err := d.LoadMergeCommands([]string{"rollback 1"}, true, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func strPtr(s string) *string { return &s }
