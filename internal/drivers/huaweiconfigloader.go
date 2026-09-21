package drivers

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/onu-platform/gearctl/internal/olterr"
	"github.com/onu-platform/gearctl/internal/transport"
)

// This file implements HuaweiDriver's ConfigLoader (config push), adapted
// from CiscoIOSDriver's stanza-diffing approach in mergeconfig.go/
// mergecmd.go (ciscoiosdriver.go) -- VRP's system-view/interface-view
// modal CLI is structurally close enough to Cisco IOS's (indentation-
// delimited stanzas, a context-opening header line, per-line apply) that
// the same overall algorithm carries over, with VRP's real syntax swapped
// in wherever it differs from Cisco's.
//
// IMPORTANT -- verification status: GetFacts/GetInterfaces/GetLLDPNeighbors/
// GetConfig (huaweidriver.go) are confirmed LIVE against a real Huawei
// S6700 core switch. This file is NOT -- entering system-view, sending a
// config-mode command, and detecting a rejection have never actually been
// tried against a real device. What IS grounded in real evidence from
// that same live session (not blind guesses):
//   - `display current-configuration`'s real format: CRLF line endings,
//     "#" as a bare stanza/section separator (VRP's "!"), a leading
//     "!Software Version ..." comment line, "return" as the trailing
//     sentinel (VRP's "end"), and indentation (one leading space) marking
//     a stanza's body lines -- all confirmed live from a real ~16KB
//     current-configuration capture.
//   - VRP's negation keyword is "undo", not Cisco's "no" -- confirmed
//     TWICE live: this is well-documented VRP syntax, and separately the
//     real captured config itself contains a literal stored line "undo
//     port trunk allow-pass vlan 1" under a real interface stanza.
//   - VRP's own CLI-level rejection messages are prefixed "Error: " --
//     confirmed live (though only for a login/auth rejection, "Error:
//     Authentication fail", not a command-syntax rejection specifically;
//     see huaweiRejectionPrefixPattern's doc comment).
//   - A real global command, "vlan batch ...", appears as EIGHT separate
//     consecutive lines in this device's real saved config (VRP wrapping
//     one very long VLAN member list across several lines) -- directly
//     informed huaweiAlwaysAddCommandPattern below, see its doc comment.
//
// Everything else here (system-view/interface-view prompt shapes,
// "return" as the mode-exit command, the multi-line-directive guard) is
// built from VRP's documented syntax, not observed live. Test carefully,
// dry-run first, on real hardware before trusting this with anything
// that matters.

// huaweiMergeConfigDisclaimer is prepended to every diff LoadConfig
// returns -- same rationale as CiscoIOSDriver's mergeConfigDisclaimer
// (see its doc comment): stated in the data itself, not just README
// prose, so it's in front of whoever is about to decide to apply.
const huaweiMergeConfigDisclaimer = "NOTE: this is a merge/overlay diff, not a replace-config diff -- " +
	"lines shown removed (-) will NOT be deleted from the device, only added/changed (+) lines are pushed. " +
	"VRP's negation keyword is \"undo\", not Cisco's \"no\" -- a candidate built by hand should use \"undo <command>\" " +
	"to remove something.\n\n"

// huaweiMultiLineDirectivePattern matches VRP config directives that open
// a terminator-delimited text block rather than a single command --
// VRP's "header login"/"header shell"/"header incoming" banner commands
// (the documented VRP equivalent of Cisco's "banner motd"/"banner
// login"). NOT live-verified -- built from VRP's documented syntax only,
// unlike CiscoIOSDriver's equivalent pattern (confirmed live against real
// banner/crypto config). Replaying a multi-line block naively through
// this driver's one-line-at-a-time apply risks hanging the session
// (waiting for a prompt that won't reappear until the block's own
// terminator is typed) -- refused outright (fail closed, before ever
// connecting) rather than attempted best-effort, same posture as
// CiscoIOSDriver's findMultiLineDirective.
var huaweiMultiLineDirectivePattern = regexp.MustCompile(`(?im)^\s*header\s+(login|shell|incoming)\b`)

