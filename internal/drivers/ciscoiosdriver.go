package drivers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/onu-platform/gearctl/internal/olterr"
	"github.com/onu-platform/gearctl/internal/transport"
	"github.com/pmezard/go-difflib/difflib"
)

// CiscoIOSDriver is the first vendor-specific driver: Cisco IOS and its
// close lineage (IOS-XE in classic-CLI mode). It reuses BaseDriver's
// session lifecycle unchanged -- Connect/RunCommands/Disconnect are
// identical to RawDriver's -- and adds GetFacts(), the FactsProvider
// capability (driver.go's Phase 2 seam): structured facts parsed from
// `show version` / `show ip interface brief`, the same job NAPALM's
// cisco_ios.get_facts() does.
//
// IMPORTANT: the regexes below target the well-documented, standard Cisco
// IOS `show version`/`show ip interface brief` output format. Unlike
// enablevendors.go's escalation handling (confirmed live against real
// hardware, see transport/enablevendors.go), this parser has NOT been
// verified against a real device or even a real captured "show version".
// Validate against actual hardware before relying on this in production --
// IOS-XE and older 12.x trains are known to vary some of this formatting.
type CiscoIOSDriver struct {
	BaseDriver
}

func NewCiscoIOSDriver() *CiscoIOSDriver {
	return &CiscoIOSDriver{}
}

func (d *CiscoIOSDriver) Vendor() string {
	return "cisco"
}

var (
	// Matches the banner line, e.g.:
	//   Cisco IOS Software, C3560E Software (C3560E-UNIVERSALK9-M), Version 15.2(2)E1, RELEASE SOFTWARE (fc3)
	// Non-greedy up to the first "Version" so this doesn't accidentally
	// match a later, unrelated "Version" (e.g. the BOOTLDR line).
	iosVersionPattern = regexp.MustCompile(`(?i)Cisco IOS.*?Version\s+([^\s,]+)`)

	// Matches the model line, e.g.:
	//   cisco WS-C3560E-24TD (PowerPC405) processor (revision A0) with 131072K bytes of memory.
	iosModelPattern = regexp.MustCompile(`(?i)^cisco\s+(\S+)\s+\([^)]+\)\s+processor`)

	// Matches, e.g.: Processor board ID FOC1234X56Y
	iosSerialPattern = regexp.MustCompile(`(?i)Processor board ID\s+(\S+)`)

	// Matches the uptime line, which also happens to be the only place
	// `show version` prints the hostname, e.g.:
	//   switch1 uptime is 6 weeks, 3 days, 4 hours, 22 minutes
	iosUptimeLinePattern = regexp.MustCompile(`(?i)^(\S+)\s+uptime is\s+(.+)$`)
)

// GetFacts implements FactsProvider. Runs `terminal length 0` (disables
// output paging -- see the doc comment on GetConfig for why this is sent
// proactively rather than left to the generic pagination-marker fallback),
// then `show version` and `show ip interface brief`, and parses the
// combined output into the same shape NAPALM's get_facts() returns.
//
// fqdn is best-effort: `show version` never carries a domain name, so this
// falls back to the bare hostname rather than issuing a second lookup
// command (e.g. `show running-config | include ip domain`) -- a caller
// that needs the real FQDN should pass that command explicitly via
// RunCommands.
func (d *CiscoIOSDriver) GetFacts() (map[string]interface{}, error) {
	results, err := d.RunCommands([]string{"terminal length 0", "show version", "show ip interface brief"})
	if err != nil {
		return nil, err
	}
	if len(results) < 3 || results[0].Error != "" || results[1].Error != "" || results[2].Error != "" {
		return nil, olterr.New(
			olterr.TypeProtocol,
			`GetFacts: "terminal length 0" or one of the facts commands failed -- see the per-command results for the actual error`,
		)
	}

	versionOutput := ""
	if results[1].Output != nil {
		versionOutput = *results[1].Output
	}
	interfacesOutput := ""
	if results[2].Output != nil {
		interfacesOutput = *results[2].Output
	}

	facts := map[string]interface{}{
		"vendor":         "Cisco",
		"model":          "",
		"hostname":       "",
		"fqdn":           "",
		"os_version":     "",
		"serial_number":  "",
		"uptime":         "",
		"interface_list": []string{},
	}

	if m := iosVersionPattern.FindStringSubmatch(versionOutput); m != nil {
		facts["os_version"] = m[1]
	}
	if m := iosSerialPattern.FindStringSubmatch(versionOutput); m != nil {
		facts["serial_number"] = m[1]
	}
	for _, line := range strings.Split(versionOutput, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if m := iosModelPattern.FindStringSubmatch(line); m != nil {
			facts["model"] = m[1]
		}
		if m := iosUptimeLinePattern.FindStringSubmatch(line); m != nil {
			facts["hostname"] = m[1]
			facts["uptime"] = m[2]
			facts["fqdn"] = m[1]
		}
	}

	facts["interface_list"] = parseInterfaceBrief(interfacesOutput)

	return facts, nil
}

// GetConfig implements ConfigGetter: runs `terminal length 0` then
// `show running-config`, and returns the second command's output verbatim
// -- no parsing, this is the read-only half of Phase 3's config seam.
//
// "terminal length 0" is sent proactively rather than relying on
// transport/promptdetector.go's generic pagination-marker fallback:
// confirmed live against a real Cisco ISRv that a multi-page
// "show running-config" (device reported 6703 bytes) got silently
// truncated after the first page with no error surfaced at all -- exactly
// the documented fallback behavior when a page boundary's quiescence
// pause doesn't get recognized as pagination (see waitForPrompt's
// pagination-marker-miss comment). CiscoIOSDriver knows it's Cisco and
// knows the fix, so it applies it proactively for its own commands rather
// than depending on best-effort vendor-agnostic detection.
//
// Config push is LoadConfig, below (the ConfigLoader capability).
func (d *CiscoIOSDriver) GetConfig() (string, error) {
	results, err := d.RunCommands([]string{"terminal length 0", "show running-config"})
	if err != nil {
		return "", err
	}
	if len(results) < 2 || results[0].Error != "" || results[1].Error != "" {
		return "", olterr.New(
			olterr.TypeProtocol,
			`GetConfig: "terminal length 0" or "show running-config" failed -- see the command results for the actual error`,
		)
	}
	if results[1].Output == nil {
		return "", nil
	}
	return *results[1].Output, nil
}

