package drivers

import (
	"reflect"
	"strings"
	"testing"

	"github.com/onu-platform/gearctl/internal/olterr"
	"github.com/onu-platform/gearctl/internal/transport"
)

// sampleHuaweiCurrentConfig mirrors the REAL shape confirmed live against
// a Huawei S6700 (see huaweiconfigloader.go's package doc comment): a
// leading "!"-prefixed comment, "#" bare section separators, indented
// stanza bodies, and a trailing "return" sentinel. Values here are
// synthetic, not the real device's own config.
const sampleHuaweiCurrentConfig = `!Software Version V200R005C00SPC500
#
sysname CoreSwitch01
#
interface GigabitEthernet0/0/1
 description old-desc
 port link-type trunk
#
interface GigabitEthernet0/0/2
 undo port trunk allow-pass vlan 1
#
vlan batch 2 to 10
vlan batch 20 to 30
#
return
`

func TestParseHuaweiCandidateLinesSkipsCommentsHashesAndTrailingReturn(t *testing.T) {
	got := parseHuaweiCandidateLines(sampleHuaweiCurrentConfig)
	want := []string{
		"sysname CoreSwitch01",
		"interface GigabitEthernet0/0/1",
		"description old-desc",
		"port link-type trunk",
		"interface GigabitEthernet0/0/2",
		"undo port trunk allow-pass vlan 1",
		"vlan batch 2 to 10",
		"vlan batch 20 to 30",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseHuaweiCandidateLines = %#v, want %#v", got, want)
	}
}

func TestFindHuaweiMultiLineDirectiveDetectsHeaderBlocks(t *testing.T) {
	cases := []string{
		"sysname R1\nheader login information \"Welcome\"\n",
		"header shell information \"Authorized use only\"\n",
	}
	for _, candidate := range cases {
		if got := findHuaweiMultiLineDirective(candidate); got == "" {
			t.Errorf("findHuaweiMultiLineDirective(%q) = \"\", want a non-empty match", candidate)
		}
	}
}

func TestFindHuaweiMultiLineDirectiveAllowsOrdinaryConfig(t *testing.T) {
	if got := findHuaweiMultiLineDirective(sampleHuaweiCurrentConfig); got != "" {
		t.Errorf("findHuaweiMultiLineDirective(ordinary config) = %q, want \"\"", got)
	}
}

func TestDetectHuaweiRejectionMatchesConfirmedLiveErrorPrefix(t *testing.T) {
	// "Error: " is confirmed live from this exact device (see
	// huaweiRejectionPrefixPattern's doc comment) -- the specific wording
	// here (a syntax-error shape) is documented VRP convention, not
	// itself a live capture.
	result := transport.CommandResult{Output: strPtr("                  ^\nError: Unrecognized command found at '^' position.")}
	if got := detectHuaweiRejection(result); got == "" {
		t.Error("expected a non-empty rejection reason")
	}
}

func TestDetectHuaweiRejectionDoesNotFalsePositiveOnOrdinaryOutput(t *testing.T) {
	result := transport.CommandResult{Output: strPtr("sysname CoreSwitch01\n#\nreturn")}
	if got := detectHuaweiRejection(result); got != "" {
		t.Errorf("detectHuaweiRejection(ordinary output) = %q, want \"\"", got)
	}
}

func TestDetectHuaweiRejectionFlagsTruncatedOutput(t *testing.T) {
	result := transport.CommandResult{Output: strPtr("sysname CoreSwitch01"), Truncated: true}
	if got := detectHuaweiRejection(result); got == "" {
		t.Error("expected truncated output to be treated as a rejection")
	}
}

