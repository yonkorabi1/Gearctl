package drivers

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/onu-platform/gearctl/internal/olterr"
)

// ExtremeDriver targets ExtremeXOS (EXOS) -- Extreme Networks' Summit/X-
// series switch OS, NOT VOSS/Fabric Engine (the other Extreme CLI family,
// ex-Avaya/Nortel lineage, which is modal like Cisco IOS and would need its
// own driver). Confirmed live against a real EXOS-VM (ExtremeXOS 32.7.2.19)
// at 192.168.14.16.
//
// EXOS has NO enable/config-terminal modal concept at all -- every command
// (including ones Cisco would gate behind "configure terminal") runs
// directly at the single top-level prompt and takes effect immediately.
// enablevendors.go's escalation logic is therefore irrelevant here; "extreme"
// is deliberately NOT added to its allowlist.
//
// Confirmed live prompt grammar: "<sysname>.<N> #", where N is a
// monotonically increasing per-command counter (increments on EVERY
// command, including rejected/invalid ones -- confirmed by sending an
// invalid command and seeing the counter still advance) -- NOT a stable
// string the way Cisco's/Junos's prompts are between same-mode commands.
// Once any config-mutating command has run, the prompt gains a leading
// "* " that persists (switch-wide, not just this session -- confirmed by
// reconnecting in a fresh session and still seeing it) until "save
// configuration" is run; no command clears it back off without saving.
// This defeats promptdetector.go's fast substring-match path on every
// single command (the previously-captured prompt text is never the same
// string twice), unlike Cisco/Junos where only mode-changing commands
// pay that cost -- see predictExtremePrompt, which restores the fast path
// by predicting the next "<sysname>.<N+1> #" (or "* "-prefixed form once
// dirty) instead of leaving every command to fall back to the full
// commandQuiescenceMs wait.
//
// Confirmed live command-rejection format: a line starting with
// "%% Unrecognized command:" (note: DOUBLE "%%", unlike Cisco's single
// "%") followed by a second "%%"-prefixed line describing the specific
// problem, e.g. "%% Invalid input detected at '^' marker." or
// "%% Invalid port number detected."
//
// Confirmed live pagination: EXOS's pager prompt is
// "Press <SPACE> to continue or <Q> to quit:" wrapped in ANSI reverse-video
// (ESC[7m ... ESC[m ESC[K) -- a different marker from Cisco/Huawei's
// "--More--" and Junos's "---(more N%)---", so
// transport/promptdetector.go's paginationMarkerPattern does NOT recognize
// it today (out of scope for this driver to fix; a caller wanting
// EXOS-specific pagination handled generically would need that pattern
// extended). This driver instead disables paging proactively via
// "disable cli paging" before anything that could produce multi-screen
// output, the same proactive-disable posture CiscoIOSDriver's GetConfig
// takes for "terminal length 0" (see its doc comment) -- confirmed live
// this both suppresses the pager and needs sending only once per session.
//
// ALSO CONFIRMED LIVE, a real hang hazard for any caller: several `show`
// commands with no qualifying suffix -- bare "show ports", "show ports
// info", "show ports configuration" (without "no-refresh") -- open a
// self-refreshing LIVE MONITOR screen (repaints roughly once a second via
// ANSI clear-line/cursor-up codes, with its own "U->page up D->page down
// ESC->exit" footer) rather than printing a static result and returning to
// the prompt. Sent through a promptdetector-style session, this never goes
// quiet, so it looks identical to a hung device. This driver only ever
// sends the static, one-shot variants confirmed NOT to do this: "show
// ports information", "show ports description", "show ports configuration
// no-refresh" was also confirmed static but isn't used here (see
// GetInterfaces' doc comment for why "show ports information" was chosen
// instead). A caller using RunCommands directly for ad-hoc EXOS commands
// should avoid the bare/monitor forms.
//
// ALSO CONFIRMED LIVE: plain "exit" (and, by the same mechanism, "logout")
// triggers an interactive
// "Do you wish to save your configuration changes to primary.cfg? (y/N)"
// prompt whenever unsaved changes are pending -- a session-level hang
// hazard if ever sent as an ordinary RunCommands entry (it blocks waiting
// for a y/N answer that never comes). BaseDriver.Disconnect() never sends
// "exit" -- it closes the underlying transport directly -- so this is only
// a risk for a caller that runs "exit"/"logout" itself; not otherwise
// guarded against here, the same "trust the caller" posture RawDriver and
// every other driver in this package already take for arbitrary commands.
//
// Read-only capabilities only (FactsProvider, InterfaceProvider,
// LLDPNeighborsProvider, ConfigGetter) -- ConfigLoader (config push) is
// deliberately NOT implemented yet. EXOS's config is flat and non-modal
// (see GetConfig's doc comment): CiscoIOSDriver's LoadMergeCommands
// machinery (locateLines/projectMergedConfig/matchKey, see
// ciscoiosdriver.go) is built entirely around Cisco's INDENTED stanza
// grammar and doesn't carry over -- EXOS would need its own reconciliation
// design keyed on each command family's own object identifier (port
// number, VLAN name, ...) rather than indentation, which is real, scoped
// follow-up work, not a small addition. Per driver.go's own convention
// ("a driver implementing one [LoadConfig/LoadMergeCommands] is expected
// to implement both"), deferring both together rather than shipping a
// partial/rushed version of either.
type ExtremeDriver struct {
	BaseDriver
}

