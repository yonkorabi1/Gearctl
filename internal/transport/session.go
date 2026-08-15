package transport

import (
	"log"
	"strings"
	"time"

	"github.com/onu-platform/gearctl/internal/olterr"
)

// ShellSession is the common contract both SSH and Telnet sessions
// implement -- RawDriver (in the drivers package) only depends on this,
// never on the underlying transport directly.
type ShellSession interface {
	// Open completes the prompt-capture step (and, if set, the
	// disablePagingCommand) -- assumes the transport has ALREADY completed
	// connection + login (ssh2-equivalent handles SSH auth natively;
	// TelnetSession runs its own login-prompt state machine before
	// constructing a StreamShellSession).
	Open(params ConnectionParams) error

	// SendAndWaitForPrompt sends a command and waits for the prompt to
	// reappear. Callers MUST NOT reuse the session after an error --
	// its state is indeterminate.
	SendAndWaitForPrompt(command string, timeout time.Duration) (output string, truncated bool, err error)

	// Close tears down the connection. Safe to call multiple times.
	Close() error
}

// StreamShellSession implements ShellSession on top of any DataSource (an
// SSH shell channel or a raw Telnet socket, once each transport has
// finished its own connection/auth handshake). All prompt-detection,
// pagination, and per-command timeout logic lives here ONCE so
// SSHSession/TelnetSession only need to establish the underlying pipe.
type StreamShellSession struct {
	source     *DataSource
	promptText string
	// carry: bytes waitForPrompt/capturePrompt read past the previous
	// call's own match but didn't consume -- see waitForPrompt's doc
	// comment. Threaded into every subsequent call's carry parameter so
	// nothing read from the channel is ever silently dropped.
	carry   string
	onClose func() error
	closed  bool
}

func NewStreamShellSession(source *DataSource, onClose func() error) *StreamShellSession {
	return &StreamShellSession{source: source, onClose: onClose}
}

func (s *StreamShellSession) Open(params ConnectionParams) error {
	// "" -- nothing could possibly be carried in yet, this is the very
	// first read on a fresh session.
	prompt, err := capturePrompt(s.source, "", params.ConnectTimeoutMs)
	if err != nil {
		return err
	}
	s.promptText = prompt
	// s.carry stays "" -- capturePrompt always fully drains until quiet,
	// so it never has anything of its own left over.

	// Enable-style privileged-mode escalation is NOT universal (Huawei and
	// Junos-lineage CLIs have no such concept, and Junos's normal prompt
	// also ends in ">" -- see enablevendors.go for why this is an explicit
	// vendor allowlist, not a prompt-shape guess) -- gated on vendor first,
	// the ">" suffix second. Deliberately NOT also gated on having an
	// enable password configured: confirmed live that some devices accept
	// "enable" with no password at all (see attemptEnable -- it only
	// needs one if the device actually asks for one).
	if isEnableStyleVendor(params.Vendor) && strings.HasSuffix(s.promptText, ">") {
		log.Printf("enable: attempting for vendor=%q prompt=%q", params.Vendor, s.promptText)
		nextPrompt, newCarry, err := attemptEnable(
			s.source,
			s.promptText,
			params.EnablePassword,
			s.carry,
			time.Duration(params.ConnectTimeoutMs)*time.Millisecond,
		)
		if err != nil {
			log.Printf("enable: failed: %v", err)
			return err
		}
		log.Printf("enable: done, prompt now %q", nextPrompt)
		s.promptText = nextPrompt
		s.carry = newCarry
	} else {
		log.Printf("enable: skipped for vendor=%q prompt=%q (vendor allowlisted=%v, prompt ends with '>'=%v)",
			params.Vendor, s.promptText, isEnableStyleVendor(params.Vendor), strings.HasSuffix(s.promptText, ">"))
	}

	if params.DisablePagingCommand != "" {
		// Discard output -- this command's only purpose is to stop the
		// device from blocking on a "--More--" style pager, not to report
		// anything back to the caller. newCarry still matters, though --
		// this is exactly the command that triggered the real-hardware
		// finding motivating carry-forward (see waitForPrompt's doc
		// comment): a low-output command like this one is where a stray
		// trailing prompt redraw is most likely to land.
		_, nextPrompt, newCarry, err := waitForPrompt(
			s.source,
			params.DisablePagingCommand,
			s.promptText,
			s.carry,
			time.Duration(params.PerCommandTimeoutMs)*time.Millisecond,
		)
		if err != nil {
			return err
		}
		s.promptText = nextPrompt
		s.carry = newCarry
	}
	return nil
}

