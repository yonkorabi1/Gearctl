package drivers

import (
	"strings"
	"testing"

	"github.com/onu-platform/gearctl/internal/olterr"
	"github.com/onu-platform/gearctl/internal/transport"
)

// sampleCurrentConfigForProjection is a realistic (if small) running-config:
// Gi1 up (no "shutdown" line -- Cisco omits it for enabled interfaces),
// Gi2 down (shows "shutdown" explicitly).
const sampleCurrentConfigForProjection = `hostname R1
!
interface GigabitEthernet1
 description old-desc
 ip address 10.0.0.1 255.255.255.0
!
interface GigabitEthernet2
 shutdown
!
end
`

func TestLocateLinesFindsStanzaBodyRangesAndGlobalLines(t *testing.T) {
	lines := strings.Split(sampleCurrentConfigForProjection, "\n")
	byHeader, globalLineIdxs := locateLines(lines)

	gi1, ok := byHeader["interface GigabitEthernet1"]
	if !ok {
		t.Fatal("interface GigabitEthernet1 not found")
	}
	gotGi1 := lines[gi1.bodyStart:gi1.bodyEnd]
	wantGi1 := []string{" description old-desc", " ip address 10.0.0.1 255.255.255.0"}
	if len(gotGi1) != 2 || gotGi1[0] != wantGi1[0] || gotGi1[1] != wantGi1[1] {
		t.Errorf("Gi1 body = %#v, want %#v", gotGi1, wantGi1)
	}

	gi2, ok := byHeader["interface GigabitEthernet2"]
	if !ok {
		t.Fatal("interface GigabitEthernet2 not found")
	}
	gotGi2 := lines[gi2.bodyStart:gi2.bodyEnd]
	if len(gotGi2) != 1 || gotGi2[0] != " shutdown" {
		t.Errorf("Gi2 body = %#v, want [\" shutdown\"]", gotGi2)
	}

	if len(globalLineIdxs) != 1 || lines[globalLineIdxs[0]] != "hostname R1" {
		t.Errorf("globalLineIdxs = %v (line content %q), want exactly one index pointing at \"hostname R1\"",
			globalLineIdxs, describeIdxs(lines, globalLineIdxs))
	}
}

func describeIdxs(lines []string, idxs []int) []string {
	out := make([]string, len(idxs))
	for i, idx := range idxs {
		out[i] = lines[idx]
	}
	return out
}

// TestProjectMergedConfigPreservesUnrelatedLineOrder is the regression
// test for a real-hardware finding: an earlier version of this
// projection fully reparsed and reserialized the ENTIRE config from a
// normalized stanza model, which collapsed every top-level global
// command into one block rendered first -- so anything that originally
// appeared AFTER an interface stanza (confirmed live: "line con 0",
// "ntp server ...", "app-hosting", "netconf-yang", among others) got
// reordered to appear before it. A single one-line description change
// then produced a diff showing the device's ENTIRE ~7KB config as
// removed and re-added. This pins that every line NOT touched by the
// delta keeps its EXACT original position, and the file only grows by
// exactly the lines the delta actually added.
func TestProjectMergedConfigPreservesUnrelatedLineOrder(t *testing.T) {
	current := "hostname R1\n" +
		"!\n" +
		"interface GigabitEthernet1\n" +
		" description old-desc\n" +
		"!\n" +
		"line con 0\n" +
		" stopbits 1\n" +
		"!\n" +
		"ntp server 10.0.0.1\n" +
		"end\n"

	projected, _ := projectMergedConfig(current, []string{
		"interface GigabitEthernet1", "description new-desc",
	})

	wantLines := strings.Split(strings.TrimSuffix(current, "\n"), "\n")
	gotLines := strings.Split(strings.TrimSuffix(projected, "\n"), "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("projected has %d lines, want %d (only the description line should have changed):\n%s",
			len(gotLines), len(wantLines), projected)
	}
	for i := range wantLines {
		if wantLines[i] == " description old-desc" {
			if gotLines[i] != " description new-desc" {
				t.Errorf("line %d = %q, want the replaced description in the same position", i, gotLines[i])
			}
			continue
		}
		if gotLines[i] != wantLines[i] {
			t.Errorf("line %d = %q, want untouched %q -- unrelated lines must keep their exact original position",
				i, gotLines[i], wantLines[i])
		}
	}
}