func NewExtremeDriver() *ExtremeDriver {
	return &ExtremeDriver{}
}

func (d *ExtremeDriver) Vendor() string {
	return "extreme"
}

// extremePromptPattern matches EXOS's confirmed-live prompt shape:
// optional leading "* " (unsaved changes pending), a hostname, ".", the
// per-command counter, " #". Captures the counter separately so
// predictExtremePrompt can increment it without needing to know the
// hostname's own character set.
var extremePromptPattern = regexp.MustCompile(`^(\* )?(.+)\.(\d+) #\s*$`)

// predictExtremePrompt guesses the prompt EXOS will show after sending
// exactly one more command, given currentPrompt is what it shows right
// now -- see the type doc comment for why EXOS needs this on EVERY
// command, not just mode-changing ones the way Cisco/Junos do.
//
// The counter increments unconditionally (confirmed live: it advanced
// even after a rejected/invalid command), so this always predicts N+1
// regardless of what cmd is. The leading "* " is stickier: once dirty, it
// never clears itself back off short of "save configuration" (confirmed
// live), so the prediction preserves an already-dirty prompt's "* " even
// when cmd is a harmless read-only "show ..." -- but only widens FROM a
// clean prompt to a dirty one when cmd itself looks config-mutating (a
// crude heuristic: HasPrefix on the small set of verbs EXOS config
// commands actually start with -- "configure "/"create "/"delete "/
// "enable "/"disable "/"unconfigure "). A wrong guess either way is never
// unsafe: same as CiscoIOSDriver's predictNextPrompt, it just falls back
// to gearctl's normal commandQuiescenceMs wait exactly as if no hint had
// been given at all.
func predictExtremePrompt(currentPrompt, cmd string) string {
	m := extremePromptPattern.FindStringSubmatch(currentPrompt)
	if m == nil {
		return ""
	}
	alreadyDirty := m[1] != ""
	hostname := m[2]
	counter := m[3]

	nextCounter := counter
	if n, err := strconv.Atoi(counter); err == nil {
		nextCounter = strconv.Itoa(n + 1)
	}

	dirty := alreadyDirty || looksConfigMutating(cmd)
	prefix := ""
	if dirty {
		prefix = "* "
	}
	return prefix + hostname + "." + nextCounter + " #"
}

// extremeMutatingVerbs are the leading verbs EXOS config-changing commands
// are confirmed to start with (configure/create/delete/enable/disable/
// unconfigure ports and VLANs, live-tested). Deliberately a small,
// explicit list rather than "anything that isn't 'show'/'display'" -- a
// false negative here (a mutating command not recognized) just costs the
// usual quiescence wait on that one command, never anything unsafe.
var extremeMutatingVerbs = []string{"configure ", "create ", "delete ", "enable ", "disable ", "unconfigure "}

func looksConfigMutating(cmd string) bool {
	lower := strings.ToLower(strings.TrimSpace(cmd))
	for _, verb := range extremeMutatingVerbs {
		if strings.HasPrefix(lower, verb) {
			return true
		}
	}
	return false
}