func TestHuaweiPredictNextPrompt(t *testing.T) {
	cases := []struct {
		name          string
		currentPrompt string
		cmd           string
		want          string
	}{
		{"system-view from user-view prompt", "<CoreSwitch01>", "system-view", "[CoreSwitch01]"},
		{"system-view is case-insensitive", "<CoreSwitch01>", "SYSTEM-VIEW", "[CoreSwitch01]"},
		{"interface opener from system-view prompt", "[CoreSwitch01]", "interface GigabitEthernet0/0/1", "[CoreSwitch01-GigabitEthernet0/0/1]"},
		{"return goes back to user-view", "[CoreSwitch01-GigabitEthernet0/0/1]", "return", "<CoreSwitch01>"},
		{"plain child line has no prediction", "[CoreSwitch01-GigabitEthernet0/0/1]", "description test", ""},
		{"undo-prefixed removal has no prediction", "[CoreSwitch01-GigabitEthernet0/0/1]", "undo shutdown", ""},
		{"quit has no prediction (applyMergeConfig never sends it)", "[CoreSwitch01-GigabitEthernet0/0/1]", "quit", ""},
		{"empty current prompt yields no prediction", "", "system-view", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := huaweiPredictNextPrompt(c.currentPrompt, c.cmd); got != c.want {
				t.Errorf("huaweiPredictNextPrompt(%q, %q) = %q, want %q", c.currentPrompt, c.cmd, got, c.want)
			}
		})
	}
}

func TestHuaweiLocateLinesFindsStanzaBodyRangesAndGlobalLines(t *testing.T) {
	lines := strings.Split(sampleHuaweiCurrentConfig, "\n")
	byHeader, globalLineIdxs := huaweiLocateLines(lines)

	gi1, ok := byHeader["interface GigabitEthernet0/0/1"]
	if !ok {
		t.Fatal("interface GigabitEthernet0/0/1 not found")
	}
	gotGi1 := lines[gi1.bodyStart:gi1.bodyEnd]
	wantGi1 := []string{" description old-desc", " port link-type trunk"}
	if !reflect.DeepEqual(gotGi1, wantGi1) {
		t.Errorf("Gi0/0/1 body = %#v, want %#v", gotGi1, wantGi1)
	}

	var globalLines []string
	for _, idx := range globalLineIdxs {
		globalLines = append(globalLines, lines[idx])
	}
	want := []string{"sysname CoreSwitch01", "vlan batch 2 to 10", "vlan batch 20 to 30"}
	if !reflect.DeepEqual(globalLines, want) {
		t.Errorf("global lines = %#v, want %#v (the leading \"!\" comment, \"#\" separators, and trailing \"return\" must all be skipped, not treated as global commands)", globalLines, want)
	}
}

func TestHuaweiApplyGlobalCommandReplacesExistingLineInPlace(t *testing.T) {
	lines := []string{"sysname CoreSwitch01"}
	got, op := huaweiApplyGlobalCommand(lines, []int{0}, "sysname NewName")
	if got[0] != "sysname NewName" {
		t.Errorf("lines[0] = %q, want \"sysname NewName\"", got[0])
	}
	if op.kind != "replace" || op.previousLine != "sysname CoreSwitch01" {
		t.Errorf("op = %#v, want kind=replace previousLine=\"sysname CoreSwitch01\"", op)
	}
}

func TestHuaweiApplyGlobalCommandUndoRemovesMatchingLine(t *testing.T) {
	lines := []string{"sysname CoreSwitch01", "stp mode mstp"}
	got, op := huaweiApplyGlobalCommand(lines, []int{0, 1}, "undo stp mode mstp")
	want := []string{"sysname CoreSwitch01"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %#v, want %#v", got, want)
	}
	if op.kind != "remove" || op.previousLine != "stp mode mstp" {
		t.Errorf("op = %#v, want kind=remove previousLine=\"stp mode mstp\"", op)
	}
}

// TestHuaweiApplyGlobalCommandVlanBatchNeverReplaces is the regression
// test for the real-hardware finding driving huaweiAlwaysAddCommandPattern
// -- see its doc comment. A NEW "vlan batch" command must always be
// appended as an additional line, never overwrite one of several existing
// wrapped "vlan batch" lines.
func TestHuaweiApplyGlobalCommandVlanBatchNeverReplaces(t *testing.T) {
	lines := []string{"vlan batch 2 to 10", "vlan batch 20 to 30"}
	got, op := huaweiApplyGlobalCommand(lines, []int{0, 1}, "vlan batch 100")
	want := []string{"vlan batch 2 to 10", "vlan batch 20 to 30", "vlan batch 100"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %#v, want %#v (both original \"vlan batch\" lines must survive untouched)", got, want)
	}
	if op.kind != "add" {
		t.Errorf("op.kind = %q, want \"add\"", op.kind)
	}
}

