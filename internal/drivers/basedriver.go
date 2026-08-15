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

func (d *BaseDriver) Disconnect() error {
	if d.session == nil {
		return nil
	}
	err := d.session.Close()
	d.session = nil
	return err
}
