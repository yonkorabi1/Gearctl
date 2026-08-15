// Package drivers implements the NAPALM-like abstraction seam. Every
// vendor driver (Phase 2+) implements this same contract; the HTTP route,
// transport layer, and platform-api integration never need to change when
// a new driver is added -- only Resolve (registry.go) gains a case.
package drivers

import (
	"github.com/onu-platform/gearctl/internal/transport"
)

// CliDriver is the interface every vendor driver implements. RawDriver
// ("*") implements only Connect/RunCommands/Disconnect with zero vendor
// knowledge: connect, run whatever commands the caller asked for,
// disconnect -- Resolve's fallback for any vendor without a dedicated
// driver. CiscoIOSDriver ("cisco", see ciscoiosdriver.go) is the first
// vendor-specific driver.
//
// Drivers add optional capabilities via type assertions rather than
// interface methods every driver must stub out:
//   - FactsProvider: vendor drivers parse `display`/`show` output into
//     structured JSON (interface status, ONU list, optical levels).
//     CiscoIOSDriver implements this today; more vendors add it as they're
//     built.
//   - InterfaceProvider: richer per-interface state (up/down, enabled,
//     description, MTU, speed, MAC) than FactsProvider's bare
//     interface_list. CiscoIOSDriver implements this today.
//   - LLDPNeighborsProvider: topology discovery -- which local port sees
//     which remote device/port over LLDP. CiscoIOSDriver implements this
//     today.
//   - ConfigGetter: fetch a device's running configuration verbatim (no
//     parsing). CiscoIOSDriver implements this today.
//   - ConfigLoader: config push (a merge/overlay onto the existing
//     config, NOT an atomic replace -- see CiscoIOSDriver.LoadConfig's
//     doc comment), gated behind a required dry-run-first, hash-checked
//     apply flow. Deliberately higher-risk than ConfigGetter and kept as
//     a separate interface so a driver can support reading config without
//     being forced to stub out writing it. CiscoIOSDriver implements this
//     today; user-facing surfaces call it "merge config", not "load
//     config", to avoid implying replace semantics it doesn't have.
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

// InterfaceInfo is one interface's structured state -- the same job
// NAPALM's get_interfaces() getter does, a deliberately small subset of
// its full field set (is_up/is_enabled/description/mtu/speed/mac_address
// -- NAPALM also has last_flapped, which most of its own drivers return
// as an unavailable sentinel too, since classic Cisco IOS doesn't cleanly
// expose "time since this interface last changed state" in a reliably
// parseable way; left out here rather than faked). IPAddress is extra --
// not part of NAPALM's get_interfaces() at all (NAPALM keeps that in a
// separate getter, get_interfaces_ip()) -- added here instead of as its
// own capability since it's cheap to parse from the same `show
// interfaces` output already being read, and "what's this interface's
// IP" is too basic a fact to make a caller fetch it separately. Empty
// string if the interface has no IP configured (e.g. Cisco's own "no ip
// address"), same convention as Description.
type InterfaceInfo struct {
	IsUp        bool    `json:"isUp"`
	IsEnabled   bool    `json:"isEnabled"`
	Description string  `json:"description"`
	IPAddress   string  `json:"ipAddress"`
	MTU         int     `json:"mtu"`
	SpeedMbps   float64 `json:"speedMbps"`
	MACAddress  string  `json:"macAddress"`
}

// InterfaceProvider is a richer sibling of FactsProvider's bare
// interface_list -- per-interface health/state, not implemented by
// RawDriver. Read-only, same as FactsProvider/ConfigGetter -- no new
// device prerequisites.
type InterfaceProvider interface {
	GetInterfaces() (map[string]InterfaceInfo, error)
}

// LLDPNeighbor is one remote device/port an LLDP-speaking local
// interface sees -- the same job NAPALM's get_lldp_neighbors() getter
// does (its detailed sibling, get_lldp_neighbors_detail(), returns much
// more per neighbor -- chassis ID, capabilities, management addresses --
// deliberately not built here; this is the simple "who's plugged into
// what" shape, not full LLDP-TLV detail).
type LLDPNeighbor struct {
	RemoteSystemName string `json:"remoteSystemName"`
	RemotePortID     string `json:"remotePortId"`
}

// LLDPNeighborsProvider is topology-discovery -- not implemented by
// RawDriver. Read-only, no new device prerequisites beyond LLDP itself
// being enabled on the device (a common default, but not universal --
// see CiscoIOSDriver.GetLLDPNeighbors's doc comment). Keyed by LOCAL
// interface name; a slice value (not a single neighbor) because one
// local port can in principle see more than one LLDP neighbor (e.g. a
// hub or unmanaged switch in between), even though that's rare.
type LLDPNeighborsProvider interface {
	GetLLDPNeighbors() (map[string][]LLDPNeighbor, error)
}

// ConfigGetter is the read half of the Phase 3 config seam -- not
// implemented by RawDriver.
type ConfigGetter interface {
	GetConfig() (string, error)
}

// ConfigLoader is the write half of the Phase 3 config seam.
//
// Call with dryRun=true first: candidate/expectedConfigHash are otherwise
// unused, and the returned configHash is a fingerprint of the device's
// CURRENT config at the moment of the call. No device write happens.
//
// Call with dryRun=false to actually apply: expectedConfigHash MUST be the
// configHash a prior dryRun=true call just returned. LoadConfig
// re-fetches the live config and refuses to apply (applied=false, a
// descriptive err) if it no longer matches -- the device changed since
// the dry-run this hash came from, so the diff the caller reviewed may no
// longer be accurate. This is enforced here, in the interface contract,
// not left to each driver to remember.
//
// LoadMergeCommands is LoadConfig's delta-based sibling: candidate is a
// full config (typically a prior GetConfig() output, edited); commands is
// just the handful of lines actually being changed. Same dry-run/
// hash-freshness/apply contract, but the diff shown is a projected
// before/after preview built by reconciling commands against the current
// config section-by-section (see CiscoIOSDriver.LoadMergeCommands's doc
// comment for the heuristic and its explicit limits) -- not a literal
// text diff of the input, since diffing a small delta against the full
// current config directly would be meaningless noise.
//
// reverseCommands is the commands that would undo this merge, derived
// from the same per-command reconciliation the projection already does
// (see CiscoIOSDriver.reverseCommands). It's the vendor-agnostic building
// block for a caller wanting "commit confirmed" semantics -- apply, start
// its own timer, and if not explicitly confirmed in time, call
// LoadMergeCommands again with these commands to revert -- without
// needing any Cisco-specific device feature (archive/SCP) the way
// Cisco's own native `configure revert timer` does. gearctl stays
// stateless; the caller owns the timer. LoadConfig has no equivalent --
// it diffs a full candidate as one opaque text blob, with no per-command
// operation log to invert from, so it always leaves this nil.
type ConfigLoader interface {
	LoadConfig(candidate string, dryRun bool, expectedConfigHash string) (diff string, configHash string, applied bool, err error)
	LoadMergeCommands(commands []string, dryRun bool, expectedConfigHash string) (diff string, reverseCommands []string, configHash string, applied bool, err error)
}