// runSingle runs exactly one EXOS command, predicting its resulting prompt
// via predictExtremePrompt so gearctl's fast substring-match path applies
// even though EXOS's prompt text changes on literally every command --
// see the type doc comment and predictExtremePrompt's own doc comment.
func (d *ExtremeDriver) runSingle(command string) (string, bool, string, error) {
	hint := predictExtremePrompt(d.CurrentPrompt(), command)
	result, err := d.RunCommandWithHint(command, hint)
	if err != nil {
		return "", false, "", err
	}
	output := ""
	if result.Output != nil {
		output = *result.Output
	}
	return output, result.Truncated, result.Error, nil
}

// extremeDisablePagingCommand is sent once at the start of every Getter
// below -- see the type doc comment's pagination section for why this is
// proactive rather than left to promptdetector.go's generic (and, for
// EXOS, non-matching) pagination-marker fallback.
const extremeDisablePagingCommand = "disable cli paging"

var extremeVersionPattern = regexp.MustCompile(`(?i)ExtremeXOS version\s+(\S+)`)

// extremeSerialPattern is deliberately anchored to the "Switch" line
// specifically, not a bare `SN:(\S+)` search over the whole "show
// version" output -- confirmed live that an unanchored search would wrongly
// match one of the OTHER "SN:" tokens the same output prints for
// components (PSUCTRL-1/PSUCTRL-2/individual PSUs each have their own,
// e.g. "PSUCTRL-1 : PN:MEAD SN:MD1 Rev 01 ..."), which come BEFORE the
// Switch line's own field in real captured output and are a different
// serial number entirely (a power-supply controller's, not the chassis's).
// The Switch-line format this targets (PN:<part> SN:<serial> Rev ...) is
// Extreme's documented real-hardware shape -- NOT live-confirmed to match
// anything, since this lab's EXOS-VM Switch line has no "SN:" token at
// all ("Switch : PN:1N2039 SIM0012-0000 Rev 01 ..."), which is itself the
// live-confirmed case this correctly leaves serial_number as "" for.
var extremeSerialPattern = regexp.MustCompile(`(?m)^Switch\s*:.*\bSN:(\S+)`)
var extremeSysNamePattern = regexp.MustCompile(`(?m)^SysName:\s*(\S+)`)
var extremeSystemTypePattern = regexp.MustCompile(`(?m)^System Type:\s*(\S+)`)
var extremeUptimePattern = regexp.MustCompile(`(?m)^System UpTime:\s*(.+?)\s*$`)

// GetFacts implements FactsProvider. Runs "disable cli paging", then
// "show version" (image/serial), "show switch" (sysname/model/uptime),
// and "show ports information" (for interface_list) -- confirmed live
// against a real EXOS-VM. model/serial_number are best-effort: this lab
// VM has no real serial ("SN:" never appears in its "show version" Switch
// line, only on real hardware per Extreme's documented format), so
// extremeSerialPattern is confirmed to correctly leave serial_number ""
// here but has NOT been confirmed to match on a real switch with an
// actual serial burned in.
func (d *ExtremeDriver) GetFacts() (map[string]interface{}, error) {
	if _, _, errStr, err := d.runSingle(extremeDisablePagingCommand); err != nil {
		return nil, err
	} else if errStr != "" {
		return nil, olterr.New(olterr.TypeConnection, "GetFacts: \"disable cli paging\" failed: "+errStr)
	}

	versionOutput, _, errStr, err := d.runSingle("show version")
	if err != nil {
		return nil, err
	}
	if errStr != "" {
		return nil, olterr.New(olterr.TypeProtocol, "GetFacts: \"show version\" failed: "+errStr)
	}

	switchOutput, _, errStr, err := d.runSingle("show switch")
	if err != nil {
		return nil, err
	}
	if errStr != "" {
		return nil, olterr.New(olterr.TypeProtocol, "GetFacts: \"show switch\" failed: "+errStr)
	}

	portsOutput, _, errStr, err := d.runSingle("show ports information")
	if err != nil {
		return nil, err
	}
	if errStr != "" {
		return nil, olterr.New(olterr.TypeProtocol, "GetFacts: \"show ports information\" failed: "+errStr)
	}

	facts := map[string]interface{}{
		"vendor":         "Extreme",
		"model":          "",
		"hostname":       "",
		"fqdn":           "",
		"os_version":     "",
		"serial_number":  "",
		"uptime":         "",
		"interface_list": []string{},
	}

	if m := extremeVersionPattern.FindStringSubmatch(versionOutput); m != nil {
		facts["os_version"] = m[1]
	}
	if m := extremeSerialPattern.FindStringSubmatch(versionOutput); m != nil {
		facts["serial_number"] = m[1]
	}
	if m := extremeSysNamePattern.FindStringSubmatch(switchOutput); m != nil {
		facts["hostname"] = m[1]
		facts["fqdn"] = m[1]
	}
	if m := extremeSystemTypePattern.FindStringSubmatch(switchOutput); m != nil {
		facts["model"] = m[1]
	}
	if m := extremeUptimePattern.FindStringSubmatch(switchOutput); m != nil {
		facts["uptime"] = m[1]
	}

	interfaces := parseExtremePortsInformation(portsOutput)
	names := make([]string, 0, len(interfaces))
	for name := range interfaces {
		names = append(names, name)
	}
	sort.Strings(names)
	facts["interface_list"] = names

	return facts, nil
}

