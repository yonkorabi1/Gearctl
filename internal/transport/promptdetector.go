package transport

import (
	"regexp"
	"strings"
	"time"

	"github.com/onu-platform/gearctl/internal/olterr"
)

const defaultQuiescenceMs = 300

// commandQuiescenceMs is deliberately longer than defaultQuiescenceMs
// (used only for the initial, output-free prompt capture) -- see
// waitForPrompt's doc comment for why: it's the fallback used when a
// command's output never contains the previously-known prompt at all, and
// a real command's output can legitimately have brief pauses mid-stream
// (e.g. a "show" enumerating many ONUs) that would otherwise look like
// premature quiescence.
const commandQuiescenceMs = 2000

// crlf is the NVT (Network Virtual Terminal) line ending per RFC 854 --
// Telnet defines a line ending as CR LF, not a bare LF. Confirmed live
// against a real OLT (packet capture + a manual telnet session that DID
// complete a full login, unlike this client): sending a bare "\n" got the
// device to echo back what was typed but never advance past it -- it was
// still waiting for the actual line terminator. SSH PTYs are generally
// forgiving of a bare LF, but there's no reason not to send the
// protocol-correct terminator everywhere this shared code is used.
const crlf = "\r\n"

// paginationMarkerPattern matches common "there's more, press a key"
// pagination prompts used across vendors -- e.g. "--More--", "-- More --",
// "(more)", "Press any key to continue", and Junos's own
// "---(more)---"/"---(more 10%)---" (dashes AND parens together, the
// latter with a live progress percentage -- confirmed live against a
// real Juniper vMX, Junos 18.2R1.9: `show version | display json`'s
// output is long enough to paginate, and the unrecognized marker was
// silently swallowed into the "output" as if it were real content, with
// the rest of the response truncated -- the same class of bug
// GetConfig's doc comment describes finding for Cisco). Deliberately
// specific enough to require dashes together with parens for the Junos
// case (not just a bare "(more)"), and NOT a bare trailing "more" (too
// easy to false-positive on genuine output that happens to end with that
// word -- see TestPaginationMarkerPattern's "3 more ONTs configured"/"row
// of real data with more columns than usual" traps). A false negative
// here just means paginated output looks like any other
// quiescence-fallback case (silently truncated at the pause, the
// pre-existing behavior); a false positive costs one harmless extra
// continuation keystroke sent to a device that had nothing more to show,
// which is a no-op.
var paginationMarkerPattern = regexp.MustCompile(
	`(?i)-{2,}\s*more\s*-{2,}|\(\s*more\s*\)|press\s+any\s+key|-{2,}\s*\(\s*more(?:\s+\d+\s*%)?\s*\)\s*-{2,}`,
)

// paginationContinueKeystroke is what advances a paginated CLI to the next
// page -- a bare space with no line ending, matching the traditional
// Unix `more`/`less` and Cisco "--More--" convention (unlike a normal
// command, this isn't a line to submit, just a single keypress).
const paginationContinueKeystroke = " "

// waitForQuiescence accumulates bytes from source (seeded with carry, any
// bytes already read out of the channel by an earlier call but not yet
// consumed -- see waitForPrompt's doc comment for why that carry-forward
// exists) until no new data has arrived for quiescenceMs, or maxWait
// elapses (safety cap for a device that never goes quiet -- e.g. a
// broken/chatty CLI). Returns everything accumulated, carry included.
func waitForQuiescence(source *DataSource, carry string, quiescence, maxWait time.Duration) string {
	var buffer strings.Builder
	buffer.WriteString(carry)
	quietTimer := time.NewTimer(quiescence)
	defer quietTimer.Stop()
	maxTimer := time.NewTimer(maxWait)
	defer maxTimer.Stop()

	for {
		select {
		case chunk, ok := <-source.Chunks:
			if !ok {
				return buffer.String()
			}
			buffer.Write(chunk)
			if !quietTimer.Stop() {
				select {
				case <-quietTimer.C:
				default:
				}
			}
			quietTimer.Reset(quiescence)
		case <-quietTimer.C:
			return buffer.String()
		case <-maxTimer.C:
			return buffer.String()
		}
	}
}

