package drivers

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/onu-platform/gearctl/internal/olterr"
)

// HuaweiDriver targets Huawei's VRP (Versatile Routing Platform) CLI --
// used across Huawei's S-series switches, AR routers, NE routers, and
// (per this platform's own OLT-facing use case) other VRP-lineage device
// CLIs.
//
// Confirmed LIVE against a real Huawei S6700-24-EI core switch (VRP
// 5.150, V200R005C00SPC500, telnet) for all four read capabilities:
// GetFacts, GetInterfaces (real descriptions/MTU/speed/MAC/IP across ~90
// real interfaces -- Eth-Trunk, Vlanif, XGigabitEthernet, MEth,
// LoopBack, NULL0), GetLLDPNeighbors (real neighbors on 4 local
// interfaces, including multiple neighbors sharing one local port), and
// GetConfig (a real ~16KB `display current-configuration`). GetFacts'
// model/uptime and hostname parsing were WRONG against this device on
// the first pass and have since been fixed -- see
// huaweiModelUptimePattern's and parseHuaweiPromptHostname's own doc
// comments for exactly what broke and why. Given two real, non-obvious
// bugs already surfaced from testing against just one device/firmware,
// treat anything NOT explicitly marked "confirmed live" in the parsers
// below with real skepticism -- VRP output is known to vary across
// product lines (S-series vs AR vs NE) and firmware versions/sub-brands
// (see the "Quidway" case) more than a blind read of Huawei's
// documentation could anticipate. Not yet confirmed on any device: the
// zero-neighbor LLDP case, and the "LLDP is off" rejection message (this
// test device had LLDP on with real neighbors).
//
// No enable-style escalation: VRP's system-view (config mode) needs no
// separate password the way Cisco's "enable" does, and
// transport/enablevendors.go already documents "huawei" as explicitly
// NOT an enable-style vendor. Moot here regardless -- this driver never
// enters system-view, since every capability below is read-only.
//
// Read-only capabilities only (FactsProvider, InterfaceProvider,
// LLDPNeighborsProvider, ConfigGetter) -- ConfigLoader (config push) is
// deliberately NOT implemented. VRP's system-view/interface-view modal
// CLI is structurally similar to Cisco IOS's, so CiscoIOSDriver's
// stanza-diffing LoadMergeCommands machinery could plausibly be adapted
// later -- but doing that blind, with no live device to verify command
// acceptance, prompt transitions, or rejection-message format against,
// is real, scoped follow-up work, not a small addition (the same
// reasoning ExtremeDriver's and MikroTikDriver's own doc comments give
// for their own missing ConfigLoader).
type HuaweiDriver struct {
	BaseDriver
}

func NewHuaweiDriver() *HuaweiDriver {
	return &HuaweiDriver{}
}

func (d *HuaweiDriver) Vendor() string {
	return "huawei"
}

// huaweiDisablePaging is sent proactively before every read command
// below, the same posture CiscoIOSDriver takes for "terminal length 0"
// (see its GetConfig doc comment) rather than relying solely on
// transport/promptdetector.go's generic pagination-marker fallback --
// which DOES recognize VRP's "---- More ----" prompt already (see
// promptdetector.go's own doc comment), but proactively disabling paging
// avoids depending on that fallback firing correctly for a command path
// never exercised against a real device. "temporary" scopes the change
// to this session only, not the saved config -- documented VRP syntax,
// already referenced as this project's own known Huawei example in
// README.md's `-disable-paging` flag table before this driver existed.
const huaweiDisablePaging = "screen-length 0 temporary"

// huaweiVersionPattern matches `display version`'s software-version
// line, e.g.:
//
//	VRP (R) software, Version 5.170 (S5720 V200R011C10SPC600)
//
// Confirmed LIVE against a real Huawei S6700 -- extracted "5.150"
// correctly from that device's real "VRP (R) software, Version 5.150
// (S6700 V200R005C00SPC500)" line.
var huaweiVersionPattern = regexp.MustCompile(`(?i)VRP\s*\(R\)\s*software,?\s*Version\s+([^\s,]+)`)