// interfaceHeaderPattern matches the header line `show interfaces`
// prints once per interface, e.g.:
//
//	GigabitEthernet1 is up, line protocol is up
//	GigabitEthernet3 is administratively down, line protocol is down
//
// Confirmed live against a real Cisco ISRv (IOS-XE 17.01.01) -- each
// header line has a trailing space before the newline, which the (.+?)
// non-greedy admin-status group and the rest of the parsing below don't
// need to care about. The admin-status group captures everything between
// "is " and ", line protocol is" -- "up" or "administratively down" are
// the two observed live, but this doesn't hardcode an exhaustive list
// (isEnabled below just checks for the substring "administratively
// down"), so it degrades gracefully on any other admin-status text
// (isEnabled defaults true, matching "the device didn't tell us it's
// administratively disabled").
var interfaceHeaderPattern = regexp.MustCompile(`(?m)^(\S+) is (.+?), line protocol is (\S+)`)

// ifaceMACPattern matches, e.g.:
//
//	Hardware is CSR vNIC, address is 5000.0001.0000 (bia 5000.0001.0000)
//
// Confirmed live -- Cisco's dotted-triplet MAC notation (4 hex digits,
// dot, 4 hex digits, dot, 4 hex digits), not colon-separated octets.
var ifaceMACPattern = regexp.MustCompile(`address is ([0-9a-fA-F]{4}\.[0-9a-fA-F]{4}\.[0-9a-fA-F]{4})`)

// ifaceDescriptionPattern matches, e.g.:
//
//	Description: sample-desc-for-parsing
//
// Confirmed live: this line is entirely ABSENT from the block when no
// description is configured (not printed as "Description: " with nothing
// after it) -- so a block with no match here correctly leaves
// InterfaceInfo.Description as the zero value "", not a false match.
var ifaceDescriptionPattern = regexp.MustCompile(`(?m)^\s*Description: (.+)$`)

// ifaceMTUPattern/ifaceBWPattern match, e.g.:
//
//	MTU 1500 bytes, BW 1000000 Kbit/sec, DLY 10 usec,
//
// Confirmed live. BW is in Kbit/sec; SpeedMbps below divides by 1000 to
// match NAPALM's get_interfaces() convention (speed reported in Mbps).
var (
	ifaceMTUPattern = regexp.MustCompile(`MTU (\d+) bytes`)
	ifaceBWPattern  = regexp.MustCompile(`BW (\d+) Kbit`)
)

// ifaceIPAddressPattern matches, e.g.:
//
//	Internet address is 192.168.14.13/24
//
// Confirmed live -- same "line entirely absent when not configured"
// convention as Description (Cisco's "no ip address" interfaces simply
// don't print this line at all, rather than printing it empty), so a
// block with no match here correctly leaves InterfaceInfo.IPAddress as
// the zero value "". Captured as the single "address/prefixlen" token
// Cisco prints it as, not split into separate IP/prefix-length fields.
var ifaceIPAddressPattern = regexp.MustCompile(`Internet address is (\S+)`)

// parseInterfaces splits `show interfaces` output into per-interface
// blocks at each interfaceHeaderPattern match (a block runs from one
// header line up to, but not including, the next header line or the end
// of the output), then parses each block independently. A field's regex
// simply not matching within a block (e.g. no Description: line) leaves
// that InterfaceInfo field at its zero value rather than erroring --
// same "degrade gracefully, don't fail the whole parse" posture as
// GetFacts().
func parseInterfaces(output string) map[string]InterfaceInfo {
	matches := interfaceHeaderPattern.FindAllStringSubmatchIndex(output, -1)
	result := make(map[string]InterfaceInfo, len(matches))

	for i, m := range matches {
		blockStart := m[0]
		blockEnd := len(output)
		if i+1 < len(matches) {
			blockEnd = matches[i+1][0]
		}
		block := output[blockStart:blockEnd]

		name := output[m[2]:m[3]]
		adminStatus := output[m[4]:m[5]]
		protocolStatus := output[m[6]:m[7]]

		info := InterfaceInfo{
			IsUp:      strings.EqualFold(protocolStatus, "up"),
			IsEnabled: !strings.Contains(strings.ToLower(adminStatus), "administratively down"),
		}
		if mm := ifaceMACPattern.FindStringSubmatch(block); mm != nil {
			info.MACAddress = mm[1]
		}
		if mm := ifaceDescriptionPattern.FindStringSubmatch(block); mm != nil {
			info.Description = strings.TrimSpace(mm[1])
		}
		if mm := ifaceIPAddressPattern.FindStringSubmatch(block); mm != nil {
			info.IPAddress = mm[1]
		}
		if mm := ifaceMTUPattern.FindStringSubmatch(block); mm != nil {
			if mtu, err := strconv.Atoi(mm[1]); err == nil {
				info.MTU = mtu
			}
		}
		if mm := ifaceBWPattern.FindStringSubmatch(block); mm != nil {
			if bwKbit, err := strconv.ParseFloat(mm[1], 64); err == nil {
				info.SpeedMbps = bwKbit / 1000
			}
		}

		result[name] = info
	}
	return result
}