// capturePrompt sends a bare newline, waits for quiescence, and treats the
// last non-empty line of whatever came back as the shell's idle prompt.
// carry seeds the buffer with any bytes an earlier call already read out
// of the channel but didn't consume (e.g. leftover from an "enable"
// exchange right before a password prompt) -- pass "" if there's nothing
// to carry in, such as the very first call right after connecting.
// capturePrompt always fully drains until genuinely quiet, so it never
// has anything of its own left over to hand back as an outgoing carry.
// Trimmed of surrounding whitespace only -- deliberately NOT regex-escaped/
// interpreted here; matching in waitForPrompt below is a plain substring
// check.
func capturePrompt(source *DataSource, carry string, maxWaitMs int) (string, error) {
	if err := source.Write(crlf); err != nil {
		return "", olterr.New(olterr.TypeConnection, "failed to write to session: "+err.Error())
	}
	settled := waitForQuiescence(source, carry, defaultQuiescenceMs*time.Millisecond, time.Duration(maxWaitMs)*time.Millisecond)

	prompt := lastPromptLikeLine(settled)
	if prompt == "" {
		return "", olterr.New(
			olterr.TypeProtocol,
			"Could not capture a shell prompt -- the device produced no output after connecting",
		)
	}
	return prompt, nil
}

// lastNonEmptyLine returns the last non-blank line of text, trimmed of
// surrounding whitespace only -- deliberately NOT regex-escaped/
// interpreted; matching elsewhere in this file is a plain substring check.
func lastNonEmptyLine(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	last := ""
	for _, l := range lines {
		if trimmed := strings.TrimSpace(l); trimmed != "" {
			last = trimmed
		}
	}
	return last
}

// asyncLogLinePattern matches unsolicited system/log messages some devices
// push to the console asynchronously -- confirmed live against a real OLT
// (OLT-WRK-LTP-01): it sends lines like "[1970/01/31 13:23:36]
// [system]Warning: Login locally, please check the AAA settings" some time
// AFTER the real shell prompt already appeared, landing in the same
// quiescence window a naive "last non-blank line is the prompt" heuristic
// relies on -- so the warning got captured as "the prompt" instead of the
// real one, silently breaking everything downstream that depends on it
// (enable-escalation's ">"-suffix check, command completion matching).
// Bracketed-timestamp-prefixed console messages are a common convention
// well beyond this one vendor, so filtering them out when hunting for the
// real prompt is generically useful hardening, not a vendor-specific hack.
var asyncLogLinePattern = regexp.MustCompile(`^\[[^\]]*\]`)

// lastPromptLikeLine returns the last non-blank line of text that doesn't
// look like an unsolicited async log message (see asyncLogLinePattern).
// Falls back to the literal last non-blank line if every line looks like
// noise -- better to guess wrong than return nothing at all.
func lastPromptLikeLine(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	lastAny := ""
	lastPromptLike := ""
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		lastAny = trimmed
		if !asyncLogLinePattern.MatchString(trimmed) {
			lastPromptLike = trimmed
		}
	}
	if lastPromptLike != "" {
		return lastPromptLike
	}
	return lastAny
}

// findEchoEnd returns the index just after command's first occurrence in
// buffer[searchFrom:], in absolute buffer coordinates -- or -1 if not
// found. searchFrom is always the position where THIS call's own newly-
// arrived bytes begin (i.e. never inside carry, bytes an earlier call
// already read but didn't consume) -- so stale residual data can never be
// mistaken for this command's own echo. Only bytes that arrived after we
// actually wrote this specific command can be.
func findEchoEnd(buffer, command string, searchFrom int) int {
	if searchFrom > len(buffer) {
		return -1
	}
	idx := strings.Index(buffer[searchFrom:], command)
	if idx == -1 {
		return -1
	}
	return searchFrom + idx + len(command)
}