// huaweiModelUptimePattern matches `display version`'s model+uptime
// line, e.g.:
//
//	HUAWEI S5720-28X-LI-AC Routing Switch uptime is 0 week, 2 days, 3 hours, 17 minutes
//	Quidway S6700-24-EI Routing Switch uptime is 12 weeks, 0 day, 6 hours, 55 minutes
//
// Confirmed LIVE against a real Huawei S6700 (VRP 5.150/V200R005C00SPC500)
// that this line does NOT always start with "HUAWEI" -- this device's
// firmware prints the older "Quidway" sub-brand name instead. Both
// prefixes accepted for that reason; there may be others this hasn't hit
// yet. Unlike Cisco's equivalent line, this one never carries the
// hostname -- VRP's `display version` has no hostname field at all, see
// parseHuaweiPromptHostname below for where hostname actually comes
// from.
var huaweiModelUptimePattern = regexp.MustCompile(`(?i)^(?:HUAWEI|Quidway)\s+(\S+)\s+.*?uptime is\s+(.+?)\s*$`)

// huaweiPromptHostnamePattern extracts the hostname VRP's own prompt
// always carries -- "<hostname>" in user-view (confirmed live: a real
// device's prompt was literally "<Core_Switch_Kly>"), or
// "[hostname-something]" in system-view/a sub-view (e.g. after
// "interface X" -- not reachable by this driver, which never leaves
// user-view, but handled defensively: everything up to the first "-" is
// taken as the hostname, same convention network engineers use reading
// these prompts by eye).
//
// Deliberately NOT sourced from `display current-configuration | include
// sysname` (an earlier version of this driver's approach): confirmed
// LIVE against the same real device that this specific filter argument
// returns EMPTY output, even though the sysname line is unquestionably
// present -- `display current-configuration | include name` (a
// different, broader filter word) DOES return it
// ("sysname Core_Switch_Kly" among other "name"-containing lines) on the
// exact same device in the exact same session. The filter argument
// "sysname" is also a literal substring of the resulting output line
// itself ("sysname Core_Switch_Kly") -- strongly suggestive of an
// echo/self-similarity false-positive somewhere in this codebase's own
// transport-layer echo-stripping (untested against other vendors; may
// not be Huawei-specific), not a VRP quirk. Not chased down further here
// -- the prompt already carries the hostname with no command needed at
// all, sidestepping the bug rather than fixing its root cause. Worth a
// dedicated follow-up investigation given the blast radius (any
// driver/caller piping a `display`/`show` through an `include`/`grep`
// filter word that also appears in its own output could hit this).
var huaweiPromptHostnamePattern = regexp.MustCompile(`^[<\[](\S+?)[>\]]`)

func parseHuaweiPromptHostname(prompt string) string {
	m := huaweiPromptHostnamePattern.FindStringSubmatch(strings.TrimSpace(prompt))
	if m == nil {
		return ""
	}
	return strings.SplitN(m[1], "-", 2)[0]
}

// GetFacts implements FactsProvider. Runs huaweiDisablePaging, `display
// version`, and `display interface brief` (for interface_list) --
// hostname comes from the session's own prompt afterward (see
// parseHuaweiPromptHostname's doc comment), not a separate command.
//
// serial_number is deliberately left empty rather than guessed: unlike
// Cisco's single well-documented "Processor board ID" line, VRP's serial
// lives in different commands/fields across product lines (`display
// esn`, `display device manuinfo`'s "BarCode", others) with no one
// command confirmed to exist across the board -- guessing wrong here
// would silently populate a plausible-looking but potentially bogus
// value; a caller that needs it should pass the right command for their
// specific platform via RunCommands directly.
//
// fqdn is best-effort, same posture as CiscoIOSDriver.GetFacts: VRP's
// prompt has no separate domain-name concept surfaced here, so this
// just reuses the bare hostname.
func (d *HuaweiDriver) GetFacts() (map[string]interface{}, error) {
	results, err := d.RunCommands([]string{
		huaweiDisablePaging,
		"display version",
		"display interface brief",
	})
	if err != nil {
		return nil, err
	}
	if len(results) < 3 || results[0].Error != "" || results[1].Error != "" || results[2].Error != "" {
		return nil, olterr.New(
			olterr.TypeProtocol,
			`GetFacts: "screen-length 0 temporary" or one of the facts commands failed -- see the per-command results for the actual error`,
		)
	}

	versionOutput := ""
	if results[1].Output != nil {
		versionOutput = *results[1].Output
	}
	briefOutput := ""
	if results[2].Output != nil {
		briefOutput = *results[2].Output
	}

	return parseHuaweiFacts(versionOutput, d.CurrentPrompt(), briefOutput), nil
}