func TestHuaweiApplyGlobalCommandUndoVlanBatchIsNoOpNotARemoval(t *testing.T) {
	lines := []string{"vlan batch 2 to 10", "vlan batch 20 to 30"}
	got, op := huaweiApplyGlobalCommand(lines, []int{0, 1}, "undo vlan batch 5")
	if !reflect.DeepEqual(got, lines) {
		t.Errorf("lines = %#v, want unchanged %#v (no safe way to know which wrapped line to touch)", got, lines)
	}
	if op.kind != "no-op" {
		t.Errorf("op.kind = %q, want \"no-op\"", op.kind)
	}
}

func TestHuaweiApplyStanzaCommandReplaceAddAndUndoRemove(t *testing.T) {
	rng := stanzaRange{bodyStart: 0, bodyEnd: 2}

	t.Run("replace", func(t *testing.T) {
		lines := []string{" description old", " port link-type trunk"}
		got, op := huaweiApplyStanzaCommand(append([]string{}, lines...), rng, "description new")
		if got[0] != " description new" {
			t.Errorf("lines[0] = %q, want \" description new\"", got[0])
		}
		if op.kind != "replace" {
			t.Errorf("op.kind = %q, want \"replace\"", op.kind)
		}
	})

	t.Run("add", func(t *testing.T) {
		lines := []string{" description old", " port link-type trunk"}
		got, op := huaweiApplyStanzaCommand(append([]string{}, lines...), rng, "mtu 9216")
		if len(got) != 3 || got[2] != " mtu 9216" {
			t.Errorf("lines = %#v, want a new \" mtu 9216\" appended at the end of the body", got)
		}
		if op.kind != "add" {
			t.Errorf("op.kind = %q, want \"add\"", op.kind)
		}
	})

	t.Run("undo removes", func(t *testing.T) {
		lines := []string{" description old", " port link-type trunk"}
		got, op := huaweiApplyStanzaCommand(append([]string{}, lines...), rng, "undo port link-type")
		want := []string{" description old"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("lines = %#v, want %#v", got, want)
		}
		if op.kind != "remove" {
			t.Errorf("op.kind = %q, want \"remove\"", op.kind)
		}
	})
}

func TestHuaweiProjectMergedConfigSwitchesInterfaceContextWithoutExit(t *testing.T) {
	commands := []string{
		"interface GigabitEthernet0/0/1",
		"description new-desc",
		"interface GigabitEthernet0/0/2",
		"description also-new",
	}
	projected, _ := huaweiProjectMergedConfig(sampleHuaweiCurrentConfig, commands)
	if !strings.Contains(projected, " description new-desc") {
		t.Error("projected config missing new description for GigabitEthernet0/0/1")
	}
	if !strings.Contains(projected, " description also-new") {
		t.Error("projected config missing new description for GigabitEthernet0/0/2")
	}
}

func TestHuaweiProjectMergedConfigPreservesUnrelatedLineOrder(t *testing.T) {
	projected, _ := huaweiProjectMergedConfig(sampleHuaweiCurrentConfig, []string{
		"interface GigabitEthernet0/0/1",
		"description changed",
	})
	wantLines := strings.Split(sampleHuaweiCurrentConfig, "\n")
	gotLines := strings.Split(projected, "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("projected has %d lines, want %d (only the one changed line, no reordering/reserialization)", len(gotLines), len(wantLines))
	}
	for i, want := range wantLines {
		if want == " description old-desc" {
			if gotLines[i] != " description changed" {
				t.Errorf("line %d = %q, want \" description changed\"", i, gotLines[i])
			}
			continue
		}
		if gotLines[i] != want {
			t.Errorf("line %d = %q, want unchanged %q", i, gotLines[i], want)
		}
	}
}

func TestHuaweiReverseCommands(t *testing.T) {
	ops := []mergeOperation{
		{kind: "add", context: "interface GigabitEthernet0/0/1", appliedLine: "description new"},
		{kind: "replace", context: "", appliedLine: "sysname New", previousLine: "sysname Old"},
	}
	got := huaweiReverseCommands(ops)
	want := []string{
		"sysname Old",
		"interface GigabitEthernet0/0/1", "undo description new",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("huaweiReverseCommands = %#v, want %#v (walked in reverse; \"add\" inverts to \"undo \", not Cisco's \"no \")", got, want)
	}
}

// --- applyMergeConfig (method) ---