func findHuaweiMultiLineDirective(candidate string) string {
	for _, line := range strings.Split(candidate, "\n") {
		if huaweiMultiLineDirectivePattern.MatchString(line) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// parseHuaweiCandidateLines splits a full candidate config into the lines
// LoadConfig will actually replay: blank lines, VRP's "#" bare
// section-separator lines, "!"-prefixed comment lines (e.g. the real,
// confirmed-live leading "!Software Version V200R005C00SPC500"), and a
// trailing "return" sentinel are all stripped -- visual formatting or a
// `display current-configuration` end-of-output marker, not real
// commands. Confirmed live that `display current-configuration` always
// carries the "return" sentinel at the very end; replaying it as a real
// command would exit system-view one line early, same reasoning
// CiscoIOSDriver's parseCandidateLines documents for Cisco's "end".
func parseHuaweiCandidateLines(candidate string) []string {
	var lines []string
	for _, line := range strings.Split(candidate, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed == "" || trimmed == "#" || strings.HasPrefix(trimmed, "!") || strings.EqualFold(trimmed, "return") {
			continue
		}
		lines = append(lines, trimmed)
	}
	return lines
}

// huaweiRejectionPrefixPattern matches VRP's own CLI-level error
// convention. PARTIALLY confirmed live: this exact device answered a
// rejected telnet login with "Error: Authentication fail" (captured
// during this driver's own development, see huaweidriver.go's package
// doc comment for the full session) -- confirming VRP uses "Error: " (a
// literal space after the colon) as its rejection-message prefix at the
// CLI level in general. What's NOT confirmed live is the specific
// wording for a REJECTED CONFIG COMMAND (VRP's documented convention is
// something like "Error: Unrecognized command found at '^' position.",
// but this driver has never actually sent an invalid command to a real
// device to see it). If real testing finds a rejection that doesn't
// start with "Error: ", broaden this pattern.
var huaweiRejectionPrefixPattern = regexp.MustCompile(`(?m)^Error:\s`)

// detectHuaweiRejection is CiscoIOSDriver.detectRejection's VRP sibling
// -- same semantics: VRP reports a bad command as normal OUTPUT text, not
// a transport-level failure, so this is a semantic, output-scanning
// check layered on top of RunCommands. Also treats Truncated as a
// rejection, same reasoning as Cisco's: config-mode commands don't
// normally paginate, so truncation here is itself an anomaly.
func detectHuaweiRejection(result transport.CommandResult) string {
	if result.Truncated {
		return "response was truncated -- treating as a failed/uncertain apply rather than trusting partial output"
	}
	if result.Output == nil {
		return ""
	}
	for _, line := range strings.Split(*result.Output, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if huaweiRejectionPrefixPattern.MatchString(trimmed) {
			return trimmed
		}
	}
	return ""
}

// huaweiPredictNextPrompt is CiscoIOSDriver.predictNextPrompt's VRP
// sibling -- predicts the prompt VRP will show after sending cmd, for the
// small set of mode-transition commands huaweiApplyMergeConfig actually
// sends: "system-view", an "interface X" context opener, and "return".
// Pure speed optimization (see RunCommandWithHint's doc comment) -- a
// wrong guess just costs the ordinary quiescence-wait fallback, never
// unsafe. NOT live-verified (this driver has never actually entered
// system-view against a real device) -- built from VRP's documented
// prompt shapes: "<hostname>" in user-view, "[hostname]" in system-view,
// "[hostname-InterfaceName]" in interface-view (the same shapes
// parseHuaweiPromptHostname already relies on for the read-only
// capabilities, which WERE confirmed live for the user-view case).
func huaweiPredictNextPrompt(currentPrompt, cmd string) string {
	hostname := parseHuaweiPromptHostname(currentPrompt)
	if hostname == "" {
		return ""
	}

	switch {
	case strings.EqualFold(cmd, "system-view"):
		return "[" + hostname + "]"
	case strings.EqualFold(cmd, "return"):
		return "<" + hostname + ">"
	case strings.HasPrefix(cmd, "interface "):
		return "[" + hostname + "-" + strings.TrimSpace(strings.TrimPrefix(cmd, "interface ")) + "]"
	default:
		return ""
	}
}

// runSingle is CiscoIOSDriver.runSingle's VRP sibling -- one command at a
// time (not a batched RunCommands call), so detectHuaweiRejection can be
// checked after each one before the next is sent; see Cisco's own
// runSingle doc comment for why batching would break fail-fast-on-
// rejection.
func (d *HuaweiDriver) runSingle(command string) (transport.CommandResult, error) {
	hint := huaweiPredictNextPrompt(d.CurrentPrompt(), command)
	return d.RunCommandWithHint(command, hint)
}

// applyMergeConfig is CiscoIOSDriver.applyMergeConfig's VRP sibling:
// enters system-view (VRP's "configure terminal" equivalent), replays
// lines one at a time, and leaves via "return" (VRP's own documented way
// to jump straight back to user-view from ANY depth in one step --
// deliberately used here instead of "quit", which only steps back one
// level and would leave the session in system-view, not user-view, after
// applying interface-level changes). Same three outcomes as Cisco's
// version -- see its doc comment for the full breakdown (transport
// failure / clean rejection / full success).
func (d *HuaweiDriver) applyMergeConfig(lines []string) (appliedCount int, rejection string, err error) {
	entry, err := d.runSingle("system-view")
	if err != nil {
		return 0, "", err
	}
	if entry.Error != "" {
		return 0, "", olterr.New(olterr.TypeConnection, "connection lost entering system-view: "+entry.Error)
	}
	if reason := detectHuaweiRejection(entry); reason != "" {
		return 0, "entering system-view: " + reason, nil
	}

	for i, line := range lines {
		result, err := d.runSingle(line)
		if err != nil {
			return appliedCount, "", err
		}
		if result.Error != "" {
			return appliedCount, "", olterr.New(
				olterr.TypeConnection,
				fmt.Sprintf(
					"connection lost after applying %d of %d lines (line %d, %q: %s) -- "+
						"device state not verified, re-run GetConfig before retrying",
					appliedCount, len(lines), i+1, line, result.Error,
				),
			)
		}
		if reason := detectHuaweiRejection(result); reason != "" {
			_, _ = d.runSingle("return") // best-effort clean exit; its own outcome isn't reported
			return appliedCount, fmt.Sprintf("line %d (%q): %s", i+1, line, reason), nil
		}
		appliedCount++
	}

	end, err := d.runSingle("return")
	if err != nil {
		return appliedCount, "", err
	}
	if end.Error != "" {
		return appliedCount, "", olterr.New(
			olterr.TypeConnection,
			"connection lost exiting system-view after applying all "+fmt.Sprint(appliedCount)+" lines: "+end.Error,
		)
	}
	return appliedCount, "", nil
}

// LoadConfig implements ConfigLoader's full-candidate entry point -- see
// driver.go's interface doc comment for the shared dry-run/hash-echo
// contract (identical to CiscoIOSDriver's), and huaweiMergeConfigDisclaimer
// for why this is a merge, not a replace, plus the "undo" vs "no" note.
//
// candidate should be a full config (e.g. a prior GetConfig() call,
// edited) -- same reasoning as CiscoIOSDriver.LoadConfig's doc comment
// for why a partial snippet would produce a meaningless diff.
func (d *HuaweiDriver) LoadConfig(candidate string, dryRun bool, expectedConfigHash string) (string, string, bool, error) {
	if directive := findHuaweiMultiLineDirective(candidate); directive != "" {
		return "", "", false, olterr.New(
			olterr.TypeValidation,
			fmt.Sprintf(
				"candidate contains a multi-line directive (%q) gearctl cannot safely replay one line at a time -- refusing",
				directive,
			),
		)
	}

	current, err := d.GetConfig()
	if err != nil {
		return "", "", false, err
	}
	currentHash := configHashOf(current)

	diff, err := computeUnifiedDiff(current, candidate)
	if err != nil {
		return "", currentHash, false, olterr.New(olterr.TypeProtocol, "failed to compute diff: "+err.Error())
	}
	diff = huaweiMergeConfigDisclaimer + diff

	if dryRun {
		return diff, currentHash, false, nil
	}

	if expectedConfigHash == "" {
		return diff, currentHash, false, olterr.New(
			olterr.TypeValidation,
			"expectedConfigHash is required to apply -- call with dryRun=true first and pass back the configHash it returns",
		)
	}
	if expectedConfigHash != currentHash {
		return diff, currentHash, false, olterr.New(
			olterr.TypeValidation,
			"device config has changed since the dry-run this hash came from -- re-run dry-run and review the new diff before applying",
		)
	}

	lines := parseHuaweiCandidateLines(candidate)
	appliedCount, rejection, err := d.applyMergeConfig(lines)
	if err != nil {
		return diff, currentHash, false, err
	}
	if rejection != "" {
		return diff, currentHash, false, olterr.New(
			olterr.TypeValidation,
			fmt.Sprintf(
				"device rejected the candidate after applying %d of %d lines (%s) -- "+
					"system-view was exited cleanly, no further lines were attempted",
				appliedCount, len(lines), rejection,
			),
		)
	}
	return diff, currentHash, true, nil
}

// huaweiMergeCmdDisclaimer is prepended to every diff LoadMergeCommands
// returns, unconditionally -- same rationale as CiscoIOSDriver's
// mergeCmdDisclaimer (see its doc comment for why "no replacement
// flagged" must never be silently read as "verified safe").
const huaweiMergeCmdDisclaimer = "NOTE: this is a PROJECTED PREVIEW using simple first-token key-matching " +
	"within the same config section to guess at replacements -- NOT real VRP command-grammar parsing. " +
	"It can miss replacements for less common command shapes, and does not attempt VRP's own \"ip\"-style " +
	"multi-token overloaded-prefix disambiguation the way CiscoIOSDriver's Cisco-specific heuristic does " +
	"(no VRP equivalent has been confirmed, so none is special-cased here). Review carefully before applying, " +
	"especially for anything not a straightforward description/address/state-style change -- and see " +
	"huaweiAlwaysAddCommandPattern's doc comment for one confirmed-live command shape (\"vlan batch\") this " +
	"heuristic deliberately never tries to replace in place.\n\n"

// huaweiContextOpenerPrefixes: recognized block-opening commands, used
// ONLY to track position within a DELTA (which lacks huaweiLocateLines'
// real indentation -- same reasoning as CiscoIOSDriver's
// contextOpenerPrefixes, a deliberately separate, smaller list). Only
// "interface " and "user-interface " are included -- both confirmed live
// as real indentation-delimited stanza headers in this device's own
// saved config (interface stanzas throughout; "user-interface con 0"/
// "user-interface vty 0 4"/"user-interface vty 16 20" at the end). VRP
// has other view-entering commands (e.g. "bgp <as>", "acl <number>",
// "ospf <process>") not included here -- not a claim of exhaustive VRP
// command-grammar coverage, narrower even than Cisco's own non-exhaustive
// list, since only these two are grounded in something actually observed.
var huaweiContextOpenerPrefixes = []string{"interface ", "user-interface "}

func isHuaweiContextOpener(line string) bool {
	for _, prefix := range huaweiContextOpenerPrefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// huaweiAlwaysAddCommandPattern matches VRP commands that are additive/
// repeatable by nature, where issuing the command again does NOT replace
// a prior line the way "description X" replacing an earlier "description
// Y" does -- confirmed LIVE: this device's own real saved config has
// EIGHT separate "vlan batch ..." lines in a row, each a distinct
// fragment of one very long VLAN member list VRP wrapped across multiple
// lines, not eight competing "current value" candidates. huaweiMatchKey's
// generic first-token heuristic would treat all eight as sharing key
// "vlan" and wrongly show a NEW "vlan batch" delta command as REPLACING
// one of them (discarding whichever VLANs happened to be on that specific
// line) instead of being a ninth, purely additive line.
//
// Commands matching this are handled specially in huaweiApplyGlobalCommand:
// the ADD form always appends as a new line, never searches for an
// existing one to replace; the "undo" form is treated as a no-op rather
// than removing ANY existing "vlan batch" line, since there is no safe
// way for this heuristic to know which of several wrapped lines actually
// contains the VLAN(s) being undone. This is deliberately conservative
// (a no-op preview understates what a real "undo vlan batch ..." would
// do on the device) rather than risking the projection showing the WRONG
// line as removed.
//
// Only "vlan batch" is covered -- there may be other VRP global commands
// with the same "wraps across multiple same-prefix lines" shape that
// haven't been observed in this one device's config and would still hit
// the original bug this pattern fixes.
var huaweiAlwaysAddCommandPattern = regexp.MustCompile(`(?i)^vlan\s+batch\b`)

// huaweiMatchKey returns the "key" used to decide whether a delta line
// replaces an existing line in the same stanza/global scope -- the first
// token, lowercased. Deliberately simpler than CiscoIOSDriver.matchKey's
// "ip"-prefix two-token special case: no VRP equivalent of Cisco's
// heavily-overloaded "ip ..." prefix has been confirmed, so none is
// guessed here. This is a real, documented gap versus Cisco's heuristic,
// not an oversight -- see huaweiMergeCmdDisclaimer.
func huaweiMatchKey(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[0])
}

// huaweiLocateLines is CiscoIOSDriver.locateLines' VRP sibling -- same
// indentation-based algorithm (see Cisco's doc comment for the full
// rationale, including why line INDICES rather than copied content are
// returned), just recognizing VRP's own separator/sentinel/comment
// conventions instead of Cisco's: a bare "#" line (VRP's "!"), a
// "!"-prefixed comment line (VRP-specific, no Cisco equivalent -- e.g.
// the real, confirmed-live leading "!Software Version ..." line), and
// "return" (VRP's "end") are all skipped rather than treated as content.
func huaweiLocateLines(lines []string) (byHeader map[string]stanzaRange, globalLineIdxs []int) {
	byHeader = make(map[string]stanzaRange)
	isIndented := func(raw string) bool {
		return len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\t')
	}

	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "#" || strings.HasPrefix(trimmed, "!") || strings.EqualFold(trimmed, "return") || isIndented(line) {
			continue
		}

		bodyStart := i + 1
		bodyEnd := bodyStart
		for k := bodyStart; k < len(lines); k++ {
			kTrimmed := strings.TrimSpace(strings.TrimRight(lines[k], "\r"))
			if kTrimmed == "" || !isIndented(lines[k]) {
				break
			}
			bodyEnd = k + 1
		}

		if bodyEnd > bodyStart || isHuaweiContextOpener(trimmed) {
			byHeader[trimmed] = stanzaRange{bodyStart: bodyStart, bodyEnd: bodyEnd}
		} else {
			globalLineIdxs = append(globalLineIdxs, i)
		}
	}
	return byHeader, globalLineIdxs
}

// huaweiProjectMergedConfig is CiscoIOSDriver.projectMergedConfig's VRP
// sibling -- same simulate-for-preview-only algorithm (never sent to the
// device; see Cisco's doc comment for the full rationale), with "quit"
// added alongside "return" as a context-popping command (VRP has both:
// "quit" steps back one level, "return" jumps straight to user-view --
// this function doesn't track nesting depth beyond one level, so both
// are treated identically here, same simplification Cisco's version
// makes for "exit"/"end").
func huaweiProjectMergedConfig(current string, commands []string) (string, []mergeOperation) {
	lines := strings.Split(current, "\n")
	var ops []mergeOperation

	context := ""
	for _, cmd := range commands {
		if isHuaweiContextOpener(cmd) {
			context = cmd
			continue
		}
		if strings.EqualFold(cmd, "quit") || strings.EqualFold(cmd, "return") {
			context = ""
			continue
		}

		byHeader, globalLineIdxs := huaweiLocateLines(lines)

		if context == "" {
			var op mergeOperation
			lines, op = huaweiApplyGlobalCommand(lines, globalLineIdxs, cmd)
			ops = append(ops, op)
			continue
		}

		rng, ok := byHeader[context]
		if !ok {
			lines = append(lines, context, " "+cmd, "#")
			ops = append(ops, mergeOperation{kind: "add", context: context, appliedLine: cmd})
			continue
		}
		var op mergeOperation
		lines, op = huaweiApplyStanzaCommand(lines, rng, cmd)
		op.context = context
		ops = append(ops, op)
	}

	return strings.Join(lines, "\n"), ops
}

// huaweiApplyGlobalCommand is CiscoIOSDriver.applyGlobalCommand's VRP
// sibling: "undo " (not "no ") as the removal prefix, huaweiMatchKey (not
// Cisco's matchKey) for matching, and huaweiAlwaysAddCommandPattern
// checked FIRST -- before either the add or undo path's normal
// key-matching search -- so a "vlan batch ..." command (in either
// direction) never touches an existing line; see that pattern's own doc
// comment for why.
func huaweiApplyGlobalCommand(lines []string, globalLineIdxs []int, cmd string) ([]string, mergeOperation) {
	if rest, isUndo := strings.CutPrefix(cmd, "undo "); isUndo {
		if huaweiAlwaysAddCommandPattern.MatchString(rest) {
			return lines, mergeOperation{kind: "no-op", appliedLine: cmd}
		}
		key := huaweiMatchKey(rest)
		for _, idx := range globalLineIdxs {
			line := strings.TrimSpace(lines[idx])
			if huaweiMatchKey(line) == key {
				return append(lines[:idx], lines[idx+1:]...), mergeOperation{kind: "remove", appliedLine: cmd, previousLine: line}
			}
		}
		return lines, mergeOperation{kind: "no-op", appliedLine: cmd}
	}

	if huaweiAlwaysAddCommandPattern.MatchString(cmd) {
		return append(lines, cmd), mergeOperation{kind: "add", appliedLine: cmd}
	}

	key := huaweiMatchKey(cmd)
	for _, idx := range globalLineIdxs {
		line := strings.TrimSpace(lines[idx])
		if huaweiMatchKey(line) == key {
			lines[idx] = cmd
			return lines, mergeOperation{kind: "replace", appliedLine: cmd, previousLine: line}
		}
	}
	return append(lines, cmd), mergeOperation{kind: "add", appliedLine: cmd}
}

// huaweiApplyStanzaCommand is CiscoIOSDriver.applyStanzaCommand's VRP
// sibling: "undo " (not "no ") as the removal prefix, huaweiMatchKey (not
// Cisco's matchKey). No vlan-batch-style guard here -- the one confirmed-
// live instance of that shape ("vlan batch") is a global command, not a
// stanza body line; a real interface stanza's own repeatable-looking
// command ("port trunk allow-pass vlan X") was observed live to
// consolidate onto ONE line per interface in VRP's saved config, not wrap
// across several the way "vlan batch" does, so ordinary replace-in-place
// matching is not known to be wrong for stanza body lines the way it
// would be for "vlan batch".
func huaweiApplyStanzaCommand(lines []string, rng stanzaRange, cmd string) ([]string, mergeOperation) {
	if rest, isUndo := strings.CutPrefix(cmd, "undo "); isUndo {
		key := huaweiMatchKey(rest)
		for i := rng.bodyStart; i < rng.bodyEnd; i++ {
			line := strings.TrimSpace(lines[i])
			if huaweiMatchKey(line) == key {
				return append(lines[:i], lines[i+1:]...), mergeOperation{kind: "remove", appliedLine: cmd, previousLine: line}
			}
		}
		return lines, mergeOperation{kind: "no-op", appliedLine: cmd}
	}

	key := huaweiMatchKey(cmd)
	for i := rng.bodyStart; i < rng.bodyEnd; i++ {
		line := strings.TrimSpace(lines[i])
		if huaweiMatchKey(line) == key {
			lines[i] = " " + cmd
			return lines, mergeOperation{kind: "replace", appliedLine: cmd, previousLine: line}
		}
	}

	insertAt := rng.bodyEnd
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:insertAt]...)
	out = append(out, " "+cmd)
	out = append(out, lines[insertAt:]...)
	return out, mergeOperation{kind: "add", appliedLine: cmd}
}