// GetInterfaces implements InterfaceProvider: runs `terminal length 0`
// then `show interfaces` (the FULL per-interface output, not `show ip
// interface brief` -- that command alone doesn't carry MTU/speed/
// description/MAC), and parses it into structured per-interface state,
// the same job NAPALM's get_interfaces() getter does. "terminal length
// 0" is sent proactively for the same reason GetConfig sends it --
// confirmed live that a multi-page response can silently truncate
// without it (see GetConfig's doc comment).
func (d *CiscoIOSDriver) GetInterfaces() (map[string]InterfaceInfo, error) {
	results, err := d.RunCommands([]string{"terminal length 0", "show interfaces"})
	if err != nil {
		return nil, err
	}
	if len(results) < 2 || results[0].Error != "" || results[1].Error != "" {
		return nil, olterr.New(
			olterr.TypeProtocol,
			`GetInterfaces: "terminal length 0" or "show interfaces" failed -- see the command results for the actual error`,
		)
	}
	output := ""
	if results[1].Output != nil {
		output = *results[1].Output
	}
	return parseInterfaces(output), nil
}

// lldpNeighborHeaderPattern matches the column-header row of `show lldp
// neighbors`, e.g.:
//
//	Device ID           Local Intf     Hold-time  Capability      Port ID
//
// Confirmed live -- this exact header line, and the "Total entries
// displayed: 0" trailer (lldpTotalEntriesPattern, below), both verified
// against a real Cisco ISRv (IOS-XE 17.01.01) after enabling LLDP
// (`lldp run`). The DATA ROW shape below this header has NOT been
// confirmed live -- this lab environment has no other LLDP-speaking
// device on the same segment to produce a populated table. Built
// against the well-documented Cisco IOS format; validate against real
// neighbor data before relying on it in production (same posture
// GetFacts()'s original parser carried before parts of it got
// live-verified).
var lldpNeighborHeaderPattern = regexp.MustCompile(`(?i)^Device ID\s+Local Intf`)

// lldpTotalEntriesPattern matches the trailer line, e.g. "Total entries
// displayed: 0" -- confirmed live, marks the end of the neighbor table
// (present even with zero neighbors).
var lldpTotalEntriesPattern = regexp.MustCompile(`(?i)^Total entries displayed:`)

// parseLLDPNeighbors parses `show lldp neighbors` output into a map
// keyed by local interface name. Skips everything up to and including
// the column-header line, then reads one neighbor per line until a
// blank line or the "Total entries displayed" trailer (whichever comes
// first -- confirmed live for the zero-neighbor case, where the trailer
// follows the header with only a blank line between). Each row is split
// on whitespace: device ID is the first field, port ID is the LAST field
// (robust to the Capability column's variable width, e.g. "B", "B,R",
// "B,R,T"), local interface is the second field.
//
// Known, undocumented-format limitation (can't be verified without a
// real populated table): Cisco can wrap a long Device ID onto its own
// line with the rest of the row continuing on the next -- this doesn't
// attempt to reassemble that, and a row that doesn't split into at least
// 4 fields is simply skipped rather than misparsed.
func parseLLDPNeighbors(output string) map[string][]LLDPNeighbor {
	result := make(map[string][]LLDPNeighbor)
	inTable := false

	for _, raw := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(raw, "\r"))

		if !inTable {
			if lldpNeighborHeaderPattern.MatchString(trimmed) {
				inTable = true
			}
			continue
		}
		if trimmed == "" || lldpTotalEntriesPattern.MatchString(trimmed) {
			break
		}

		fields := strings.Fields(trimmed)
		if len(fields) < 4 {
			continue
		}
		localIntf := fields[1]
		result[localIntf] = append(result[localIntf], LLDPNeighbor{
			RemoteSystemName: fields[0],
			RemotePortID:     fields[len(fields)-1],
		})
	}

	return result
}

// GetLLDPNeighbors implements LLDPNeighborsProvider: runs `terminal
// length 0` then `show lldp neighbors`, and parses the resulting table --
// the same job NAPALM's get_lldp_neighbors() getter does. Requires LLDP
// to actually be enabled on the device (`lldp run` globally) -- if it
// isn't, the device replies "% LLDP is not enabled" (confirmed live),
// which this surfaces as a distinct, descriptive error rather than a
// misleadingly-empty result, so a caller can tell "no neighbors seen"
// apart from "LLDP isn't even on".
func (d *CiscoIOSDriver) GetLLDPNeighbors() (map[string][]LLDPNeighbor, error) {
	results, err := d.RunCommands([]string{"terminal length 0", "show lldp neighbors"})
	if err != nil {
		return nil, err
	}
	if len(results) < 2 || results[0].Error != "" || results[1].Error != "" {
		return nil, olterr.New(
			olterr.TypeProtocol,
			`GetLLDPNeighbors: "terminal length 0" or "show lldp neighbors" failed -- see the command results for the actual error`,
		)
	}
	output := ""
	if results[1].Output != nil {
		output = *results[1].Output
	}
	if strings.Contains(output, "LLDP is not enabled") {
		return nil, olterr.New(
			olterr.TypeValidation,
			`GetLLDPNeighbors: LLDP is not enabled on this device -- run "lldp run" (global config) to enable it first`,
		)
	}
	return parseLLDPNeighbors(output), nil
}

// mergeConfigDisclaimer is prepended to every diff LoadConfig returns --
// deliberately in the data itself, not just README prose, so it's in
// front of whoever is about to decide to apply, not just read once at
// install time. See LoadConfig's doc comment for why this distinction is
// load-bearing, not cosmetic.
const mergeConfigDisclaimer = "NOTE: this is a merge/overlay diff, not a replace-config diff -- " +
	"lines shown removed (-) will NOT be deleted from the device, only added/changed (+) lines are pushed.\n\n"

// multiLineDirectivePattern matches config directives that open a
// terminator-delimited block (banner text, certificate/key material)
// rather than a single command. LoadConfig replays a candidate one line
// at a time through the same per-command prompt-detection every other
// command uses (see applyMergeConfig) -- that has no way to recognize
// "still inside a block, keep accumulating" the way a real terminal
// emulator would. Attempting to replay these naively risks hanging the
// session (waiting for a prompt that won't reappear until the block's own
// terminator is typed) or sending the block's body lines as if they were
// separate top-level commands. Refused outright (fail closed, before ever
// connecting) rather than attempted best-effort.
var multiLineDirectivePattern = regexp.MustCompile(`(?im)^\s*(banner\s|crypto\s+pki\s+certificate\s+chain\b|crypto\s+key\s+generate\b)`)