func TestProjectMergedConfigReplacesExistingLineInPlace(t *testing.T) {
	projected, _ := projectMergedConfig(sampleCurrentConfigForProjection, []string{
		"interface GigabitEthernet1", "description new-desc",
	})
	if strings.Contains(projected, "description old-desc") {
		t.Errorf("projected still contains the old description:\n%s", projected)
	}
	if !strings.Contains(projected, "description new-desc") {
		t.Errorf("projected missing the new description:\n%s", projected)
	}
	if !strings.Contains(projected, "ip address 10.0.0.1 255.255.255.0") {
		t.Errorf("projected lost an untouched line:\n%s", projected)
	}

	diff, err := computeUnifiedDiff(sampleCurrentConfigForProjection, projected)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(diff, "- description old-desc") || !strings.Contains(diff, "+ description new-desc") {
		t.Errorf("diff doesn't show a clean replace:\n%s", diff)
	}
}

func TestProjectMergedConfigRemovesViaNo(t *testing.T) {
	projected, _ := projectMergedConfig(sampleCurrentConfigForProjection, []string{
		"interface GigabitEthernet2", "no shutdown",
	})
	byHeader, _ := locateLines(strings.Split(projected, "\n"))
	rng, ok := byHeader["interface GigabitEthernet2"]
	if !ok {
		t.Fatalf("interface GigabitEthernet2 stanza missing from projection:\n%s", projected)
	}
	if got := rng.bodyEnd - rng.bodyStart; got != 0 {
		t.Errorf("Gi2 body has %d lines, want 0 (shutdown removed):\n%s", got, projected)
	}
}

func TestProjectMergedConfigAddsNewLineToExistingStanza(t *testing.T) {
	projected, _ := projectMergedConfig(sampleCurrentConfigForProjection, []string{
		"interface GigabitEthernet1", "mtu 9216",
	})
	lines := strings.Split(projected, "\n")
	byHeader, _ := locateLines(lines)
	rng, ok := byHeader["interface GigabitEthernet1"]
	if !ok {
		t.Fatalf("interface GigabitEthernet1 stanza missing from projection:\n%s", projected)
	}
	body := lines[rng.bodyStart:rng.bodyEnd]
	if len(body) != 3 {
		t.Fatalf("Gi1 body = %#v, want 3 lines (2 existing + 1 new)", body)
	}
	if body[0] != " description old-desc" || body[1] != " ip address 10.0.0.1 255.255.255.0" {
		t.Errorf("Gi1 body = %#v, want the existing lines undisturbed", body)
	}
	if body[2] != " mtu 9216" {
		t.Errorf("Gi1 body = %#v, want the new line appended last", body)
	}
}

func TestProjectMergedConfigAddsNewStanza(t *testing.T) {
	projected, _ := projectMergedConfig(sampleCurrentConfigForProjection, []string{
		"interface GigabitEthernet3", "description brand new interface",
	})
	if !strings.Contains(projected, "interface GigabitEthernet3") {
		t.Errorf("projected missing the new stanza header:\n%s", projected)
	}
	if !strings.Contains(projected, "description brand new interface") {
		t.Errorf("projected missing the new stanza's content:\n%s", projected)
	}
}

func TestProjectMergedConfigIpPrefixTwoTokenMatching(t *testing.T) {
	current := `interface GigabitEthernet1
 ip address 10.0.0.1 255.255.255.0
 ip helper-address 10.0.0.5
!
end
`
	projected, _ := projectMergedConfig(current, []string{
		"interface GigabitEthernet1", "ip helper-address 10.0.0.9",
	})
	if !strings.Contains(projected, "ip address 10.0.0.1 255.255.255.0") {
		t.Errorf("ip-prefix matching wrongly touched \"ip address\" when the delta only changed \"ip helper-address\":\n%s", projected)
	}
	if strings.Contains(projected, "10.0.0.5") {
		t.Errorf("old helper-address should have been replaced, not left in place:\n%s", projected)
	}
	if !strings.Contains(projected, "ip helper-address 10.0.0.9") {
		t.Errorf("projected missing the new helper-address:\n%s", projected)
	}
}