// parseHuaweiFacts is GetFacts' parsing logic, pulled out as a pure
// function over already-captured command output (and the session's
// current prompt) so it's testable against fixed fixtures without a
// live device -- see huaweidriver_test.go.
func parseHuaweiFacts(versionOutput, currentPrompt, briefOutput string) map[string]interface{} {
	facts := map[string]interface{}{
		"vendor":         "Huawei",
		"model":          "",
		"hostname":       "",
		"fqdn":           "",
		"os_version":     "",
		"serial_number":  "",
		"uptime":         "",
		"interface_list": []string{},
	}

	if m := huaweiVersionPattern.FindStringSubmatch(versionOutput); m != nil {
		facts["os_version"] = m[1]
	}
	for _, line := range strings.Split(versionOutput, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if m := huaweiModelUptimePattern.FindStringSubmatch(line); m != nil {
			facts["model"] = m[1]
			facts["uptime"] = m[2]
		}
	}
	if hostname := parseHuaweiPromptHostname(currentPrompt); hostname != "" {
		facts["hostname"] = hostname
		facts["fqdn"] = hostname
	}

	facts["interface_list"] = parseHuaweiInterfaceBrief(briefOutput)

	return facts
}

// huaweiInterfaceBriefRowPattern matches one data row of `display
// interface brief`, e.g.:
//
//	GigabitEthernet0/0/1        up    up          0%     0%          0          0
//	GigabitEthernet0/0/2        *down down         0%     0%          0          0
//
// requiring the PHY-status column to look like one of VRP's documented
// values ("up", "down", "*down" administratively-down, "^down" standby)
// specifically so the command's own legend lines ("PHY: Physical",
// "*down: administratively down", ...) that precede the table don't get
// mistaken for data rows the way a bare "first two fields" split would.
// Confirmed LIVE against a real Huawei S6700 -- correctly extracted all
// ~90 real interfaces (Eth-Trunk*, Vlanif*, XGigabitEthernet0/0/*,
// MEth0/0/1, NULL0, LoopBack0) with none of the legend lines misparsed
// as data rows.
var huaweiInterfaceBriefRowPattern = regexp.MustCompile(`(?m)^(\S+)\s+(?:\*|\^)?(?:up|down)\s+(?:up|down)\b`)

func parseHuaweiInterfaceBrief(output string) []string {
	names := make([]string, 0)
	for _, m := range huaweiInterfaceBriefRowPattern.FindAllStringSubmatch(output, -1) {
		names = append(names, m[1])
	}
	return names
}

// huaweiInterfaceHeaderPattern matches the header line `display
// interface` prints once per interface, e.g.:
//
//	GigabitEthernet0/0/1 current state : UP
//	GigabitEthernet0/0/2 current state : Administratively DOWN
//
// Confirmed LIVE against a real Huawei S6700, both the plain UP/DOWN
// case (e.g. "XGigabitEthernet0/0/5 current state : UP") and the
// disabled case (e.g. "Vlanif1 current state : Administratively DOWN").
var huaweiInterfaceHeaderPattern = regexp.MustCompile(`(?m)^(\S+) current state\s*:\s*(.+?)\s*$`)

// huaweiLineProtocolPattern matches the line immediately following the
// header, e.g. "Line protocol current state : UP". Confirmed LIVE.
var huaweiLineProtocolPattern = regexp.MustCompile(`(?im)^Line protocol current state\s*:\s*(\S+)`)