// findMultiLineDirective returns the first offending line, or "" if none.
func findMultiLineDirective(candidate string) string {
	for _, line := range strings.Split(candidate, "\n") {
		if multiLineDirectivePattern.MatchString(line) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// parseCandidateLines splits a full candidate config into the lines
// LoadConfig will actually replay: blank lines and Cisco's "!" section
// separators are stripped (visual formatting only, not real commands),
// and so is a trailing "end" -- `show running-config` always prints one
// as a sentinel marking the end of the config block, not because "end"
// was itself a stored command. Since candidate is expected to be "a prior
// GetConfig() output, edited" (see LoadConfig's doc comment), it will
// normally carry this sentinel; replaying it as a real command would exit
// config mode one line early and collide with applyMergeConfig's own
// controlled "end" at the close of the sequence.
func parseCandidateLines(candidate string) []string {
	var lines []string
	for _, line := range strings.Split(candidate, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed == "" || trimmed == "!" || strings.EqualFold(trimmed, "end") {
			continue
		}
		lines = append(lines, trimmed)
	}
	return lines
}

// computeUnifiedDiff renders a standard unified diff (like `diff -u`)
// between the device's current config and candidate.
func computeUnifiedDiff(current, candidate string) (string, error) {
	return difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(current),
		B:        difflib.SplitLines(candidate),
		FromFile: "current running-config",
		ToFile:   "candidate",
		Context:  3,
	})
}

// configHashOf fingerprints a config so LoadConfig's dry-run/apply pair
// can detect "the device changed since your dry-run" (see LoadConfig's
// doc comment) -- SHA-256 of the raw text, nothing fancier is needed for
// an equality check.
func configHashOf(config string) string {
	sum := sha256.Sum256([]byte(config))
	return hex.EncodeToString(sum[:])
}

var (
	// ciscoRejectionPrefixPattern requires "% " (a literal space) after
	// the percent sign, matching Cisco's interactive rejection messages
	// (e.g. "% Invalid input detected at '^' marker.") while deliberately
	// NOT matching its informational syslog-style messages, which have no
	// space after "%" (e.g. "%SYS-5-CONFIG_I: Configured from console by
	// vty0") -- those are confirmation the command succeeded, not a
	// rejection, and would false-positive against a bare "%" prefix.
	ciscoRejectionPrefixPattern = regexp.MustCompile(`(?m)^%\s`)

	// ciscoRejectionSubstrings catches rejections that don't use the "%"
	// convention at all -- notably AAA command-authorization failures,
	// which can reject a command with no "%" prefix whatsoever.
	ciscoRejectionSubstrings = []string{
		"Command authorization failed",
		"Command rejected",
	}
)

// detectRejection inspects one apply-mode CommandResult and returns a
// non-empty human-readable reason if the device appears to have rejected
// it. Cisco reports a bad command as normal OUTPUT text, not as a
// transport-level failure -- RunCommands alone can't tell "accepted" from
// "rejected", so this is a semantic, output-scanning check layered on top.
//
// Also treats Truncated as a rejection: config-mode commands don't
// normally paginate, so truncation here is itself an anomaly (the same
// class of bug GetConfig's doc comment describes finding live for `show
// running-config`) -- safer to abort than trust partial output.
func detectRejection(result transport.CommandResult) string {
	if result.Truncated {
		return "response was truncated -- treating as a failed/uncertain apply rather than trusting partial output"
	}
	if result.Output == nil {
		return ""
	}
	output := *result.Output
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if ciscoRejectionPrefixPattern.MatchString(trimmed) {
			return trimmed
		}
		for _, substr := range ciscoRejectionSubstrings {
			if strings.Contains(trimmed, substr) {
				return trimmed
			}
		}
	}
	return ""
}

// runSingle runs exactly one command and returns its CommandResult.
// Deliberately one RunCommands([]string{command}) call at a time, NOT one
// batched multi-command call -- BaseDriver.RunCommands only stops early
// on a transport-level failure, it does not inspect Output for a Cisco
// rejection, so a batched call would send every subsequent line
// regardless of an earlier rejection. Calling one at a time from
// applyMergeConfig's loop, checking each result before sending the next,
// is what makes fail-fast on rejection actually fail fast on the wire.
func (d *CiscoIOSDriver) runSingle(command string) (transport.CommandResult, error) {
	results, err := d.RunCommands([]string{command})
	if err != nil {
		return transport.CommandResult{}, err
	}
	if len(results) < 1 {
		return transport.CommandResult{}, olterr.New(olterr.TypeProtocol, "no result returned for command "+command)
	}
	return results[0], nil
}

// applyMergeConfig enters config mode, replays lines one at a time
// (see runSingle's doc comment for why one at a time), and exits config
// mode -- unless a rejection or connection loss stops it early. Three
// distinct outcomes, all meaningful to the caller:
//
//  1. err != nil: a transport-level failure (timeout/session_closed) hit
//     mid-apply. The session is in an indeterminate state (see
//     ShellSession's contract) -- "end" is deliberately NOT attempted,
//     since the device may still be mid-response to something. appliedCount
//     tells the caller how many lines got sent before this happened, but
//     NOT whether the device actually accepted all of them.
//  2. rejection != "" (err == nil): the device cleanly rejected a line.
//     "end" WAS sent (best-effort) to leave the device in a known, clean
//     exec-mode state before returning.
//  3. Neither: every line was accepted and "end" succeeded.
func (d *CiscoIOSDriver) applyMergeConfig(lines []string) (appliedCount int, rejection string, err error) {
	entry, err := d.runSingle("configure terminal")
	if err != nil {
		return 0, "", err
	}
	if entry.Error != "" {
		return 0, "", olterr.New(olterr.TypeConnection, "connection lost entering config mode: "+entry.Error)
	}
	if reason := detectRejection(entry); reason != "" {
		return 0, "entering config mode: " + reason, nil
	}

	for i, line := range lines {
		result, err := d.runSingle(line)
		if err != nil {
			return appliedCount, "", err
		}
		if result.Error != "" {
			return appliedCount, "", olterr.New(
				olterr.TypeConnection,
				fmt.Sprintf(
					"connection lost after applying %d of %d lines (line %d, %q: %s) -- "+
						"device state not verified, re-run GetConfig before retrying",
					appliedCount, len(lines), i+1, line, result.Error,
				),
			)
		}
		if reason := detectRejection(result); reason != "" {
			_, _ = d.runSingle("end") // best-effort clean exit; its own outcome isn't reported
			return appliedCount, fmt.Sprintf("line %d (%q): %s", i+1, line, reason), nil
		}
		appliedCount++
	}

	end, err := d.runSingle("end")
	if err != nil {
		return appliedCount, "", err
	}
	if end.Error != "" {
		return appliedCount, "", olterr.New(
			olterr.TypeConnection,
			"connection lost exiting config mode after applying all "+strconv.Itoa(appliedCount)+" lines: "+end.Error,
		)
	}
	return appliedCount, "", nil
}