// huaweiReverseCommands is CiscoIOSDriver.reverseCommands' VRP sibling --
// same walked-in-reverse derivation (see Cisco's doc comment for the full
// rationale and the "commit confirmed"-style use case this exists for),
// with "add"'s inverse built as "undo "+appliedLine (VRP's own undo
// convention) instead of "no "+appliedLine.
func huaweiReverseCommands(ops []mergeOperation) []string {
	var out []string
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		var inverse string
		switch op.kind {
		case "replace", "remove":
			inverse = op.previousLine
		case "add":
			inverse = "undo " + op.appliedLine
		default: // "no-op"
			continue
		}
		if op.context != "" {
			out = append(out, op.context)
		}
		out = append(out, inverse)
	}
	return out
}

// LoadMergeCommands implements ConfigLoader's delta-based entry point --
// see CiscoIOSDriver.LoadMergeCommands' doc comment for the shared
// contract and the CRITICAL warning about never handing current+commands
// as one synthesized blob to LoadConfig's candidate-handling logic
// (findHuaweiMultiLineDirective/parseHuaweiCandidateLines/applyMergeConfig
// below are all called with the delta ONLY, exactly mirroring Cisco's
// structure for the same reason).
func (d *HuaweiDriver) LoadMergeCommands(
	commands []string, dryRun bool, expectedConfigHash string,
) (diff string, reverseCmds []string, configHash string, applied bool, err error) {
	joined := strings.Join(commands, "\n")
	if directive := findHuaweiMultiLineDirective(joined); directive != "" {
		return "", nil, "", false, olterr.New(
			olterr.TypeValidation,
			fmt.Sprintf(
				"candidate contains a multi-line directive (%q) gearctl cannot safely replay one line at a time -- refusing",
				directive,
			),
		)
	}
	lines := parseHuaweiCandidateLines(joined)

	current, err := d.GetConfig()
	if err != nil {
		return "", nil, "", false, err
	}
	currentHash := configHashOf(current)

	projected, ops := huaweiProjectMergedConfig(current, lines)
	reverseCmds = huaweiReverseCommands(ops)
	diff, err = computeUnifiedDiff(current, projected)
	if err != nil {
		return "", reverseCmds, currentHash, false, olterr.New(olterr.TypeProtocol, "failed to compute diff: "+err.Error())
	}
	diff = huaweiMergeCmdDisclaimer + diff

	if dryRun {
		return diff, reverseCmds, currentHash, false, nil
	}

	if expectedConfigHash == "" {
		return diff, reverseCmds, currentHash, false, olterr.New(
			olterr.TypeValidation,
			"expectedConfigHash is required to apply -- call with dryRun=true first and pass back the configHash it returns",
		)
	}
	if expectedConfigHash != currentHash {
		return diff, reverseCmds, currentHash, false, olterr.New(
			olterr.TypeValidation,
			"device config has changed since the dry-run this hash came from -- re-run dry-run and review the new diff before applying",
		)
	}

	appliedCount, rejection, err := d.applyMergeConfig(lines)
	if err != nil {
		return diff, reverseCmds, currentHash, false, err
	}
	if rejection != "" {
		return diff, reverseCmds, currentHash, false, olterr.New(
			olterr.TypeValidation,
			fmt.Sprintf(
				"device rejected the candidate after applying %d of %d lines (%s) -- "+
					"system-view was exited cleanly, no further lines were attempted",
				appliedCount, len(lines), rejection,
			),
		)
	}
	return diff, reverseCmds, currentHash, true, nil
}