func TestHuaweiApplyMergeConfigHappyPath(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{}}
	d := &HuaweiDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	lines := []string{"interface GigabitEthernet0/0/1", "description test"}
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
	want := []string{"system-view", "interface GigabitEthernet0/0/1", "description test", "return"}
	if !reflect.DeepEqual(fake.sent, want) {
		t.Errorf("sent = %v, want %v", fake.sent, want)
	}
}

func TestHuaweiApplyMergeConfigStopsOnRejection(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"bad line": {output: "                  ^\nError: Unrecognized command found at '^' position."},
	}}
	d := &HuaweiDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	lines := []string{"interface GigabitEthernet0/0/1", "bad line", "description never reached"}
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
	want := []string{"system-view", "interface GigabitEthernet0/0/1", "bad line", "return"}
	if !reflect.DeepEqual(fake.sent, want) {
		t.Errorf("sent = %v, want %v (third candidate line must never be sent, but \"return\" must still fire)", fake.sent, want)
	}
}

func TestHuaweiApplyMergeConfigStopsOnConnectionDrop(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"dies here": {err: olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt")},
	}}
	d := &HuaweiDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	lines := []string{"interface GigabitEthernet0/0/1", "dies here", "description never reached"}
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
	want := []string{"system-view", "interface GigabitEthernet0/0/1", "dies here"}
	if !reflect.DeepEqual(fake.sent, want) {
		t.Errorf("sent = %v, want %v (\"return\" must NOT be attempted after a connection drop)", fake.sent, want)
	}
}

// --- LoadConfig ---

func TestHuaweiLoadConfigDryRunReturnsDiffAndHashWithoutWriting(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"display current-configuration": {output: sampleHuaweiCurrentConfig},
	}}
	d := &HuaweiDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	candidate := strings.Replace(sampleHuaweiCurrentConfig, "description old-desc", "description new-desc", 1)
	diff, hash, applied, err := d.LoadConfig(candidate, true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied {
		t.Error("dry run must never report applied=true")
	}
	if hash == "" {
		t.Error("expected a non-empty configHash")
	}
	if !strings.Contains(diff, "new-desc") {
		t.Errorf("diff missing expected change: %s", diff)
	}
	for _, cmd := range fake.sent {
		if cmd == "system-view" {
			t.Error("dry run must never enter system-view")
		}
	}
}

func TestHuaweiLoadConfigApplyRequiresExpectedConfigHash(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"display current-configuration": {output: sampleHuaweiCurrentConfig},
	}}
	d := &HuaweiDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, _, applied, err := d.LoadConfig(sampleHuaweiCurrentConfig, false, "")
	if err == nil {
		t.Fatal("expected an error when expectedConfigHash is empty and dryRun=false")
	}
	if applied {
		t.Error("applied must be false when the hash check fails")
	}
}

func TestHuaweiLoadConfigApplyRejectsStaleHash(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"display current-configuration": {output: sampleHuaweiCurrentConfig},
	}}
	d := &HuaweiDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, _, applied, err := d.LoadConfig(sampleHuaweiCurrentConfig, false, "stale-hash-value")
	if err == nil {
		t.Fatal("expected an error for a stale hash")
	}
	if applied {
		t.Error("applied must be false when the hash is stale")
	}
}

func TestHuaweiLoadConfigApplySucceedsWithFreshHash(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"display current-configuration": {output: sampleHuaweiCurrentConfig},
	}}
	d := &HuaweiDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	candidate := strings.Replace(sampleHuaweiCurrentConfig, "description old-desc", "description new-desc", 1)
	_, hash, _, err := d.LoadConfig(candidate, true, "")
	if err != nil {
		t.Fatalf("unexpected error on dry run: %v", err)
	}

	_, _, applied, err := d.LoadConfig(candidate, false, hash)
	if err != nil {
		t.Fatalf("unexpected error on apply: %v", err)
	}
	if !applied {
		t.Error("expected applied=true")
	}

	foundSystemView := false
	for _, cmd := range fake.sent {
		if cmd == "system-view" {
			foundSystemView = true
		}
	}
	if !foundSystemView {
		t.Error("expected apply to enter system-view")
	}
}