// LoadConfig implements ConfigLoader -- see the interface doc comment in
// driver.go for the dry-run/hash-echo contract, and mergeConfigDisclaimer
// for why this is a merge, not a replace.
//
// candidate should be a full config (e.g. a prior GetConfig() call,
// edited) -- diffing a partial snippet against the full current config
// would just show most of the device's real config as "removed" noise,
// not a meaningful diff. Refuses candidates containing a directive
// matched by multiLineDirectivePattern (fail closed) rather than
// attempting a best-effort replay that could hang the session or
// misapply.
func (d *CiscoIOSDriver) LoadConfig(candidate string, dryRun bool, expectedConfigHash string) (string, string, bool, error) {
	if directive := findMultiLineDirective(candidate); directive != "" {
		return "", "", false, olterr.New(
			olterr.TypeValidation,
			fmt.Sprintf(
				"candidate contains a multi-line directive (%q) gearctl cannot safely replay one line at a time -- refusing",
				directive,
			),
		)
	}

	current, err := d.GetConfig()
	if err != nil {
		return "", "", false, err
	}
	currentHash := configHashOf(current)

	diff, err := computeUnifiedDiff(current, candidate)
	if err != nil {
		return "", currentHash, false, olterr.New(olterr.TypeProtocol, "failed to compute diff: "+err.Error())
	}
	diff = mergeConfigDisclaimer + diff

	if dryRun {
		return diff, currentHash, false, nil
	}

	if expectedConfigHash == "" {
		return diff, currentHash, false, olterr.New(
			olterr.TypeValidation,
			"expectedConfigHash is required to apply -- call with dryRun=true first and pass back the configHash it returns",
		)
	}
	if expectedConfigHash != currentHash {
		return diff, currentHash, false, olterr.New(
			olterr.TypeValidation,
			"device config has changed since the dry-run this hash came from -- re-run dry-run and review the new diff before applying",
		)
	}

	lines := parseCandidateLines(candidate)
	appliedCount, rejection, err := d.applyMergeConfig(lines)
	if err != nil {
		return diff, currentHash, false, err
	}
	if rejection != "" {
		return diff, currentHash, false, olterr.New(
			olterr.TypeValidation,
			fmt.Sprintf(
				"device rejected the candidate after applying %d of %d lines (%s) -- "+
					"config mode was exited cleanly, no further lines were attempted",
				appliedCount, len(lines), rejection,
			),
		)
	}
	return diff, currentHash, true, nil
}

// mergeCmdDisclaimer is prepended to every diff LoadMergeCommands
// returns, unconditionally -- every call, not just when a replacement is
// actually detected. This is the fix (per design review) for "no
// replacement flagged" being misread as "verified safe": the disclaimer
// always states what kind of check this is and its limits, so silence
// from the heuristic is never visually indistinguishable from a real
// safety guarantee.
const mergeCmdDisclaimer = "NOTE: this is a PROJECTED PREVIEW using simple first-token (or first-two-token " +
	"for \"ip ...\" commands) key-matching within the same config section to guess at replacements -- " +
	"NOT real Cisco command-grammar parsing. It can miss replacements for less common command shapes. " +
	"Review carefully before applying, especially for anything not a straightforward description/address/" +
	"state-style change.\n\n"

// stanzaRange locates one stanza's indented body within a mutable copy
// of the current config's raw lines (as split by "\n", each entry the
// UNMODIFIED original line -- "\r" and all): [bodyStart, bodyEnd) is the
// range of line indices making up its body, in the ORIGINAL line order.
// Deliberately does NOT record the header's own line index -- nothing in
// this package ever needs to touch a stanza's header line, only splice/
// replace/remove within its body.
type stanzaRange struct {
	bodyStart int // inclusive
	bodyEnd   int // exclusive
}