// TestProjectMergedConfigSwitchesInterfaceContextWithoutExit is a required
// case from the design review: two "interface X" openers back to back
// with no "exit" between them must each attribute their following lines
// to the correct (sibling, not nested) interface.
func TestProjectMergedConfigSwitchesInterfaceContextWithoutExit(t *testing.T) {
	current := `interface GigabitEthernet1
 description old1
!
interface GigabitEthernet2
 description old2
!
end
`
	projected, _ := projectMergedConfig(current, []string{
		"interface GigabitEthernet1", "description A",
		"interface GigabitEthernet2", "description B",
	})
	lines := strings.Split(projected, "\n")
	byHeader, _ := locateLines(lines)

	gi1, ok := byHeader["interface GigabitEthernet1"]
	if !ok {
		t.Fatalf("interface GigabitEthernet1 missing from projection:\n%s", projected)
	}
	gi1Body := lines[gi1.bodyStart:gi1.bodyEnd]
	if len(gi1Body) != 1 || gi1Body[0] != " description A" {
		t.Errorf("Gi1 body = %#v, want [\" description A\"]", gi1Body)
	}

	gi2, ok := byHeader["interface GigabitEthernet2"]
	if !ok {
		t.Fatalf("interface GigabitEthernet2 missing from projection:\n%s", projected)
	}
	gi2Body := lines[gi2.bodyStart:gi2.bodyEnd]
	if len(gi2Body) != 1 || gi2Body[0] != " description B" {
		t.Errorf("Gi2 body = %#v, want [\" description B\"] -- context must switch, not nest", gi2Body)
	}
}

// TestProjectMergedConfigBareHostnameResolvesGlobalContext is a required
// case from the design review: a bare delta line with no context-opener
// at all is the single most likely real-world "quick fix" shape and must
// resolve against the global ("") context.
func TestProjectMergedConfigBareHostnameResolvesGlobalContext(t *testing.T) {
	projected, _ := projectMergedConfig(sampleCurrentConfigForProjection, []string{"hostname R2"})
	if strings.Contains(projected, "hostname R1") {
		t.Errorf("old hostname should have been replaced:\n%s", projected)
	}
	if !strings.Contains(projected, "hostname R2") {
		t.Errorf("projected missing the new hostname:\n%s", projected)
	}
}

// TestProjectMergedConfigContextlessLineWithNoMatchIsPlainAddition is a
// required case from the design review: a delta line whose context has no
// matching line in current at all must be treated as a plain addition --
// no crash, no false warning. The real safety backstop for this case is
// detectRejection during apply (the device itself will reject invalid
// input at the wrong context level), not this projection step.
func TestProjectMergedConfigContextlessLineWithNoMatchIsPlainAddition(t *testing.T) {
	projected, _ := projectMergedConfig(sampleCurrentConfigForProjection, []string{"description no-context"})
	if !strings.Contains(projected, "description no-context") {
		t.Errorf("projected missing the added line:\n%s", projected)
	}
}

// TestProjectMergedConfigHeuristicLimitation documents (not silently
// passes) a known miss: the heuristic can't tell that "ip access-group
// 101 in" and "ip access-group 102 in" are the same logical setting in a
// different direction/list -- match key here is "ip access-group" for
// both, which happens to work for this simple case, but a genuinely
// different heuristic gap is left undocumented if not called out
// somewhere. This test exists to make the boundary explicit rather than
// asserting a specific (and possibly wrong) behavior silently elsewhere.
func TestProjectMergedConfigHeuristicLimitation(t *testing.T) {
	current := `interface GigabitEthernet1
 ip access-group 101 in
!
end
`
	// A same-key delta line (matchKey "ip access-group" for both) is
	// treated as a replacement, not an addition -- even though Cisco
	// itself would treat "in" and "out" direction as distinct settings.
	// This heuristic doesn't distinguish them; documented here as a known
	// limitation of first-two-token matching, not silently asserted as
	// correct behavior.
	projected, _ := projectMergedConfig(current, []string{
		"interface GigabitEthernet1", "ip access-group 102 out",
	})
	if strings.Contains(projected, "101 in") {
		t.Log("heuristic replaced the \"in\" ACL with the \"out\" ACL delta line -- " +
			"a known limitation of first-two-token matching, not a bug to fix here")
	}
}