func TestHuaweiLoadConfigRefusesMultiLineDirectiveBeforeConnecting(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{}}
	d := &HuaweiDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, _, _, err := d.LoadConfig("header login information \"hi\"\n", true, "")
	if err == nil {
		t.Fatal("expected an error for a candidate containing a multi-line directive")
	}
	if len(fake.sent) != 0 {
		t.Errorf("sent = %v, want no commands sent at all (refused before ever connecting)", fake.sent)
	}
}

// --- LoadMergeCommands ---

func TestHuaweiLoadMergeCommandsDryRunShowsProjectedDiffWithoutWriting(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"display current-configuration": {output: sampleHuaweiCurrentConfig},
	}}
	d := &HuaweiDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	diff, reverseCmds, hash, applied, err := d.LoadMergeCommands(
		[]string{"interface GigabitEthernet0/0/1", "description new-desc"}, true, "",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied {
		t.Error("dry run must never report applied=true")
	}
	if hash == "" {
		t.Error("expected a non-empty configHash")
	}
	if len(reverseCmds) == 0 {
		t.Error("expected non-empty reverseCommands even on a dry run")
	}
	if !strings.Contains(diff, "new-desc") {
		t.Errorf("diff missing expected change: %s", diff)
	}
	for _, cmd := range fake.sent {
		if cmd == "system-view" {
			t.Error("dry run must never enter system-view")
		}
	}
}

func TestHuaweiLoadMergeCommandsApplyOnlySendsDeltaLines(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"display current-configuration": {output: sampleHuaweiCurrentConfig},
	}}
	d := &HuaweiDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	commands := []string{"interface GigabitEthernet0/0/1", "description new-desc"}
	_, _, hash, _, err := d.LoadMergeCommands(commands, true, "")
	if err != nil {
		t.Fatalf("unexpected error on dry run: %v", err)
	}

	fake.sent = nil // isolate what apply itself sends
	_, _, _, applied, err := d.LoadMergeCommands(commands, false, hash)
	if err != nil {
		t.Fatalf("unexpected error on apply: %v", err)
	}
	if !applied {
		t.Error("expected applied=true")
	}
	want := []string{"screen-length 0 temporary", "display current-configuration", "system-view", "interface GigabitEthernet0/0/1", "description new-desc", "return"}
	if !reflect.DeepEqual(fake.sent, want) {
		t.Errorf("sent = %v, want %v (only the delta lines, never the device's own full current config replayed back at it)", fake.sent, want)
	}
}

func TestHuaweiLoadMergeCommandsIgnoresMultiLineDirectiveInCurrentConfig(t *testing.T) {
	// The CURRENT config (from GetConfig) containing something
	// findHuaweiMultiLineDirective would flag must NOT cause LoadMergeCommands
	// to refuse -- only the DELTA is checked. See the CRITICAL warning in
	// LoadMergeCommands' doc comment.
	configWithHeader := "header login information \"hi\"\n#\nsysname CoreSwitch01\n#\nreturn\n"
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"display current-configuration": {output: configWithHeader},
	}}
	d := &HuaweiDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, _, _, _, err := d.LoadMergeCommands([]string{"sysname NewName"}, true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v (the header directive is in the CURRENT config, not the delta, and must be ignored)", err)
	}
}

func TestHuaweiLoadMergeCommandsApplyRequiresExpectedConfigHash(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"display current-configuration": {output: sampleHuaweiCurrentConfig},
	}}
	d := &HuaweiDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, _, _, applied, err := d.LoadMergeCommands([]string{"sysname NewName"}, false, "")
	if err == nil {
		t.Fatal("expected an error when expectedConfigHash is empty and dryRun=false")
	}
	if applied {
		t.Error("applied must be false")
	}
}

func TestHuaweiLoadMergeCommandsApplyReportsRejection(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"display current-configuration": {output: sampleHuaweiCurrentConfig},
		"bad command":                   {output: "Error: Unrecognized command found at '^' position."},
	}}
	d := &HuaweiDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	commands := []string{"bad command"}
	_, _, hash, _, err := d.LoadMergeCommands(commands, true, "")
	if err != nil {
		t.Fatalf("unexpected error on dry run: %v", err)
	}
	_, _, _, applied, err := d.LoadMergeCommands(commands, false, hash)
	if err == nil {
		t.Fatal("expected an error for a rejected command")
	}
	if applied {
		t.Error("applied must be false when the device rejects a line")
	}
}