// locateLines scans a Cisco config's lines (as split by "\n") and
// returns: byHeader, a stanza's trimmed header text -> its stanzaRange;
// and globalLineIdxs, the indices of every top-level line that ISN'T a
// stanza header (bare global commands like "hostname", "ip domain
// name", ...). Keyed on INDENTATION ALONE -- not a curated keyword list.
// `current` genuinely has indentation (it comes from `show
// running-config`), so this generalizes for free to any block type
// (interface, router, line, ip access-list, route-map, class-map,
// policy-map, VRF definitions, ...) without needing to name them.
//
// Deliberately returns LINE INDICES into the caller's own `lines` slice
// rather than copied content -- projectMergedConfig mutates that same
// slice in place (replacing/removing/inserting only the specific lines a
// delta command actually changes) and re-derives this index fresh after
// every mutation (see projectMergedConfig's doc comment for why a full
// O(n) rescan per command is the right tradeoff here). That is what
// makes every line NOT touched by the delta come out of the projection
// byte-identical to `current`, in its exact original position -- fixing
// a real-hardware finding where an earlier version of this function
// fully reparsed and reserialized the whole config from a normalized
// model, which collapsed every global command into one block rendered
// first, reordering anything that originally appeared after an interface
// stanza and turning a single one-line change into a diff of the entire
// file. See TestProjectMergedConfigPreservesUnrelatedLineOrder.
//
// The curated context-opener list (see isContextOpener) is reserved for
// the DELTA side of LoadMergeCommands, which has no indentation to read
// -- deliberately not reused here, since these solve structurally
// different problems (read nesting off whitespace vs. infer nesting from
// recognized keywords) and conflating them would mean keeping two lists
// in sync as either ever expands. It's still consulted below, but only
// as the narrow fallback for one ambiguous case: a stanza that currently
// has zero body lines (every setting at default, or one this same
// function just emptied via a "no" removal) is textually indistinguishable
// from a standalone global command -- both are one non-indented line with
// nothing indented following it.
func locateLines(lines []string) (byHeader map[string]stanzaRange, globalLineIdxs []int) {
	byHeader = make(map[string]stanzaRange)
	isIndented := func(raw string) bool {
		return len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\t')
	}

	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "!" || strings.EqualFold(trimmed, "end") || isIndented(line) {
			continue
		}

		bodyStart := i + 1
		bodyEnd := bodyStart
		for k := bodyStart; k < len(lines); k++ {
			kTrimmed := strings.TrimSpace(strings.TrimRight(lines[k], "\r"))
			if kTrimmed == "" || !isIndented(lines[k]) {
				break
			}
			bodyEnd = k + 1
		}

		if bodyEnd > bodyStart || isContextOpener(trimmed) {
			byHeader[trimmed] = stanzaRange{bodyStart: bodyStart, bodyEnd: bodyEnd}
		} else {
			globalLineIdxs = append(globalLineIdxs, i)
		}
	}
	return byHeader, globalLineIdxs
}

// contextOpenerPrefixes: recognized block-opening commands, used ONLY to
// track position within a DELTA (which lacks current's real indentation
// -- see locateLines's doc comment for why this is a separate,
// deliberately small list, not shared with the indentation-based parser).
// Not a claim of exhaustive Cisco command-grammar coverage.
var contextOpenerPrefixes = []string{"interface ", "router ", "line "}

