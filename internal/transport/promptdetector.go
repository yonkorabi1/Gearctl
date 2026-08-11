package transport

import (
	"strings"
	"time"

	"github.com/onu-platform/olt-cli-gateway/internal/olterr"
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

// waitForQuiescence accumulates bytes from source until no new data has
// arrived for quiescenceMs, or maxWait elapses (safety cap for a device
// that never goes quiet -- e.g. a broken/chatty CLI). Returns everything
// accumulated.
func waitForQuiescence(source *DataSource, quiescence, maxWait time.Duration) string {
	var buffer strings.Builder
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
// Trimmed of surrounding whitespace only -- deliberately NOT regex-escaped/
// interpreted here; matching in waitForPrompt below is a plain substring
// check.
func capturePrompt(source *DataSource, maxWaitMs int) (string, error) {
	if err := source.Write(crlf); err != nil {
		return "", olterr.New(olterr.TypeConnection, "failed to write to session: "+err.Error())
	}
	settled := waitForQuiescence(source, defaultQuiescenceMs*time.Millisecond, time.Duration(maxWaitMs)*time.Millisecond)

	prompt := lastNonEmptyLine(settled)
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

// waitForPrompt sends `command`, then accumulates output until the buffer's
// tail contains `promptText` (the previously captured prompt), or the
// output goes quiet for commandQuiescenceMs without ever matching it, or
// the overall timeout elapses. On success, returns the output (leading
// echoed command line and trailing prompt occurrence stripped) plus the
// prompt to use for the NEXT command -- normally just promptText again,
// but re-derived from the settled output's own trailing line when the
// quiescence fallback fired instead of a substring match.
//
// The fallback matters because this driver has no vendor knowledge of
// privilege/mode levels: confirmed live against a real OLT, entering
// config mode changes the prompt itself (e.g. "OLT>" -> "OLT(config)#"),
// so the previously-captured prompt never reappears and a pure substring
// match would time out on every mode-changing command even though the
// device responded fine. Deliberately a much longer quiescence window
// than the initial connect-time prompt capture (commandQuiescenceMs vs.
// defaultQuiescenceMs) -- a real command's output can have brief pauses
// mid-stream (e.g. a "show" enumerating many ONUs) that would otherwise
// look like premature completion.
//
// On timeout, returns an olterr.OltCliError with Type "timeout" -- the
// caller MUST treat the session as unusable afterward (see ShellSession's
// contract).
func waitForPrompt(source *DataSource, command, promptText string, timeout time.Duration) (output string, nextPrompt string, err error) {
	if err := source.Write(command + crlf); err != nil {
		return "", "", olterr.New(olterr.TypeConnection, "failed to write to session: "+err.Error())
	}

	var buffer strings.Builder
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	quietTimer := time.NewTimer(commandQuiescenceMs * time.Millisecond)
	defer quietTimer.Stop()

	for {
		select {
		case chunk, ok := <-source.Chunks:
			if !ok {
				return "", "", olterr.New(olterr.TypeProtocol, "session closed while waiting for prompt after \""+command+"\"")
			}
			buffer.Write(chunk)
			if strings.Contains(buffer.String(), promptText) {
				return stripEchoAndPrompt(buffer.String(), command, promptText), promptText, nil
			}
			if !quietTimer.Stop() {
				select {
				case <-quietTimer.C:
				default:
				}
			}
			quietTimer.Reset(commandQuiescenceMs * time.Millisecond)
		case <-quietTimer.C:
			settled := buffer.String()
			if settled == "" {
				// Nothing at all has arrived yet -- not truly quiet, the
				// device just hasn't started responding. Keep waiting for
				// the real timeout rather than finishing early on nothing.
				quietTimer.Reset(commandQuiescenceMs * time.Millisecond)
				continue
			}
			newPrompt := lastNonEmptyLine(settled)
			if newPrompt == "" {
				newPrompt = promptText
			}
			return stripEchoAndPrompt(settled, command, newPrompt), newPrompt, nil
		case <-timer.C:
			return "", "", olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt after \""+command+"\"")
		}
	}
}

// stripEchoAndPrompt: the device echoes the command back as the first line
// (standard for both SSH and Telnet interactive shells), and the response
// ends with the prompt reappearing. Strip both so callers get just the
// command's real output.
func stripEchoAndPrompt(raw, command, promptText string) string {
	text := raw
	if idx := strings.Index(text, command); idx != -1 {
		text = text[idx+len(command):]
	}
	// Strip the trailing prompt (last occurrence, since output could
	// legitimately contain the prompt substring earlier -- unlikely but
	// not impossible for a generic driver with no vendor knowledge).
	if idx := strings.LastIndex(text, promptText); idx != -1 {
		text = text[:idx]
	}
	text = strings.TrimPrefix(text, "\r\n")
	text = strings.TrimPrefix(text, "\n")
	text = strings.TrimSuffix(text, "\r\n")
	text = strings.TrimSuffix(text, "\n")
	return text
}