// GetConfig implements ConfigGetter: runs "disable cli paging" then "show
// configuration", returning the second command's output verbatim.
//
// IMPORTANT semantic difference from every other driver in this package:
// EXOS's "show configuration" is NOT a full effective-running-config dump
// the way Cisco's "show running-config" or Junos's "show" are -- confirmed
// live, it's a REPLAYABLE SCRIPT OF ONLY THE COMMANDS THAT DIFFER FROM
// FACTORY DEFAULT (grouped under "# Module <name> configuration." comment
// headers, one per EXOS subsystem, most emitted empty on a lightly
// configured device). A setting left at its factory default never appears
// here at all, even though it's very much in effect. "show configuration
// detail" is EXOS's own option for including defaults too, deliberately
// NOT used here -- it would make GetConfig's output enormous and, unlike
// this replayable-script form, not directly meaningful for a future
// diff/merge feature the way the plain form is. Callers comparing two
// GetConfig() calls, or hashing this output, are therefore comparing
// "what's been explicitly changed from default", not "the device's full
// state" -- a real distinction from Cisco/Junos GetConfig callers should
// be aware of.
func (d *ExtremeDriver) GetConfig() (string, error) {
	if _, _, errStr, err := d.runSingle(extremeDisablePagingCommand); err != nil {
		return "", err
	} else if errStr != "" {
		return "", olterr.New(olterr.TypeConnection, "GetConfig: \"disable cli paging\" failed: "+errStr)
	}

	output, truncated, errStr, err := d.runSingle("show configuration")
	if err != nil {
		return "", err
	}
	if errStr != "" {
		return "", olterr.New(olterr.TypeProtocol, "GetConfig: \"show configuration\" failed: "+errStr)
	}
	if truncated {
		return "", olterr.New(
			olterr.TypeProtocol,
			`GetConfig: "show configuration" response was truncated -- treating as failed rather than returning partial config`,
		)
	}
	return output, nil
}

// extremePortsInfoRowPattern matches one data row of "show ports
// information": port number (optionally "<slot>:<port>" stacking form),
// the fixed-width Flags string, then the Link State word -- confirmed
// live these three leading fields are ALWAYS present and whitespace-
// delimited even when later columns in the same row collapse to nothing
// for a down port (see the type doc comment's pagination section for why
// "show ports configuration" was NOT used instead: its own columns
// collapse unpredictably for a down port in a way these three never do).
// Deliberately ignores every column after Link State.
var extremePortsInfoRowPattern = regexp.MustCompile(`(?m)^(\d+(?::\d+)?)\s+(\S+)\s+(\S+)`)

// parseExtremePortsInformation parses "show ports information" into a map
// keyed by port number, with only IsUp/IsEnabled populated (Description
// comes from a separate command -- see GetInterfaces). IsEnabled is
// confirmed live from the Flags string's first character: 'E' (Port
// Enabled) vs 'D' (Port Disabled) -- see the exact legend line this
// driver confirmed live: "D - Port Disabled, ... E - Port Enabled" (from
// this same command's own trailing legend). IsUp is inferred from the
// Link State word matching "active" (per the documented state legend
// this driver captured on a sibling command, "A-Active, R-Ready, NP-Port
// Not Present, L-Loopback") -- ONLY THE "ready" (down) CASE WAS ACTUALLY
// OBSERVED LIVE (this lab's ports had no live link partner); the "active"
// spelling/casing for an up port is inferred from the documented legend,
// not confirmed against a real up port. Validate before relying on the
// IsUp=true case in production.
func parseExtremePortsInformation(output string) map[string]InterfaceInfo {
	result := make(map[string]InterfaceInfo)
	for _, m := range extremePortsInfoRowPattern.FindAllStringSubmatch(output, -1) {
		port, flags, linkState := m[1], m[2], m[3]
		result[port] = InterfaceInfo{
			IsEnabled: strings.HasPrefix(flags, "E"),
			IsUp:      strings.EqualFold(linkState, "active"),
		}
	}
	return result
}