func isContextOpener(line string) bool {
	for _, prefix := range contextOpenerPrefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// matchKey returns the "key" used to decide whether a delta line
// replaces an existing line in the same stanza: the first token, except
// when that token is "ip" -- a heavily overloaded Cisco prefix ("ip
// address", "ip helper-address", "ip access-group", "ip nat", ... are
// all genuinely different commands, so first-token-only matching would
// wrongly treat them as the same key) -- where the first two tokens are
// used instead.
func matchKey(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	if len(fields) > 1 && strings.EqualFold(fields[0], "ip") {
		return strings.ToLower(fields[0] + " " + fields[1])
	}
	return strings.ToLower(fields[0])
}

// firstToken returns just the first whitespace-delimited token, or "" for
// an empty string -- used for "no <rest>" removal matching (§3 of the
// design), which is deliberately looser than matchKey's ip-prefix special
// case: Cisco's "no" convention is general enough that first-token-only
// is the right granularity for "what is this undoing".
func firstToken(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// mergeOperation records what one delta command actually did to the
// projection, so reverseCommands (below) can derive an undo command from
// it -- kind is one of "replace"/"add"/"remove"/"no-op", appliedLine is
// the delta command itself (used to build "add"'s inverse), previousLine
// is what was there before (for "replace"/"remove", the exact original
// content to restore), and context is the stanza header this happened
// under ("" for global). Preview-only, same as everything else in this
// file -- never sent to the device directly; see reverseCommands' own
// doc comment for how it's actually used.
type mergeOperation struct {
	kind         string
	context      string
	appliedLine  string
	previousLine string
}

// projectMergedConfig simulates what current's config would look like
// after commands is applied, for DIFF/PREVIEW PURPOSES ONLY -- it is
// never sent to the device. Actual apply always replays the literal
// commands via applyMergeConfig, completely independent of this
// projection, so a bug here can only produce a misleading preview, never
// an incorrect command sent to the device. Also returns the per-command
// mergeOperation log (see reverseCommands) driving LoadMergeCommands'
// reverseCommands output.
//
// Operates on a mutable copy of current's own lines (split on "\n"),
// splicing/replacing/inserting only the specific lines a command
// actually changes -- everything else in the file is never touched, so
// it comes out of the projection byte-identical to current, in its exact
// original position. This is the deliberate difference from an earlier
// version of this function, which fully reparsed and reserialized the
// whole config from a normalized stanza model -- see locateLines's doc
// comment for the real-hardware bug that caused (a single one-line
// change producing a diff of the entire file) and why per-command
// re-derivation of stanza/global-line positions, rather than a one-shot
// parse, is what fixes it.
//
// Walks commands in order, tracking a "current context" (starts global,
// i.e. ""): a line matching isContextOpener REPLACES the tracked context
// (does not nest under whatever was open before -- real deltas commonly
// switch e.g. "interface GigabitEthernet1" straight to "interface
// GigabitEthernet2" with no "exit" in between); "exit"/"end" pop back to
// global. For every other line: a "no <rest>" line removes an existing
// line in the current context whose first token matches <rest>'s first
// token, if one exists (Cisco's general undo convention, not a curated
// per-command list); anything else looks for an existing line in the
// current context sharing the same matchKey and REPLACES it in place
// (preserving position, so the diff shows a clean adjacent -old/+new
// pair) if found, or INSERTS as a new line -- right after the context's
// last existing line (or a brand new stanza, appended at the very end of
// the file, if the context doesn't exist in current at all) -- if not.
//
// If a line's context has no corresponding stanza in current at all
// (e.g. a bare "description" with no prior opener, and current has no
// global "description" line), it's simply treated as a plain addition --
// this function does not fail or warn. On the real device this is itself
// invalid input at the wrong context level, which detectRejection (not
// this projection) catches and aborts cleanly during apply.
func projectMergedConfig(current string, commands []string) (string, []mergeOperation) {
	lines := strings.Split(current, "\n")
	var ops []mergeOperation

	context := ""
	for _, cmd := range commands {
		if isContextOpener(cmd) {
			context = cmd
			continue
		}
		if strings.EqualFold(cmd, "exit") || strings.EqualFold(cmd, "end") {
			context = ""
			continue
		}

		// Re-derived fresh before every command: a prior command in this
		// same loop may have inserted/removed a line, shifting every
		// index after it. Command lists are small (a handful of lines
		// per -merge-cmd call), so this O(n) rescan per command is cheap
		// -- and it sidesteps ever having to incrementally patch stored
		// indices by hand.
		byHeader, globalLineIdxs := locateLines(lines)

		if context == "" {
			var op mergeOperation
			lines, op = applyGlobalCommand(lines, globalLineIdxs, cmd)
			ops = append(ops, op)
			continue
		}

		rng, ok := byHeader[context]
		if !ok {
			// A brand-new stanza this delta introduces -- appended as a
			// whole new block at the very end, consistent with "content
			// this delta genuinely adds with no natural existing position
			// shows up at the bottom" (same treatment new global lines
			// get, in applyGlobalCommand). Its inverse (see
			// reverseCommands) is still just "no <cmd>" under this
			// context, same as any other addition -- it leaves an empty
			// stanza behind rather than removing the interface entirely;
			// a known, documented limitation, not a bug.
			lines = append(lines, context, " "+cmd, "!")
			ops = append(ops, mergeOperation{kind: "add", context: context, appliedLine: cmd})
			continue
		}
		var op mergeOperation
		lines, op = applyStanzaCommand(lines, rng, cmd)
		op.context = context
		ops = append(ops, op)
	}

	return strings.Join(lines, "\n"), ops
}

// applyGlobalCommand reconciles cmd (already known to be in the global,
// i.e. "", context) against the top-level lines listed in
// globalLineIdxs -- same reconciliation rules as applyStanzaCommand,
// just at the top level, and with no leading-space indent added to a new
// or replaced line (global commands aren't indented). The returned
// mergeOperation's context is always left "" (the caller doesn't need to
// set it, unlike applyStanzaCommand's).
func applyGlobalCommand(lines []string, globalLineIdxs []int, cmd string) ([]string, mergeOperation) {
	if rest, isNo := strings.CutPrefix(cmd, "no "); isNo {
		key := firstToken(rest)
		for _, idx := range globalLineIdxs {
			line := strings.TrimSpace(lines[idx])
			if strings.EqualFold(firstToken(line), key) {
				return append(lines[:idx], lines[idx+1:]...), mergeOperation{kind: "remove", appliedLine: cmd, previousLine: line}
			}
		}
		return lines, mergeOperation{kind: "no-op", appliedLine: cmd}
	}

	key := matchKey(cmd)
	for _, idx := range globalLineIdxs {
		line := strings.TrimSpace(lines[idx])
		if matchKey(line) == key {
			lines[idx] = cmd
			return lines, mergeOperation{kind: "replace", appliedLine: cmd, previousLine: line}
		}
	}
	return append(lines, cmd), mergeOperation{kind: "add", appliedLine: cmd}
}

// applyStanzaCommand reconciles cmd against one stanza's body range
// [rng.bodyStart, rng.bodyEnd) only -- the header line itself, and
// everything outside this range (before or after the stanza, in any
// other stanza), is never touched. "no <rest>" removes a matching
// existing child line by first token; anything else replaces an
// existing child line sharing the same matchKey in place, or is
// inserted as a new child line (indented by one space, matching Cisco's
// own convention) right after the stanza's last existing child -- or
// right after its header, if it currently has none (e.g. a freshly
// created stanza, or one whose only line a prior "no" just removed).
// The returned mergeOperation's context is left unset -- projectMergedConfig
// (the only caller) fills it in, since it's the one that knows which
// stanza's context this call was made for.
func applyStanzaCommand(lines []string, rng stanzaRange, cmd string) ([]string, mergeOperation) {
	if rest, isNo := strings.CutPrefix(cmd, "no "); isNo {
		key := firstToken(rest)
		for i := rng.bodyStart; i < rng.bodyEnd; i++ {
			line := strings.TrimSpace(lines[i])
			if strings.EqualFold(firstToken(line), key) {
				return append(lines[:i], lines[i+1:]...), mergeOperation{kind: "remove", appliedLine: cmd, previousLine: line}
			}
		}
		return lines, mergeOperation{kind: "no-op", appliedLine: cmd}
	}

	key := matchKey(cmd)
	for i := rng.bodyStart; i < rng.bodyEnd; i++ {
		line := strings.TrimSpace(lines[i])
		if matchKey(line) == key {
			lines[i] = " " + cmd
			return lines, mergeOperation{kind: "replace", appliedLine: cmd, previousLine: line}
		}
	}

	insertAt := rng.bodyEnd
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:insertAt]...)
	out = append(out, " "+cmd)
	out = append(out, lines[insertAt:]...)
	return out, mergeOperation{kind: "add", appliedLine: cmd}
}

