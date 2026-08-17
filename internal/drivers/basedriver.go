package drivers

import (
	"time"

	"github.com/onu-platform/gearctl/internal/olterr"
	"github.com/onu-platform/gearctl/internal/transport"
)

// BaseDriver holds the session-lifecycle boilerplate every CliDriver needs:
// open a transport session, run commands against it with fail-fast-on-
// timeout ordering, tear it down. Vendor-specific drivers embed BaseDriver
// instead of copy-pasting this -- Go's answer to NAPALM subclassing a
// shared NetworkDriver base class. A vendor driver only adds what's
// actually vendor-specific: Vendor(), and any optional capability
// (FactsProvider, ConfigGetter, ConfigLoader) it chooses to implement.
type BaseDriver struct {
	session transport.ShellSession
	params  transport.ConnectionParams
}

func (d *BaseDriver) Connect(params transport.ConnectionParams) error {
	d.params = params
	var session transport.ShellSession
	var err error
	if params.Protocol == "ssh" {
		session, err = transport.OpenSSHSession(params)
	} else {
		session, err = transport.OpenTelnetSession(params)
	}
	if err != nil {
		return err
	}
	d.session = session
	return nil
}

func (d *BaseDriver) RunCommands(commands []string) ([]transport.CommandResult, error) {
	if d.session == nil {
		return nil, olterr.New(olterr.TypeProtocol, "RunCommands() called before Connect()")
	}

	results := make([]transport.CommandResult, 0, len(commands))

	for _, command := range commands {
		startedAt := time.Now()
		output, truncated, err := d.session.SendAndWaitForPrompt(
			command,
			time.Duration(d.params.PerCommandTimeoutMs)*time.Millisecond,
		)
		if err != nil {
			// Fail-fast: a timed-out session is in an indeterminate state
			// (see ShellSession's contract) -- record this command as
			// failed and stop attempting the remaining ones rather than
			// risk sending more commands into a session that may still be
			// mid-response.
			errClass := "session_closed"
			if cliErr, ok := err.(*olterr.OltCliError); ok && cliErr.ErrType == olterr.TypeTimeout {
				errClass = "timeout"
			}
			results = append(results, transport.CommandResult{
				Command:    command,
				Output:     nil,
				DurationMs: time.Since(startedAt).Milliseconds(),
				Truncated:  false,
				Error:      errClass,
			})
			break
		}

		out := output
		results = append(results, transport.CommandResult{
			Command:    command,
			Output:     &out,
			DurationMs: time.Since(startedAt).Milliseconds(),
			Truncated:  truncated,
		})
	}

	return results, nil
}

// CurrentPrompt returns the session's own last-known prompt text, or ""
// if Connect hasn't been called yet. Exists so a driver can build a
// SendAndWaitForPromptHint prediction (via RunCommandWithHint below) from
// something real -- see ShellSession.CurrentPrompt's doc comment.
func (d *BaseDriver) CurrentPrompt() string {
	if d.session == nil {
		return ""
	}
	return d.session.CurrentPrompt()
}

// RunCommandWithHint is RunCommands for exactly one command, but lets the
// caller supply expectedPrompt -- see
// ShellSession.SendAndWaitForPromptHint's doc comment for why: a command
// known to change the device's prompt (Cisco's "configure terminal"/
// "interface X"/"end", say) would otherwise pay a guaranteed
// commandQuiescenceMs fallback wait every time, since the session's own
// last-known prompt is guaranteed stale the instant such a command is
// sent. A wrong guess is never unsafe, just not faster -- same fallback
// self-correction as passing no hint at all.
//
// Same fail-fast-on-timeout error classing as RunCommands, just for one
// command instead of a list -- used directly by CiscoIOSDriver's
// applyMergeConfig, which already runs one command at a time for its own
// reasons (see its doc comment) and is the one caller in this codebase
// that both knows a vendor's prompt grammar well enough to predict with,
// and cares about the resulting speedup.
func (d *BaseDriver) RunCommandWithHint(command, expectedPrompt string) (transport.CommandResult, error) {
	if d.session == nil {
		return transport.CommandResult{}, olterr.New(olterr.TypeProtocol, "RunCommandWithHint() called before Connect()")
	}

	startedAt := time.Now()
	output, truncated, err := d.session.SendAndWaitForPromptHint(
		command,
		time.Duration(d.params.PerCommandTimeoutMs)*time.Millisecond,
		expectedPrompt,
	)
	if err != nil {
		errClass := "session_closed"
		if cliErr, ok := err.(*olterr.OltCliError); ok && cliErr.ErrType == olterr.TypeTimeout {
			errClass = "timeout"
		}
		return transport.CommandResult{
			Command:    command,
			Output:     nil,
			DurationMs: time.Since(startedAt).Milliseconds(),
			Truncated:  false,
			Error:      errClass,
		}, nil
	}

	out := output
	return transport.CommandResult{
		Command:    command,
		Output:     &out,
		DurationMs: time.Since(startedAt).Milliseconds(),
		Truncated:  truncated,
	}, nil
}

// RunCommandUntilComplete is RunCommandWithHint (no prompt hint -- this
// isn't a mode-changing command, just a possibly-slow one), extended
// with isComplete: if the underlying session implements
// transport.CompletionAwareSession, waitForPrompt's quiescence-fallback
// wait keeps extending itself (bounded by the overall per-command
// timeout, same backstop as always) as long as isComplete(settled)
// reports the accumulated output isn't finished yet, instead of
// concluding completion the instant nothing new arrives for the
// ordinary quiescence window.
//
// This exists for a real, live-reproduced device behavior: a large `|
// display json` response on a loaded Juniper vMX can pause
// mid-transmission longer than the ordinary quiescence window without
// actually being done -- see JuniperDriver.runJSONCommand's doc comment
// for the full story and how json.Valid is used as isComplete there.
//
// Falls back to plain RunCommandWithHint (no completeness awareness) if
// the session doesn't implement transport.CompletionAwareSession -- e.g.
// a test double that has no need to model this; every OTHER existing
// caller/session is completely unaffected either way.
func (d *BaseDriver) RunCommandUntilComplete(command string, isComplete func(settled string) bool) (transport.CommandResult, error) {
	if d.session == nil {
		return transport.CommandResult{}, olterr.New(olterr.TypeProtocol, "RunCommandUntilComplete() called before Connect()")
	}

	cas, ok := d.session.(transport.CompletionAwareSession)
	if !ok {
		return d.RunCommandWithHint(command, "")
	}

	startedAt := time.Now()
	output, truncated, err := cas.SendAndWaitForPromptUntilComplete(
		command,
		time.Duration(d.params.PerCommandTimeoutMs)*time.Millisecond,
		"",
		isComplete,
	)
	if err != nil {
		errClass := "session_closed"
		if cliErr, ok := err.(*olterr.OltCliError); ok && cliErr.ErrType == olterr.TypeTimeout {
			errClass = "timeout"
		}
		return transport.CommandResult{
			Command:    command,
			Output:     nil,
			DurationMs: time.Since(startedAt).Milliseconds(),
			Truncated:  false,
			Error:      errClass,
		}, nil
	}

	out := output
	return transport.CommandResult{
		Command:    command,
		Output:     &out,
		DurationMs: time.Since(startedAt).Milliseconds(),
		Truncated:  truncated,
	}, nil
}

func (d *BaseDriver) Disconnect() error {
	if d.session == nil {
		return nil
	}
	err := d.session.Close()
	d.session = nil
	return err
}