// huaweiDescriptionPattern matches, e.g. "Description:uplink-to-core" or
// "Description :". Same "line entirely absent when unset" assumption
// CiscoIOSDriver's own ifaceDescriptionPattern documents. Confirmed LIVE
// against a real Huawei S6700 -- both real configured descriptions (e.g.
// "L2|FTTH|WLINK", "**Wlink-Ulink-L3**") and the empty/unset case parsed
// correctly, the latter only after the newline-crossing fix described
// below.
//
// Deliberately [ \t]* rather than \s* around the colon: Go's \s matches
// "\n" too, and with a *greedy* (.*) capture (unlike
// huaweiInterfaceHeaderPattern's non-greedy sibling below, whose trailing
// \s* an adjacent $ anchor forces back to the same line), a bare \s* on
// an EMPTY "Description:" line silently consumed the line's own
// trailing newline and let (.*) capture the entire NEXT line instead --
// caught by TestHuaweiGetInterfacesParsesUpAndAdministrativelyDownBlocks
// wrongly asserting the next line's "Switch Port,Link Type : ..." text
// as the description of an interface with no description configured at
// all.
var huaweiDescriptionPattern = regexp.MustCompile(`(?im)^Description[ \t]*:[ \t]*(.*)$`)

// huaweiMTUPattern matches either of VRP's two documented MTU line
// forms, e.g. "The Maximum Transmit Unit is 1500" (routed interfaces) or
// "The Maximum Frame Length is 9216" (switched interfaces). Confirmed
// LIVE (the "Frame Length" form -- a real S6700 interface reporting
// 9216); the "Transmit Unit" form is NOT live-verified.
var huaweiMTUPattern = regexp.MustCompile(`(?i)Maximum (?:Transmit Unit|Frame Length) is\s+(\d+)`)

// huaweiSpeedPattern matches, e.g. "Speed : 1000, Loopback : NONE" --
// already in Mbps (unlike Cisco's Kbit/sec "BW" line), so no unit
// conversion needed. Confirmed LIVE -- a real S6700 10GE port
// (XGigabitEthernet0/0/5) correctly reported 10000. Left unpopulated
// (InterfaceInfo's zero value) if this doesn't match (confirmed live for
// Vlanif/Eth-Trunk interfaces, which don't print a Speed line at all),
// same "don't guess" posture MikroTikDriver.GetInterfaces takes for
// SpeedMbps.
var huaweiSpeedPattern = regexp.MustCompile(`(?i)Speed\s*:\s*(\d+)`)

// huaweiIPAddressPattern matches, e.g. "Internet Address is 192.168.1.1/24".
// Confirmed LIVE (a real MEth0/0/1 and Vlanif1 both correctly parsed).
var huaweiIPAddressPattern = regexp.MustCompile(`(?i)Internet Address is\s+(\S+)`)

// huaweiMACPattern matches, e.g. "Hardware address is 4c1f-cc00-0001-0001"
// -- VRP's dash-separated hex-group MAC notation. Confirmed LIVE (a real
// device consistently returned e.g. "5860-5f48-41e0" across interfaces).
var huaweiMACPattern = regexp.MustCompile(`(?i)Hardware address is\s+([0-9a-fA-F.-]+)`)