// reverseCommands derives the commands that would undo ops -- the
// vendor-agnostic, no-device-prerequisites building block for a caller
// (e.g. platform-api) wanting "commit confirmed" semantics: apply a
// merge, start its own timer, and if not explicitly confirmed within N
// minutes, call LoadMergeCommands again with THESE commands to revert.
// gearctl stays stateless (no timer, no memory of past runs -- see
// README.md's Architecture section) -- the caller owns the confirm
// window and the decision to actually replay this.
//
// Walked in REVERSE order (the last change applied is undone first, the
// same convention any transaction-log rollback uses): "replace"/"remove"
// invert to re-establishing previousLine exactly as it was;
// "add" inverts to "no "+appliedLine (Cisco's general undo convention,
// consistent with how projectMergedConfig itself interprets "no ...");
// "no-op" (a "no <cmd>" that matched nothing, so nothing was actually
// undone) has no inverse. A context is re-emitted before EVERY command
// that needs one, even for consecutive commands under the same stanza --
// deliberately not deduplicated for simplicity; a redundant
// "interface X" re-entry is a harmless no-op on real hardware, and this
// avoids any chance of a subtle context-tracking bug in the reverse
// direction.
//
// Same disclaimer as the forward projection: this is a heuristic
// derived from the SAME reconciliation logic, not real Cisco
// command-grammar parsing, and inherits its limitations (e.g. a reversed
// brand-new-stanza addition leaves an empty stanza behind rather than
// removing the interface entirely). Never sent to the device by
// LoadMergeCommands itself -- returned for the CALLER to replay, on
// purpose, via its own subsequent LoadMergeCommands call, going through
// the exact same dry-run/hash-freshness/apply safety machinery as any
// other merge.
func reverseCommands(ops []mergeOperation) []string {
	var out []string
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		var inverse string
		switch op.kind {
		case "replace", "remove":
			inverse = op.previousLine
		case "add":
			inverse = "no " + op.appliedLine
		default: // "no-op" -- nothing was actually changed, nothing to undo
			continue
		}
		if op.context != "" {
			out = append(out, op.context)
		}
		out = append(out, inverse)
	}
	return out
}

// LoadMergeCommands implements ConfigLoader's delta-based entry point --
// see driver.go's ConfigLoader doc comment for the shared dry-run/
// hash-freshness/apply contract (identical to LoadConfig's).
//
// CRITICAL, per the design review that caught this: this must NEVER hand
// a current+commands synthesized blob to LoadConfig's own
// candidate-handling logic (findMultiLineDirective/parseCandidateLines/
// applyMergeConfig all operate on ONE opaque string there). Below,
// findMultiLineDirective and applyMergeConfig are called with the delta
// (commands / lines) ONLY -- current's own text is used exclusively to
// build the projected diff, never scanned for directives or replayed.
// Getting this backwards would mean apply replays the device's ENTIRE
// existing config back at itself (not just the delta), and the feature
// would refuse to run at all on any device that already has a banner
// configured, even though the banner isn't part of the delta.
//
// The diff shown is a PROJECTED PREVIEW (see projectMergedConfig) -- a
// heuristic reconciliation of commands against current, not a literal
// text diff of the input (diffing a small delta directly against the
// full current config would be meaningless noise: nearly everything in
// current would show as "removed"). See mergeCmdDisclaimer for the
// heuristic's explicit, always-stated limits.
//
// reverseCommands (see its own doc comment and the ConfigLoader
// interface's) is derived from the SAME per-command operation log the
// projection builds, and is populated on every call -- dry run or apply
// -- same as diff/configHash, so a caller can review "here's what
// undoing this would look like" before ever applying.
func (d *CiscoIOSDriver) LoadMergeCommands(
	commands []string, dryRun bool, expectedConfigHash string,
) (diff string, reverseCmds []string, configHash string, applied bool, err error) {
	joined := strings.Join(commands, "\n")
	if directive := findMultiLineDirective(joined); directive != "" {
		return "", nil, "", false, olterr.New(
			olterr.TypeValidation,
			fmt.Sprintf(
				"candidate contains a multi-line directive (%q) gearctl cannot safely replay one line at a time -- refusing",
				directive,
			),
		)
	}
	lines := parseCandidateLines(joined)

	current, err := d.GetConfig()
	if err != nil {
		return "", nil, "", false, err
	}
	currentHash := configHashOf(current)

	projected, ops := projectMergedConfig(current, lines)
	reverseCmds = reverseCommands(ops)
	diff, err = computeUnifiedDiff(current, projected)
	if err != nil {
		return "", reverseCmds, currentHash, false, olterr.New(olterr.TypeProtocol, "failed to compute diff: "+err.Error())
	}
	diff = mergeCmdDisclaimer + diff

	if dryRun {
		return diff, reverseCmds, currentHash, false, nil
	}

	if expectedConfigHash == "" {
		return diff, reverseCmds, currentHash, false, olterr.New(
			olterr.TypeValidation,
			"expectedConfigHash is required to apply -- call with dryRun=true first and pass back the configHash it returns",
		)
	}
	if expectedConfigHash != currentHash {
		return diff, reverseCmds, currentHash, false, olterr.New(
			olterr.TypeValidation,
			"device config has changed since the dry-run this hash came from -- re-run dry-run and review the new diff before applying",
		)
	}

	appliedCount, rejection, err := d.applyMergeConfig(lines)
	if err != nil {
		return diff, reverseCmds, currentHash, false, err
	}
	if rejection != "" {
		return diff, reverseCmds, currentHash, false, olterr.New(
			olterr.TypeValidation,
			fmt.Sprintf(
				"device rejected the candidate after applying %d of %d lines (%s) -- "+
					"config mode was exited cleanly, no further lines were attempted",
				appliedCount, len(lines), rejection,
			),
		)
	}
	return diff, reverseCmds, currentHash, true, nil
}

// parseInterfaceBrief extracts interface names from `show ip interface
// brief` output: the first whitespace-delimited column of every row except
// the header ("Interface  IP-Address  OK? Method Status  Protocol").
//
// Confirmed live against a real Cisco ISRv (IOS-XE 17.01.01): the raw
// output can carry a trailing device-prompt line (e.g. "ISR#") after the
// last real row -- BaseDriver's prompt-stripping is keyed to the captured
// prompt text, not specific to this command, so a stray prompt-shaped line
// slipping through here is possible on some devices/timings. A real row
// always has at least an IP-Address/status column beyond the interface
// name itself; a bare prompt does not -- requiring >=2 fields filters it
// out without assuming an exact column count (which varies: a 2-word
// Status like "administratively down" shifts the total).
func parseInterfaceBrief(output string) []string {
	interfaces := []string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.EqualFold(fields[0], "Interface") {
			continue
		}
		interfaces = append(interfaces, fields[0])
	}
	return interfaces
}
