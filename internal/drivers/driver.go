// Package drivers implements the NAPALM-like abstraction seam. Every
// vendor driver (Phase 2+) implements this same contract; the HTTP route,
// transport layer, and platform-api integration never need to change when
// a new driver is added -- only Resolve (registry.go) gains a case.
package drivers

import (
	"github.com/onu-platform/olt-cli-gateway/internal/transport"
)

// CliDriver is the interface every vendor driver implements. Phase 1 ships
// exactly one driver, RawDriver ("*"), which implements only Connect/
// RunCommands/Disconnect with zero vendor knowledge: connect, run whatever
// commands the caller asked for, disconnect.
//
// Later phases add optional capabilities via type assertions rather than
// interface methods every driver must stub out:
//   - FactsProvider: Phase 2 -- vendor drivers parse `display`/`show`
//     output into structured JSON (interface status, ONU list, optical
//     levels).
//   - ConfigProvider: Phase 3 -- config backup/diff/push, with a
//     candidate/diff/commit/rollback flow gated behind explicit approval.
type CliDriver interface {
	// Vendor identifier this driver claims to handle, or "*" for the
	// generic/raw driver.
	Vendor() string

	Connect(params transport.ConnectionParams) error
	RunCommands(commands []string) ([]transport.CommandResult, error)
	Disconnect() error
}

// FactsProvider is the Phase 2 seam -- not implemented by RawDriver.
type FactsProvider interface {
	GetFacts() (map[string]interface{}, error)
}

// ConfigProvider is the Phase 3 seam -- not implemented by RawDriver.
type ConfigProvider interface {
	GetConfig() (string, error)
	LoadConfig(candidate string, dryRun bool) (diff string, applied bool, err error)
}