// cleanOutput trims the one leading and one trailing line-ending artifact
// immediately around real command output, and strips any pagination-
// marker lines a paginated response left in the middle of the text (see
// waitForPrompt's pagination handling -- accumulated across page
// continuations, a marker line sits at each page boundary, not just at
// the very end).
func cleanOutput(text string) string {
	text = strings.TrimPrefix(text, "\r\n")
	text = strings.TrimPrefix(text, "\n")
	text = strings.TrimSuffix(text, "\r\n")
	text = strings.TrimSuffix(text, "\n")
	return stripPaginationMarkerLines(text)
}

// stripTrailingPromptOccurrence removes prompt from the end of text, and
// keeps removing while what remains (after discarding only trailing
// line-ending whitespace) still ends with prompt too. Used only by
// waitForPrompt's quiescence-fallback path (see below) -- the substring-
// match path never needs this, since it locates the prompt by exact
// index and simply slices around it, with anything genuinely left over
// afterward carried forward to the next call rather than needing to be
// found-and-stripped here. The fallback path has no such carry-forward
// boundary (by the time it fires, the device has gone fully quiet for
// commandQuiescenceMs -- there's nothing further to carry), so a device
// that redraws its own prompt twice in the same settled burst needs this
// repeat-collapsing to avoid leaving the inner occurrence embedded in the
// returned text as if it were real content.
func stripTrailingPromptOccurrence(text, prompt string) string {
	for {
		idx := strings.LastIndex(text, prompt)
		if idx == -1 {
			return text
		}
		text = text[:idx]
		trimmed := strings.TrimRight(text, "\r\n")
		if !strings.HasSuffix(trimmed, prompt) {
			return text
		}
		text = trimmed
	}
}