// parseHuaweiInterfaces splits `display interface` output into
// per-interface blocks at each huaweiInterfaceHeaderPattern match (a
// block runs from one header line up to, but not including, the next
// header line or the end of output), then parses each block
// independently -- same "a field's regex simply not matching leaves that
// field at its zero value rather than erroring" posture as
// CiscoIOSDriver's parseInterfaces. Confirmed LIVE end-to-end against a
// real Huawei S6700 (see GetInterfaces' doc comment).
func parseHuaweiInterfaces(output string) map[string]InterfaceInfo {
	matches := huaweiInterfaceHeaderPattern.FindAllStringSubmatchIndex(output, -1)
	result := make(map[string]InterfaceInfo, len(matches))

	for i, m := range matches {
		blockStart := m[0]
		blockEnd := len(output)
		if i+1 < len(matches) {
			blockEnd = matches[i+1][0]
		}
		block := output[blockStart:blockEnd]

		name := output[m[2]:m[3]]
		adminState := output[m[4]:m[5]]

		info := InterfaceInfo{
			IsEnabled: !strings.Contains(strings.ToUpper(adminState), "ADMINISTRATIVELY DOWN"),
		}
		if mm := huaweiLineProtocolPattern.FindStringSubmatch(block); mm != nil {
			info.IsUp = strings.EqualFold(mm[1], "UP")
		}
		if mm := huaweiDescriptionPattern.FindStringSubmatch(block); mm != nil {
			info.Description = strings.TrimSpace(mm[1])
		}
		if mm := huaweiIPAddressPattern.FindStringSubmatch(block); mm != nil {
			info.IPAddress = mm[1]
		}
		if mm := huaweiMACPattern.FindStringSubmatch(block); mm != nil {
			info.MACAddress = mm[1]
		}
		if mm := huaweiMTUPattern.FindStringSubmatch(block); mm != nil {
			if mtu, convErr := strconv.Atoi(mm[1]); convErr == nil {
				info.MTU = mtu
			}
		}
		if mm := huaweiSpeedPattern.FindStringSubmatch(block); mm != nil {
			if speed, convErr := strconv.ParseFloat(mm[1], 64); convErr == nil {
				info.SpeedMbps = speed
			}
		}

		result[name] = info
	}
	return result
}

// GetInterfaces implements InterfaceProvider: runs huaweiDisablePaging
// then `display interface` (the full per-interface dump, not `display
// interface brief` -- that command alone doesn't carry MTU/description/
// MAC/IP), and parses it into structured per-interface state. Confirmed
// LIVE against a real Huawei S6700 core switch (~90 real interfaces:
// Eth-Trunk, Vlanif, XGigabitEthernet, MEth, LoopBack, NULL0) --
// up/down, enabled/administratively-down, description, MTU, MAC, and IP
// address all confirmed against real production output; SpeedMbps
// confirmed both populated (a real 10GE port) and correctly left at zero
// for interface types that don't print a Speed line.
func (d *HuaweiDriver) GetInterfaces() (map[string]InterfaceInfo, error) {
	results, err := d.RunCommands([]string{huaweiDisablePaging, "display interface"})
	if err != nil {
		return nil, err
	}
	if len(results) < 2 || results[0].Error != "" || results[1].Error != "" {
		return nil, olterr.New(
			olterr.TypeProtocol,
			`GetInterfaces: "screen-length 0 temporary" or "display interface" failed -- see the command results for the actual error`,
		)
	}
	output := ""
	if results[1].Output != nil {
		output = *results[1].Output
	}
	return parseHuaweiInterfaces(output), nil
}

// huaweiLLDPHeaderPattern matches `display lldp neighbor brief`'s column
// header row -- tolerant of minor wording differences across VRP
// versions/product lines ("Neighbor Dev" vs "Neighbor Device", "Intf" vs
// "Interface") that haven't been observed live, on top of the real
// header this WAS confirmed live against: "Local Intf   Neighbor Dev
// Neighbor Intf             Exptime".
var huaweiLLDPHeaderPattern = regexp.MustCompile(`(?i)Local\s+Int.*Neighbor\s+Dev.*Neighbor\s+Int`)