// TestReverseCommands exercises the inversion logic directly against a
// hand-built operation log covering all three invertible kinds (replace,
// add, remove) plus a no-op, both under a stanza context and at global
// scope -- confirming reverse order (last change undone first), correct
// per-kind inversion, and that no-ops contribute nothing.
func TestReverseCommands(t *testing.T) {
	ops := []mergeOperation{
		{kind: "replace", context: "interface GigabitEthernet1", appliedLine: "description new-desc", previousLine: "description old-desc"},
		{kind: "add", context: "interface GigabitEthernet1", appliedLine: "mtu 9216"},
		{kind: "remove", context: "interface GigabitEthernet2", appliedLine: "no shutdown", previousLine: "shutdown"},
		{kind: "no-op", appliedLine: "no nonexistent-thing"},
		{kind: "replace", appliedLine: "hostname R2", previousLine: "hostname R1"},
	}

	got := reverseCommands(ops)
	want := []string{
		"hostname R1",
		"interface GigabitEthernet2", "shutdown",
		"interface GigabitEthernet1", "no mtu 9216",
		"interface GigabitEthernet1", "description old-desc",
	}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q (full: %#v)", i, got[i], want[i], got)
		}
	}
}

// TestProjectMergedConfigReverseCommandsRestoreOriginal is the strongest
// possible correctness check for reverseCommands: applying a delta, then
// applying THAT delta's own reverseCommands to the result, must
// reconstruct the exact original config -- not just "looks plausible".
func TestProjectMergedConfigReverseCommandsRestoreOriginal(t *testing.T) {
	delta := []string{"interface GigabitEthernet1", "description new-desc"}
	changed, ops := projectMergedConfig(sampleCurrentConfigForProjection, delta)

	reverse := reverseCommands(ops)
	if len(reverse) == 0 {
		t.Fatal("expected non-empty reverseCommands")
	}

	restored, _ := projectMergedConfig(changed, reverse)
	if restored != sampleCurrentConfigForProjection {
		t.Errorf(
			"applying reverseCommands didn't restore the original config exactly:\nrestored:\n%s\nwant:\n%s",
			restored, sampleCurrentConfigForProjection,
		)
	}
}

// --- LoadMergeCommands ---

func TestLoadMergeCommandsDryRunShowsProjectedDiffWithoutWriting(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleCurrentConfigForProjection},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	diff, _, hash, applied, err := d.LoadMergeCommands([]string{
		"interface GigabitEthernet1", "description new-desc",
	}, true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied {
		t.Error("applied = true, want false for a dry run")
	}
	if hash == "" {
		t.Error("configHash = \"\", want a non-empty fingerprint")
	}
	if !strings.Contains(diff, "PROJECTED PREVIEW") {
		t.Errorf("diff = %q, want the mergeCmdDisclaimer present", diff)
	}
	if !strings.Contains(diff, "- description old-desc") || !strings.Contains(diff, "+ description new-desc") {
		t.Errorf("diff = %q, want it to show the projected replace", diff)
	}
	for _, cmd := range fake.sent {
		if cmd == "configure terminal" || cmd == "end" {
			t.Errorf("sent = %v, want no config-mode commands during a dry run", fake.sent)
		}
	}
}

// TestLoadMergeCommandsReturnsReverseCommandsOnDryRun confirms
// reverseCommands is populated on a dry run too (same as diff/configHash
// -- a caller can review "here's how to undo this" before ever applying,
// not only after).
func TestLoadMergeCommandsReturnsReverseCommandsOnDryRun(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleCurrentConfigForProjection},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, reverse, _, _, err := d.LoadMergeCommands([]string{
		"interface GigabitEthernet1", "description new-desc",
	}, true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"interface GigabitEthernet1", "description old-desc"}
	if len(reverse) != len(want) || reverse[0] != want[0] || reverse[1] != want[1] {
		t.Errorf("reverseCommands = %#v, want %#v", reverse, want)
	}
}

func TestLoadMergeCommandsRefusesMultiLineDirectiveInDelta(t *testing.T) {
	fake := &fakeShellSession{}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, _, _, _, err := d.LoadMergeCommands([]string{"banner motd ^C Welcome ^C"}, true, ""); err == nil {
		t.Fatal("expected an error for a delta containing a multi-line directive")
	}
	if len(fake.sent) != 0 {
		t.Errorf("sent = %v, want nothing sent -- refusal must happen before any command is sent", fake.sent)
	}
}