func (s *StreamShellSession) SendAndWaitForPrompt(command string, timeout time.Duration) (string, bool, error) {
	if s.closed {
		return "", false, olterr.New(olterr.TypeProtocol, "Cannot send a command on a closed session")
	}
	output, nextPrompt, newCarry, err := waitForPrompt(s.source, command, s.promptText, s.carry, timeout)
	if err != nil {
		return "", false, err
	}
	// Re-derived every call, not just assumed unchanged -- see
	// waitForPrompt's doc comment: a command can change the prompt itself
	// (e.g. entering a config/privileged mode), and the next command needs
	// to match against whatever the prompt actually is NOW. carry is
	// threaded the same way, for the same reason -- see StreamShellSession's
	// doc comment on the carry field.
	s.promptText = nextPrompt
	s.carry = newCarry
	if len(output) > MaxOutputBytes {
		return truncateUTF8(output, MaxOutputBytes), true, nil
	}
	return output, false, nil
}

// attemptEnable sends the literal command "enable" and inspects what
// comes back:
//
//   - Something password-shaped, AND an enable password is configured ->
//     answer it, then settle on whatever prompt follows (same logic as
//     the initial post-login prompt capture).
//   - Something password-shaped, but NO enable password is configured ->
//     a real, surfaced error. Confirmed live that not every enable-style
//     vendor requires a password at all, so this is attempted
//     unconditionally (see Open()) -- but if THIS device does demand one
//     and we have nothing to give it, the session is now stuck sitting
//     at an unanswered prompt. Continuing anyway would just make every
//     subsequent command fail with a confusing timeout instead.
//   - Anything else -- unrecognized command, or "enable" already
//     succeeded with no password step at all -- a no-op. The resulting
//     prompt is used either way: if the device really did escalate
//     silently (no password needed), its prompt may already have
//     changed, and callers need to track that.
//
// carry/returned newCarry follow waitForPrompt's carry-forward contract
// (see its doc comment) -- if a password step is needed, whatever
// waitForPrompt's own "enable" call left over is fed into the follow-up
// capturePrompt call rather than dropped, since that's a second,
// independent read that would otherwise never see it. If no password
// step is needed, waitForPrompt's own newCarry is returned directly;
// capturePrompt always fully drains, so there's never anything left over
// coming out of the password-step branch.
//
// Callers are expected to have already decided this is worth trying
// (vendor allowlist + prompt shape, see Open()); this function itself has
// no vendor knowledge.
func attemptEnable(
	source *DataSource, promptText, enablePassword, carry string, timeout time.Duration,
) (newPrompt string, newCarry string, err error) {
	output, candidatePrompt, afterEnableCarry, err := waitForPrompt(source, "enable", promptText, carry, timeout)
	if err != nil {
		// A failure here (timeout, session closed) is still a real
		// connection-level problem worth surfacing, same as any other
		// command -- this isn't "enable didn't apply", it's "something
		// went wrong sending it".
		return "", "", err
	}
	log.Printf("enable: device responded with output=%q candidatePrompt=%q", output, candidatePrompt)
	if !passwordPattern.MatchString(candidatePrompt) {
		return candidatePrompt, afterEnableCarry, nil
	}
	if enablePassword == "" {
		return "", "", olterr.New(
			olterr.TypeAuth,
			`Device asked for a password after "enable", but no enable password is configured for this OLT`,
		)
	}
	if err := source.Write(enablePassword + crlf); err != nil {
		return "", "", olterr.New(olterr.TypeConnection, "failed to write enable password: "+err.Error())
	}
	prompt, err := capturePrompt(source, afterEnableCarry, int(timeout/time.Millisecond))
	if err != nil {
		return "", "", err
	}
	return prompt, "", nil
}

func (s *StreamShellSession) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.onClose != nil {
		return s.onClose()
	}
	return nil
}

// truncateUTF8 truncates on a UTF-8 boundary and appends a marker,
// acceptable for a diagnostics-output truncation.
func truncateUTF8(text string, maxBytes int) string {
	b := []byte(text)
	if len(b) <= maxBytes {
		return text
	}
	cut := b[:maxBytes]
	// Back off if we landed mid-rune.
	for len(cut) > 0 && !isUTF8Boundary(cut) {
		cut = cut[:len(cut)-1]
	}
	return string(cut) + "\n...[truncated]"
}

func isUTF8Boundary(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	last := b[len(b)-1]
	// A continuation byte (10xxxxxx) means we're mid-rune.
	return last&0xC0 != 0x80
}