// parseHuaweiLLDPNeighbors parses `display lldp neighbor brief` output
// into a map keyed by local interface name. Skips everything up to and
// including the column-header line, then reads one neighbor per line
// until a blank line -- same table-scanning shape as
// CiscoIOSDriver.parseLLDPNeighbors, but WITHOUT that driver's live-
// confirmed "Total entries displayed" trailer (VRP's real output, unlike
// IOS's, has no equivalent trailer line at all -- confirmed live, the
// table just ends at a blank line). Each row is split on whitespace:
// local interface is the first field, remote system name the second,
// remote port ID the third -- fixed columns rather than Cisco's "last
// field" convention, since VRP's brief table also has a trailing Exptime
// column this driver deliberately does not want to capture as the port
// ID. A row that doesn't split into at least 3 fields is skipped rather
// than misparsed.
//
// Confirmed LIVE against a real Huawei S6700 with real neighbors on 4
// local interfaces, including MULTIPLE neighbors on one local interface
// (a real port with 6 distinct sub-interface neighbors all sharing the
// same physical link) -- the append-don't-overwrite map value confirmed
// necessary, not just theoretical. Zero-neighbor/empty-table case still
// NOT live-verified (this device had neighbors on every LLDP-speaking
// port tested).
//
// Known REAL, confirmed-live VRP limitation this does NOT work around:
// `display lldp neighbor brief`'s fixed-width columns truncate long
// device/port names with a literal "..." (e.g. a real neighbor came back
// as remote system name "Jalpa-Laghubittya-EoM..." and remote port ID
// "bridge520-IntrAnet/vpl...") -- this is genuinely the most that
// command tells you, not a parsing bug; a caller that needs the untruncated
// name would need `display lldp neighbor` (the verbose, one-neighbor-per-block
// form) instead, not implemented here.
func parseHuaweiLLDPNeighbors(output string) map[string][]LLDPNeighbor {
	result := make(map[string][]LLDPNeighbor)
	inTable := false

	for _, raw := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(raw, "\r"))

		if !inTable {
			if huaweiLLDPHeaderPattern.MatchString(trimmed) {
				inTable = true
			}
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "---") {
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) < 3 {
			continue
		}
		localIntf := fields[0]
		result[localIntf] = append(result[localIntf], LLDPNeighbor{
			RemoteSystemName: fields[1],
			RemotePortID:     fields[2],
		})
	}

	return result
}

// GetLLDPNeighbors implements LLDPNeighborsProvider: runs
// huaweiDisablePaging then `display lldp neighbor brief`, and parses the
// resulting table -- confirmed LIVE against a real Huawei S6700 (see
// parseHuaweiLLDPNeighbors' doc comment). Requires LLDP to be enabled on
// the device (VRP's global `lldp enable`) -- unlike CiscoIOSDriver/
// ExtremeDriver, the "LLDP isn't even on" rejection case has NOT been
// confirmed live for VRP (this device had it on), so this deliberately
// does NOT special-case any particular rejection message the way those
// two do; if LLDP is off, whatever VRP prints simply fails to match
// huaweiLLDPHeaderPattern and this returns an empty map --
// indistinguishable here from "LLDP is on but sees nothing", unlike
// those two drivers' more precise handling. Tighten this once the real
// device-reported message is known.
func (d *HuaweiDriver) GetLLDPNeighbors() (map[string][]LLDPNeighbor, error) {
	results, err := d.RunCommands([]string{huaweiDisablePaging, "display lldp neighbor brief"})
	if err != nil {
		return nil, err
	}
	if len(results) < 2 || results[0].Error != "" || results[1].Error != "" {
		return nil, olterr.New(
			olterr.TypeProtocol,
			`GetLLDPNeighbors: "screen-length 0 temporary" or "display lldp neighbor brief" failed -- see the command results for the actual error`,
		)
	}
	output := ""
	if results[1].Output != nil {
		output = *results[1].Output
	}
	return parseHuaweiLLDPNeighbors(output), nil
}

// GetConfig implements ConfigGetter: runs huaweiDisablePaging then
// `display current-configuration`, and returns that command's output
// verbatim -- no parsing, this is the read-only half of the config seam.
// Confirmed LIVE against a real Huawei S6700 -- a real ~16KB
// current-configuration came back complete (untruncated, matching
// MaxOutputBytes headroom).
func (d *HuaweiDriver) GetConfig() (string, error) {
	results, err := d.RunCommands([]string{huaweiDisablePaging, "display current-configuration"})
	if err != nil {
		return "", err
	}
	if len(results) < 2 || results[0].Error != "" || results[1].Error != "" {
		return "", olterr.New(
			olterr.TypeProtocol,
			`GetConfig: "screen-length 0 temporary" or "display current-configuration" failed -- see the command results for the actual error`,
		)
	}
	if results[1].Output == nil {
		return "", nil
	}
	return *results[1].Output, nil
}