// waitForPrompt sends `command`, then accumulates output until the
// buffer (past this command's own echo) contains `promptText`, or the
// output goes quiet for commandQuiescenceMs without ever matching it, or
// the overall timeout elapses. On success, returns the output, the
// prompt to use for the NEXT command -- normally just promptText again,
// but re-derived from the settled output's own trailing line when the
// quiescence fallback fired instead of a substring match -- and newCarry:
// any bytes read past the matched prompt in the SAME read that produced
// the match, to be threaded into the next call's `carry` parameter.
//
// carry seeds this call's buffer with whatever the PREVIOUS call
// returned as its own newCarry -- pass "" for the first command on a
// fresh session. Bytes are only ever consumed from a channel once, so
// anything read but not returned as output or carry is gone for good;
// threading carry through is what keeps that from happening.
//
// Echo-gating + carry-forward (ported from Netmiko's read-buffer
// approach, itself refined over years of exactly this class of bug) is
// what this replaced: a real-hardware finding against a live Cisco ISR
// showed a command with essentially no real output (e.g. "terminal
// length 0") followed by a few stray trailing bytes (a duplicate prompt
// redraw was the observed case) landing in the SAME read as its own
// completion. An earlier fix drained and discarded those bytes with an
// explicit ~300ms wait after every single command -- correct, but a real
// per-command latency tax with no principled way to shrink it further.
// This version has none of that: findEchoEnd locates THIS command's own
// echo only within bytes that arrived after it was written (never inside
// carry, so stale residue can never be mistaken for it), the prompt is
// searched for only after that point, and anything past the match is
// returned as newCarry rather than waited-out-then-thrown-away. Zero
// added latency in the common case, and nothing is ever silently
// dropped. See TestWaitForPromptCarriesResidualBytesToNextCall and
// TestStreamShellSessionCarriesResidualBytesBetweenCommands.
//
// Trade-off accepted deliberately (matching Netmiko's own re.search
// semantics, not a regression introduced here): the FIRST occurrence of
// promptText after the echo is treated as completion, not the last. If a
// command's real output happened to legitimately contain the exact
// prompt substring mid-stream before actually finishing, this would cut
// early -- an extremely unlikely, never-observed-live shape for a real
// prompt string to appear in real output, and not a case a fast, no-wait
// design can distinguish from genuine completion without adding back the
// exact latency this change removes.
//
// The quiescence fallback matters because this driver has no vendor
// knowledge of privilege/mode levels: confirmed live against a real OLT,
// entering config mode changes the prompt itself (e.g. "OLT>" ->
// "OLT(config)#"), so the previously-captured prompt never reappears and
// a pure substring match would time out on every mode-changing command
// even though the device responded fine. Deliberately a much longer
// quiescence window than the initial connect-time prompt capture
// (commandQuiescenceMs vs. defaultQuiescenceMs) -- a real command's
// output can have brief pauses mid-stream (e.g. a "show" enumerating many
// ONUs) that would otherwise look like premature completion.
//
// isComplete, if non-nil, gates ONLY the quiescence-fallback conclusion
// (not the substring-match path, which is genuine prompt detection and
// needs no such gate): when the quiet timer fires, if isComplete(settled)
// returns false, this resets the quiet timer and keeps waiting instead
// of concluding the device is done -- for a caller that knows its
// command's output has a checkable "is this actually complete" shape
// (e.g. a `| display json` command, where valid/complete JSON is a
// cheap, reliable check) and has hit a real device-side pause exceeding
// commandQuiescenceMs well before the response is actually finished.
// nil preserves the exact prior behavior (conclude immediately once
// nothing arrives for commandQuiescenceMs) -- every existing caller
// passes nil and is unaffected. Bounded the same way regardless: the
// overall per-command `timeout` still fires via timer.C no matter how
// many times the quiescence check is deferred.
//
// On timeout, returns an olterr.OltCliError with Type "timeout" -- the
// caller MUST treat the session as unusable afterward (see ShellSession's
// contract).
func waitForPrompt(
	source *DataSource, command, promptText, carry string, timeout time.Duration,
	isComplete func(settled string) bool,
) (output string, nextPrompt string, newCarry string, err error) {
	if err := source.Write(command + crlf); err != nil {
		return "", "", "", olterr.New(olterr.TypeConnection, "failed to write to session: "+err.Error())
	}

	var buffer strings.Builder
	buffer.WriteString(carry)
	newDataStart := len(carry)
	echoEnd := -1

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	quietTimer := time.NewTimer(commandQuiescenceMs * time.Millisecond)
	defer quietTimer.Stop()

	for {
		select {
		case chunk, ok := <-source.Chunks:
			if !ok {
				return "", "", "", olterr.New(olterr.TypeProtocol, "session closed while waiting for prompt after \""+command+"\"")
			}
			buffer.Write(chunk)
			full := buffer.String()

			if echoEnd == -1 {
				echoEnd = findEchoEnd(full, command, newDataStart)
			}
			if echoEnd != -1 {
				if idx := strings.Index(full[echoEnd:], promptText); idx != -1 {
					matchStart := echoEnd + idx
					matchEnd := matchStart + len(promptText)
					return cleanOutput(full[echoEnd:matchStart]), promptText, full[matchEnd:], nil
				}
			}

			if !quietTimer.Stop() {
				select {
				case <-quietTimer.C:
				default:
				}
			}
			quietTimer.Reset(commandQuiescenceMs * time.Millisecond)
		case <-quietTimer.C:
			full := buffer.String()
			settledFrom := newDataStart
			if echoEnd != -1 {
				settledFrom = echoEnd
			}
			settled := full[settledFrom:]
			if settled == "" {
				// Nothing at all has arrived yet -- not truly quiet, the
				// device just hasn't started responding. Keep waiting for
				// the real timeout rather than finishing early on nothing.
				quietTimer.Reset(commandQuiescenceMs * time.Millisecond)
				continue
			}
			if paginationMarkerPattern.MatchString(lastNonEmptyLine(settled)) {
				// Not command completion -- the device is paused waiting
				// for a keypress to show the next page. Confirmed live:
				// without this, a long "show" enumerating many ONUs got
				// silently truncated at the first page, with the
				// pagination marker itself mistaken for (and stripped
				// away as) a new prompt -- the operator just saw a
				// partial table with no indication anything was missing.
				if err := source.Write(paginationContinueKeystroke); err != nil {
					return "", "", "", olterr.New(olterr.TypeConnection, "failed to write pagination continuation: "+err.Error())
				}
				if !quietTimer.Stop() {
					select {
					case <-quietTimer.C:
					default:
					}
				}
				quietTimer.Reset(commandQuiescenceMs * time.Millisecond)
				continue
			}
			if isComplete != nil && !isComplete(settled) {
				// The caller can tell this isn't actually finished yet,
				// despite commandQuiescenceMs of silence -- a real,
				// live-reproduced device behavior (see this parameter's
				// own doc comment): keep waiting rather than concluding
				// completion. Bounded by the overall per-command
				// `timeout`, same backstop as everything else here.
				quietTimer.Reset(commandQuiescenceMs * time.Millisecond)
				continue
			}
			newPrompt := lastPromptLikeLine(settled)
			if newPrompt == "" {
				newPrompt = promptText
			}
			// No outgoing carry here (contrast the substring-match branch
			// above): reaching this branch already required
			// commandQuiescenceMs (2s) of silence with nothing further
			// arriving -- a much stronger "the device is truly done"
			// signal than an instant substring match, so there's nothing
			// left to carry forward by construction.
			return cleanOutput(stripTrailingPromptOccurrence(settled, newPrompt)), newPrompt, "", nil
		case <-timer.C:
			return "", "", "", olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt after \""+command+"\"")
		}
	}
}

