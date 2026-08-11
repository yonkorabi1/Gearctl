package transport

import (
	"strings"
	"time"

	"github.com/onu-platform/olt-cli-gateway/internal/olterr"
)

const defaultQuiescenceMs = 300

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
	if err := source.Write("\n"); err != nil {
		return "", olterr.New(olterr.TypeConnection, "failed to write to session: "+err.Error())
	}
	settled := waitForQuiescence(source, defaultQuiescenceMs*time.Millisecond, time.Duration(maxWaitMs)*time.Millisecond)

	lines := strings.Split(strings.ReplaceAll(settled, "\r\n", "\n"), "\n")
	prompt := ""
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			prompt = trimmed
		}
	}
	if prompt == "" {
		return "", olterr.New(
			olterr.TypeProtocol,
			"Could not capture a shell prompt -- the device produced no output after connecting",
		)
	}
	return prompt, nil
}

// waitForPrompt sends `command`, then accumulates output until the buffer's
// tail contains `promptText` (the previously captured prompt) or timeout
// elapses. On success, returns the output with the leading echoed command
// line and the trailing prompt occurrence stripped. On timeout, returns an
// olterr.OltCliError with Type "timeout" -- the caller MUST treat the
// session as unusable afterward (see ShellSession's contract).
func waitForPrompt(source *DataSource, command, promptText string, timeout time.Duration) (string, error) {
	if err := source.Write(command + "\n"); err != nil {
		return "", olterr.New(olterr.TypeConnection, "failed to write to session: "+err.Error())
	}

	var buffer strings.Builder
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case chunk, ok := <-source.Chunks:
			if !ok {
				return "", olterr.New(olterr.TypeProtocol, "session closed while waiting for prompt after \""+command+"\"")
			}
			buffer.Write(chunk)
			if strings.Contains(buffer.String(), promptText) {
				return stripEchoAndPrompt(buffer.String(), command, promptText), nil
			}
		case <-timer.C:
			return "", olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt after \""+command+"\"")
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