// parseExtremePortsDescription parses "show ports description" by reading
// column boundaries off its own "======" separator line rather than
// whitespace-splitting each data row -- confirmed live necessary: the
// Description String column can be entirely empty for a port (a bare
// trailing-spaces row, e.g. port 2 in this driver's live capture), which
// whitespace-splitting can't distinguish from "fewer columns present".
// Slicing by the separator line's own column widths handles that
// correctly, and also tolerates a Description String containing spaces
// (a real, expected case -- e.g. "uplink to core"), which whitespace-
// splitting would have broken apart regardless of the empty-column issue.
func parseExtremePortsDescription(output string) map[string]string {
	result := make(map[string]string)
	lines := strings.Split(output, "\n")

	sepIdx := -1
	for i, raw := range lines {
		trimmed := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(trimmed, "======") {
			sepIdx = i
			break
		}
	}
	if sepIdx == -1 {
		return result
	}
	sep := strings.TrimRight(lines[sepIdx], "\r")

	// Column boundaries: each run of '=' characters in the separator line
	// marks one column; a column's text lives at those same byte offsets
	// in every data row beneath it. The last column runs to end-of-line
	// (not just to its own '=' run's width) so a long description isn't
	// clipped to the separator's own (arbitrary, cosmetic) dashed width.
	type col struct{ start, end int }
	var cols []col
	inRun := false
	for i, c := range sep {
		if c == '=' {
			if !inRun {
				cols = append(cols, col{start: i})
				inRun = true
			}
		} else if inRun {
			cols[len(cols)-1].end = i
			inRun = false
		}
	}
	if inRun {
		cols[len(cols)-1].end = len(sep)
	}
	if len(cols) < 2 {
		return result
	}
	cols[len(cols)-1].end = -1 // sentinel: slice to end-of-line for the last column

	sliceCol := func(line string, c col) string {
		if c.start >= len(line) {
			return ""
		}
		end := c.end
		if end == -1 || end > len(line) {
			end = len(line)
		}
		return strings.TrimSpace(line[c.start:end])
	}

	for _, raw := range lines[sepIdx+1:] {
		line := strings.TrimRight(raw, "\r")
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "=====") {
			break // trailing rule line closing the table, confirmed live present
		}
		port := sliceCol(line, cols[0])
		if port == "" {
			continue
		}
		desc := sliceCol(line, cols[len(cols)-1])
		result[port] = desc
	}
	return result
}