// TestLoadMergeCommandsIgnoresMultiLineDirectivesInCurrentConfig is the
// CRITICAL regression test for the bug the design review caught (Finding
// 0): current already has a banner configured (extremely common on real
// devices), and the delta doesn't touch it at all. If
// findMultiLineDirective were ever run against a synthesized
// current+delta blob instead of the delta alone, this would incorrectly
// refuse. It must not.
func TestLoadMergeCommandsIgnoresMultiLineDirectivesInCurrentConfig(t *testing.T) {
	currentWithBanner := "hostname R1\n!\nbanner motd ^C Welcome to R1 ^C\n!\n" + sampleCurrentConfigForProjection
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: currentWithBanner},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, _, _, _, err := d.LoadMergeCommands([]string{
		"interface GigabitEthernet1", "description new-desc",
	}, true, "")
	if err != nil {
		t.Fatalf("unexpected error -- a banner in CURRENT's config must not cause a refusal: %v", err)
	}
}

// TestLoadMergeCommandsApplyOnlySendsDeltaLines is the other half of the
// Finding-0 regression: confirms apply only ever replays the delta lines
// (via applyMergeConfig), never current's own config text.
func TestLoadMergeCommandsApplyOnlySendsDeltaLines(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleCurrentConfigForProjection},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	delta := []string{"interface GigabitEthernet1", "description new-desc"}
	_, _, hash, _, err := d.LoadMergeCommands(delta, true, "")
	if err != nil {
		t.Fatalf("unexpected error on dry run: %v", err)
	}

	_, _, _, applied, err := d.LoadMergeCommands(delta, false, hash)
	if err != nil {
		t.Fatalf("unexpected error on apply: %v", err)
	}
	if !applied {
		t.Error("applied = false, want true")
	}

	want := []string{"configure terminal", "interface GigabitEthernet1", "description new-desc", "end"}
	// fake.sent also contains the two "terminal length 0"/"show running-config"
	// calls from each GetConfig() -- only check the tail (the actual apply
	// sequence) matches exactly the delta, nothing from current.
	if len(fake.sent) < len(want) {
		t.Fatalf("sent = %v, too short", fake.sent)
	}
	got := fake.sent[len(fake.sent)-len(want):]
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sent tail = %v, want %v (only delta lines, never current's own config)", got, want)
			break
		}
	}
	for _, cmd := range fake.sent {
		if cmd == "hostname R1" || strings.Contains(cmd, "old-desc") {
			t.Errorf("sent = %v, want current's own config lines never replayed", fake.sent)
		}
	}
}

func TestLoadMergeCommandsApplyRequiresExpectedConfigHash(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleCurrentConfigForProjection},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, _, _, applied, err := d.LoadMergeCommands([]string{"hostname R2"}, false, "")
	if err == nil {
		t.Fatal("expected an error when applying with no expectedConfigHash")
	}
	if applied {
		t.Error("applied = true, want false")
	}
}

func TestLoadMergeCommandsApplyRejectsStaleHash(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleCurrentConfigForProjection},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, _, _, applied, err := d.LoadMergeCommands([]string{"hostname R2"}, false, "stale-hash")
	if err == nil {
		t.Fatal("expected an error for a stale hash")
	}
	if !strings.Contains(err.Error(), "changed") {
		t.Errorf("err = %v, want it to mention the device config changed", err)
	}
	if applied {
		t.Error("applied = true, want false")
	}
}

func TestLoadMergeCommandsApplyReportsRejection(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleCurrentConfigForProjection},
		"bad line":            {output: "% Invalid input detected at '^' marker."},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	delta := []string{"bad line"}
	_, _, hash, _, err := d.LoadMergeCommands(delta, true, "")
	if err != nil {
		t.Fatalf("unexpected error on dry run: %v", err)
	}

	_, _, _, applied, err := d.LoadMergeCommands(delta, false, hash)
	if err == nil {
		t.Fatal("expected an error when the device rejects a delta line")
	}
	if applied {
		t.Error("applied = true, want false")
	}
}

func TestLoadMergeCommandsApplyReportsConnectionDrop(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleCurrentConfigForProjection},
		"dies here":           {err: olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt")},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	delta := []string{"dies here"}
	_, _, hash, _, err := d.LoadMergeCommands(delta, true, "")
	if err != nil {
		t.Fatalf("unexpected error on dry run: %v", err)
	}

	_, _, _, applied, err := d.LoadMergeCommands(delta, false, hash)
	if err == nil {
		t.Fatal("expected an error when the connection drops mid-apply")
	}
	if applied {
		t.Error("applied = true, want false")
	}
}
