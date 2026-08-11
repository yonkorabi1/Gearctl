package transport

import "strings"

// enableStyleVendors are OLT vendor identifiers known to gate privileged
// commands behind a separate "enable"-style escalation step (Cisco IOS and
// its lineage: type "enable", answer a second password, get a "#" prompt
// instead of ">").
//
// Deliberately an explicit allowlist rather than a prompt-shape guess:
// confirmed live that trailing ">" is NOT a reliable signal on its own --
// Juniper's Junos uses ">" for its normal operational-mode prompt too
// (e.g. "user@router>") and has no enable concept at all, and neither does
// Huawei's VRP-derived CLIs. Guessing from the prompt alone would send a
// speculative "enable" (and, if the device happens to ask for a password
// anyway, an unhelpful one) to vendors that never needed it -- harmless in
// most cases, but this platform talks to devices that visibly log failed
// login/command attempts (see OLT-WRK-LTP-01's own login banner), so
// keeping this opt-in per vendor is the safer default until proven
// otherwise for a given vendor string.
//
// Add to this list as specific vendors are confirmed (live, against real
// hardware) to need it -- not speculatively.
var enableStyleVendors = map[string]bool{
	"cisco":  true,
	"arista": true,
	// Confirmed live against OLT-WRK-LTP-01: its CLI reports errors as
	// "vty% [VTY] vty[node:N],Unknown command: ..." -- the Quagga/FRRouting
	// VTY shell's exact error format, which has Cisco-style privilege
	// nodes. "config"/"configure" is genuinely unrecognized at the
	// unprivileged node and only becomes valid after "enable" -- and on
	// this device, "enable" succeeded with no password prompt at all
	// (see attemptEnable for why that's handled as a distinct case from
	// "vendor doesn't have this concept").
	"hsgq": true,
}

// isEnableStyleVendor reports whether vendor (the free-text hint carried
// through from the `olts` table) is known to use enable-style escalation.
func isEnableStyleVendor(vendor string) bool {
	return enableStyleVendors[strings.ToLower(strings.TrimSpace(vendor))]
}