// GetInterfaces implements InterfaceProvider: "disable cli paging", then
// "show ports information" (admin/link state -- see
// parseExtremePortsInformation) and "show ports description" (the
// per-port free-text description -- see parseExtremePortsDescription),
// merged by port number.
//
// Deliberately leaves MTU, SpeedMbps, MACAddress, and IPAddress at their
// zero values rather than guessing: "show ports information"'s "Jumbo
// Size" column is a CONFIGURED CEILING (max frame size the port will
// accept), not the operational MTU NAPALM's get_interfaces() means by
// this field, so it's deliberately not mapped there. Speed/duplex live in
// "show ports configuration"'s table instead, but that table's own
// columns were confirmed live to collapse unpredictably for a down port
// (several interior columns go fully blank, shifting a naive whitespace
// split) with no live-observed "up" row available in this lab to confirm
// the populated-column layout against -- rather than guess at column
// positions never actually seen, this is left unpopulated pending a
// switch with an active link to verify against. IPAddress is correctly
// always "" here: EXOS physical switch ports don't carry IP addresses
// (VLANs/SVIs do, a separate, unaddressed-here concept -- see the type
// doc comment).
func (d *ExtremeDriver) GetInterfaces() (map[string]InterfaceInfo, error) {
	if _, _, errStr, err := d.runSingle(extremeDisablePagingCommand); err != nil {
		return nil, err
	} else if errStr != "" {
		return nil, olterr.New(olterr.TypeConnection, "GetInterfaces: \"disable cli paging\" failed: "+errStr)
	}

	infoOutput, _, errStr, err := d.runSingle("show ports information")
	if err != nil {
		return nil, err
	}
	if errStr != "" {
		return nil, olterr.New(olterr.TypeProtocol, "GetInterfaces: \"show ports information\" failed: "+errStr)
	}

	descOutput, _, errStr, err := d.runSingle("show ports description")
	if err != nil {
		return nil, err
	}
	if errStr != "" {
		return nil, olterr.New(olterr.TypeProtocol, "GetInterfaces: \"show ports description\" failed: "+errStr)
	}

	interfaces := parseExtremePortsInformation(infoOutput)
	descriptions := parseExtremePortsDescription(descOutput)
	for port, desc := range descriptions {
		info, ok := interfaces[port]
		if !ok {
			continue // a port show ports description knows but show ports information doesn't -- shouldn't happen live, skip rather than fabricate a bare entry
		}
		info.Description = desc
		interfaces[port] = info
	}
	return interfaces, nil
}

// extremeLLDPNeighborRowPattern: best-effort match for one data row of
// "show lldp neighbors" -- port token, then a MAC-address-shaped Chassis
// ID, then a second MAC-address-shaped Port ID, with the remote system
// name (if any) trailing. NOT LIVE-VERIFIED: this lab's two ports have no
// LLDP-speaking neighbor attached, so "show lldp neighbors" only ever
// produced empty output here (which IS confirmed live -- see
// GetLLDPNeighbors) -- the populated-table shape below is built from
// Extreme's published CLI reference documentation, not a real capture.
// Validate against a real neighbor before relying on this in production,
// the same posture CiscoIOSDriver's own parseLLDPNeighbors documents for
// its data-row shape.
var extremeLLDPNeighborRowPattern = regexp.MustCompile(
	`(?m)^(\S+)\s+([0-9a-fA-F:.]{6,})\s+([0-9a-fA-F:.]{6,})(?:\s+(.+?))?\s*$`,
)

// parseExtremeLLDPNeighbors: see extremeLLDPNeighborRowPattern's doc
// comment for the live-verification caveat on the populated case. Empty
// input correctly produces an empty map (confirmed live: zero neighbors
// means "show lldp neighbors" prints nothing at all -- no header, no
// trailer -- unlike Cisco, which still prints a header/"Total entries
// displayed: 0" trailer even with none).
func parseExtremeLLDPNeighbors(output string) map[string][]LLDPNeighbor {
	result := make(map[string][]LLDPNeighbor)
	for _, m := range extremeLLDPNeighborRowPattern.FindAllStringSubmatch(output, -1) {
		localPort, portID, remoteName := m[1], m[3], m[4]
		if remoteName == "" {
			remoteName = m[2] // no system name field -- fall back to chassis ID, same "something over nothing" posture as leaving fields at zero value elsewhere
		}
		result[localPort] = append(result[localPort], LLDPNeighbor{
			RemoteSystemName: remoteName,
			RemotePortID:     portID,
		})
	}
	return result
}

// GetLLDPNeighbors implements LLDPNeighborsProvider: "disable cli paging"
// then "show lldp neighbors". See parseExtremeLLDPNeighbors's doc comment
// for the live-verification caveat on non-empty output.
func (d *ExtremeDriver) GetLLDPNeighbors() (map[string][]LLDPNeighbor, error) {
	if _, _, errStr, err := d.runSingle(extremeDisablePagingCommand); err != nil {
		return nil, err
	} else if errStr != "" {
		return nil, olterr.New(olterr.TypeConnection, "GetLLDPNeighbors: \"disable cli paging\" failed: "+errStr)
	}

	output, _, errStr, err := d.runSingle("show lldp neighbors")
	if err != nil {
		return nil, err
	}
	if errStr != "" {
		return nil, olterr.New(olterr.TypeProtocol, "GetLLDPNeighbors: \"show lldp neighbors\" failed: "+errStr)
	}
	return parseExtremeLLDPNeighbors(output), nil
}