// stripPaginationMarkerLines removes any line matching
// paginationMarkerPattern. Output accumulated across one or more
// pagination continuations (see waitForPrompt) has one of these per page
// boundary, sitting in the MIDDLE of the text, not just at the very end --
// they're page-navigation noise, not part of the device's actual answer.
// stripPaginationMarkerLines drops any \n-delimited "line" containing a
// pagination marker -- except it doesn't just drop it blindly, because a
// real device confirmed this isn't always safe: Junos (a real Juniper
// vMX, Junos 18.2R1.9) erases its "---(more N%)---" marker IN PLACE with
// a bare \r (no \n), then continues real content on what's technically
// the same \n-delimited line, e.g.
//
//	---(more 3%)---\r                                        \r        {\r\n
//
// A naive whole-line drop here silently ate the real "        {" that
// happened to share that line with the marker -- found live via `show
// version | display json` (long enough to paginate), which came back
// with a `{` missing from partway through the JSON, breaking it. Fixed
// by resolving the \r-based overwrite first: keep only what's after the
// LAST \r in the line (the final visual state of a simple
// erase-and-rewrite sequence, the same convention a real terminal would
// display). If real content survives that resolution, keep IT (not the
// raw original line, not nothing); if nothing survives (the common
// Cisco/Huawei case -- just "--More--\r" with nothing after), the line
// is dropped entirely, same as before this fix.
func stripPaginationMarkerLines(text string) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		// Splitting on "\n" alone leaves a trailing "\r" on every line
		// when the original text used "\r\n" endings -- that's just the
		// other half of the CRLF, not part of any \r-based overwrite
		// sequence, so it has to come off BEFORE hunting for the erase
		// sequence's own \r's below (otherwise this trailing remnant is
		// always the "last" \r found, and a real survivor never gets
		// noticed).
		trimmed := strings.TrimSuffix(l, "\r")
		if !paginationMarkerPattern.MatchString(trimmed) {
			kept = append(kept, l)
			continue
		}
		if idx := strings.LastIndex(trimmed, "\r"); idx != -1 {
			if survivor := trimmed[idx+1:]; survivor != "" && !paginationMarkerPattern.MatchString(survivor) {
				kept = append(kept, survivor)
			}
		}
	}
	return strings.Join(kept, "\n")
}
