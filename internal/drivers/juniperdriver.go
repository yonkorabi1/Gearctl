package drivers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/onu-platform/gearctl/internal/olterr"
	"github.com/onu-platform/gearctl/internal/transport"
)

// JuniperDriver is gearctl's second vendor-specific driver: Juniper's
// Junos OS. Confirmed live against a real Juniper vMX (Junos 18.2R1.9) --
// same lab device transport/promptdetector.go's Junos pagination fix
// (see its own doc comment) was found and fixed against first, including
// every command below and the two live-verified/live-reverted edge cases
// noted on parseJuniperInterfaces and Description's field, respectively.
//
// Unlike CiscoIOSDriver's regex-scraped free-text parsing, every read
// command this driver sends is suffixed "| display json" -- Junos's own
// structured-output mode -- so parsing is a straightforward
// encoding/json unmarshal into the shapes below, not pattern matching
// against a text format that can vary by release train. "set cli
// screen-length 0" is still sent proactively first, the same reasoning
// CiscoIOSDriver's GetConfig doc comment gives for "terminal length 0":
// confirmed live (see promptdetector.go) that relying on the generic
// pagination-marker fallback alone is riskier than just disabling paging
// for the session.
//
// Junos's JSON convention, confirmed live and consistent across every
// command captured: every leaf value is wrapped as a ONE-ELEMENT ARRAY
// of an object carrying "data" (e.g. `"os-version": [{"data":
// "18.2R1.9"}]`), not a bare scalar -- junosLeaf/leafString below exist
// solely to unwrap that. A field that's unset can appear either as a
// completely absent JSON key (encoding/json leaves the Go zero value,
// nothing special needed) OR as present with an EMPTY object array
// element (e.g. `"host-name": [{}]`, confirmed live on this device,
// which genuinely has no hostname configured) -- leafString handles
// both the same way, returning "".
type JuniperDriver struct {
	BaseDriver
}

func NewJuniperDriver() *JuniperDriver {
	return &JuniperDriver{}
}

func (d *JuniperDriver) Vendor() string {
	return "juniper"
}

// junosLeaf is Junos's universal "| display json" leaf shape -- see
// JuniperDriver's doc comment. Deliberately a slice, not a single
// struct, since an empty array (as opposed to a one-element array
// holding an empty object) is also a possible shape -- leafString
// handles both by checking len() before indexing, rather than assuming
// exactly one element is always present.
type junosLeaf []struct {
	Data string `json:"data"`
}

// leafString unwraps a junosLeaf to its string value, or "" if the field
// was entirely absent from the JSON, present but empty, or an empty
// array -- all three confirmed live as real shapes Junos actually emits
// (see JuniperDriver's doc comment), so none of them are treated as a
// parse error.
func leafString(l junosLeaf) string {
	if len(l) == 0 {
		return ""
	}
	return l[0].Data
}

// outputOrEmpty unwraps a CommandResult's Output, or "" if nil (a
// command that itself errored -- callers here have already checked
// Error == "" before calling this, so nil in practice only means "the
// device replied with no output at all", not a transport failure).
func outputOrEmpty(r transport.CommandResult) string {
	if r.Output == nil {
		return ""
	}
	return *r.Output
}

// disablePaging sends "set cli screen-length 0" -- see JuniperDriver's
// doc comment for why this is sent proactively rather than relying on
// the generic pagination-marker fallback alone.
func (d *JuniperDriver) disablePaging() error {
	results, err := d.RunCommands([]string{"set cli screen-length 0"})
	if err != nil {
		return err
	}
	if len(results) < 1 || results[0].Error != "" {
		return olterr.New(olterr.TypeProtocol, `"set cli screen-length 0" failed -- see the command result for the actual error`)
	}
	return nil
}

// runJSONCommand runs a "| display json" command via
// BaseDriver.RunCommandUntilComplete, using encoding/json.Valid as the
// completeness check.
//
// This exists because of a real, live-reproduced bug against the same
// Juniper vMX this whole driver was verified against: a large `show
// interfaces | display json` response (~200KB) can arrive in bursts with
// an internal pause exceeding commandQuiescenceMs's 2-second window (see
// promptdetector.go), with no prompt or pagination marker in sight to
// signal "the device is still working" -- the exact quiescence-fallback
// failure mode promptdetector.go's own doc comments already describe,
// just triggered by device-side burstiness on a large payload instead of
// by pagination. Confirmed live via ~30 direct repro attempts against the
// real device: every failure was a CLEAN mid-structure cutoff (e.g.
// `unexpected end of JSON input`, `expecting property name`) with
// Truncated reported as false -- this is NOT gearctl's MaxOutputBytes
// cap being hit (that's checked separately below), it is the read loop
// concluding "no more bytes are coming" before they actually finish
// arriving.
//
// An EARLIER version of this function retried by resending the whole
// command from scratch on an incomplete-looking result. That worked
// (roughly halved the failure rate) but was wasteful -- it throws away a
// possibly-almost-complete read and re-triggers the SAME slow device-side
// generation from scratch -- and, under heavier device load, still left
// a meaningful failure rate (confirmed live: ~25-35% even with 3
// attempts). RunCommandUntilComplete instead fixes the actual root
// cause: it keeps the ORIGINAL read open and extending (still bounded by
// the overall per-command timeout, ConnectionParams.PerCommandTimeoutMs)
// for as long as the accumulated output isn't yet valid/complete JSON,
// rather than giving up and resending. A caller talking to a device this
// slow should raise PerCommandTimeoutMs rather than rely on gearctl's
// default.
//
// Falls back to plain RunCommandWithHint-equivalent behavior
// automatically if the underlying session doesn't implement
// transport.CompletionAwareSession (see RunCommandUntilComplete) --
// functionally identical to this driver's original, pre-fix behavior in
// that case, just without the improvement. Truncated is still checked
// explicitly: MaxOutputBytes being hit is a distinct condition from "the
// read gave up too early" and isComplete can't see past it (the
// captured text is real, valid-as-far-as-it-goes JSON, just cut off by a
// hard byte cap, not a quiescence pause) -- checked here so callers get
// a clear error rather than a confusing encoding/json failure.
func (d *JuniperDriver) runJSONCommand(command string) (string, error) {
	result, err := d.RunCommandUntilComplete(command, func(settled string) bool {
		return json.Valid([]byte(settled))
	})
	if err != nil {
		return "", err
	}
	if result.Error != "" {
		return "", olterr.New(olterr.TypeProtocol, fmt.Sprintf("%q failed -- see the command result for the actual error", command))
	}
	if result.Truncated {
		return "", olterr.New(
			olterr.TypeProtocol,
			fmt.Sprintf("%q: response exceeded gearctl's per-command output capture limit", command),
		)
	}
	return outputOrEmpty(result), nil
}

// junosSystemInformationEnvelope is `show system information | display
// json`'s shape -- confirmed live: hardware-model/os-name/os-version/
// serial-number/host-name all in one command, which is why GetFacts uses
// this rather than combining `show version` (Junos's closer analogue to
// Cisco's `show version`, but which carries no serial number at all --
// that's under `show chassis hardware` instead) with a second command.
type junosSystemInformationEnvelope struct {
	SystemInformation []struct {
		HardwareModel junosLeaf `json:"hardware-model"`
		OSVersion     junosLeaf `json:"os-version"`
		SerialNumber  junosLeaf `json:"serial-number"`
		HostName      junosLeaf `json:"host-name"`
	} `json:"system-information"`
}

// junosSystemUptimeEnvelope is `show system uptime | display json`'s
// shape -- confirmed live. Only up-time (Junos's own human-readable
// "14:21" duration string) is used; current-time/system-booted-time/
// protocols-started-time/last-configured-time are all present too but
// not part of FactsProvider's contract.
type junosSystemUptimeEnvelope struct {
	SystemUptimeInformation []struct {
		UptimeInformation []struct {
			UpTime junosLeaf `json:"up-time"`
		} `json:"uptime-information"`
	} `json:"system-uptime-information"`
}

// junosTerseInterfacesEnvelope is `show interfaces terse | display
// json`'s shape -- confirmed live. Used for two things GetFacts/
// GetInterfaces both need but the fuller `show interfaces | display
// json` doesn't conveniently carry: the flat interface_list (physical
// AND logical names, matching NAPALM's own junos get_facts() convention
// of not limiting interface_list to physical interfaces only) and each
// logical interface's inet address in ready-made CIDR form (e.g.
// "192.168.14.14/24", confirmed live) -- the fuller command's own
// ifa-local carries only the bare address with no prefix length; its
// sibling ifa-destination has the network+prefix but not the host
// address, and reassembling one clean CIDR string from those two would
// be more fragile than just reading terse's ready-made one instead.
type junosTerseInterfacesEnvelope struct {
	InterfaceInformation []struct {
		PhysicalInterface []junosTerseInterface `json:"physical-interface"`
	} `json:"interface-information"`
}

type junosTerseInterface struct {
	Name             junosLeaf                `json:"name"`
	LogicalInterface []junosTerseLogicalIface `json:"logical-interface"`
}

type junosTerseLogicalIface struct {
	Name          junosLeaf                 `json:"name"`
	AddressFamily []junosTerseAddressFamily `json:"address-family"`
}

type junosTerseAddressFamily struct {
	AddressFamilyName junosLeaf                 `json:"address-family-name"`
	InterfaceAddress  []junosTerseInterfaceAddr `json:"interface-address"`
}

type junosTerseInterfaceAddr struct {
	IfaLocal junosLeaf `json:"ifa-local"`
}

// junosFullInterfacesEnvelope is `show interfaces | display json`'s
// shape (no "terse") -- confirmed live. Carries the per-interface health
// fields terse's own output doesn't: admin/oper-status (terse has these
// too, but this is the single source used for InterfaceInfo so
// GetInterfaces has one authoritative status reading, not two that could
// in principle disagree), mtu, speed, current-physical-address (MAC),
// and description (confirmed live by deliberately setting one on a real
// interface, capturing the resulting JSON field name, then removing it
// again -- see promptdetector.go-adjacent commit history/README for the
// live session this was verified in; genuinely ABSENT from the JSON
// entirely, not present-but-empty, on every interface here that has no
// description configured, matching Cisco's own "line simply isn't
// there" convention for the same fact).
type junosFullInterfacesEnvelope struct {
	InterfaceInformation []struct {
		PhysicalInterface []junosFullInterface `json:"physical-interface"`
	} `json:"interface-information"`
}

type junosFullInterface struct {
	Name                   junosLeaf `json:"name"`
	AdminStatus            junosLeaf `json:"admin-status"`
	OperStatus             junosLeaf `json:"oper-status"`
	MTU                    junosLeaf `json:"mtu"`
	Speed                  junosLeaf `json:"speed"`
	CurrentPhysicalAddress junosLeaf `json:"current-physical-address"`
	Description            junosLeaf `json:"description"`
}

// GetFacts implements FactsProvider. Sends "set cli screen-length 0"
// then runs three "| display json" commands (see each envelope type's
// doc comment for why these three, specifically) through
// runJSONCommand's retry-on-incomplete-response wrapper, and parses the
// combined output into the same shape NAPALM's get_facts() returns --
// same field set CiscoIOSDriver.GetFacts populates.
//
// A genuine transport-level failure from any command (runJSONCommand
// returning a non-nil error -- see its own doc comment) fails GetFacts
// as a whole, same as CiscoIOSDriver.GetFacts's "any command failing is
// fatal" contract. A command that ran fine but whose JSON simply didn't
// parse (including after runJSONCommand's retries are exhausted)
// degrades that command's fields to the zero value instead -- same "a
// field simply not being populated is not a hard error" posture
// CiscoIOSDriver.GetFacts takes for a regex that doesn't match.
//
// fqdn is best-effort, same posture as CiscoIOSDriver.GetFacts: Junos's
// own commands here don't carry a separate domain name, so this falls
// back to the bare hostname rather than issuing `show configuration
// system domain-name` as a fourth command.
func (d *JuniperDriver) GetFacts() (map[string]interface{}, error) {
	if err := d.disablePaging(); err != nil {
		return nil, err
	}

	facts := map[string]interface{}{
		"vendor":         "Juniper",
		"model":          "",
		"hostname":       "",
		"fqdn":           "",
		"os_version":     "",
		"serial_number":  "",
		"uptime":         "",
		"interface_list": []string{},
	}

	sysInfoOutput, err := d.runJSONCommand("show system information | display json")
	if err != nil {
		return nil, err
	}
	var sysInfo junosSystemInformationEnvelope
	if json.Unmarshal([]byte(sysInfoOutput), &sysInfo) == nil && len(sysInfo.SystemInformation) > 0 {
		si := sysInfo.SystemInformation[0]
		facts["model"] = leafString(si.HardwareModel)
		facts["os_version"] = leafString(si.OSVersion)
		facts["serial_number"] = leafString(si.SerialNumber)
		hostname := leafString(si.HostName)
		facts["hostname"] = hostname
		facts["fqdn"] = hostname
	}

	uptimeOutput, err := d.runJSONCommand("show system uptime | display json")
	if err != nil {
		return nil, err
	}
	var uptime junosSystemUptimeEnvelope
	if json.Unmarshal([]byte(uptimeOutput), &uptime) == nil &&
		len(uptime.SystemUptimeInformation) > 0 && len(uptime.SystemUptimeInformation[0].UptimeInformation) > 0 {
		facts["uptime"] = leafString(uptime.SystemUptimeInformation[0].UptimeInformation[0].UpTime)
	}

	terseOutput, err := d.runJSONCommand("show interfaces terse | display json")
	if err != nil {
		return nil, err
	}
	var terse junosTerseInterfacesEnvelope
	if json.Unmarshal([]byte(terseOutput), &terse) == nil {
		facts["interface_list"] = interfaceNamesFromTerse(terse)
	}

	return facts, nil
}

// interfaceNamesFromTerse flattens a junosTerseInterfacesEnvelope into
// the same "interface_list" shape NAPALM's junos get_facts() returns:
// BOTH physical interface names (e.g. "ge-0/0/0") AND their logical
// units (e.g. "ge-0/0/0.0"), in the order Junos itself returned them.
// Always returns a non-nil slice (an empty JSON result is a real,
// meaningful "no interfaces" answer, not an unparsed/error state -- same
// convention as parseInterfaceBrief's Cisco counterpart).
func interfaceNamesFromTerse(terse junosTerseInterfacesEnvelope) []string {
	names := []string{}
	if len(terse.InterfaceInformation) == 0 {
		return names
	}
	for _, iface := range terse.InterfaceInformation[0].PhysicalInterface {
		if name := leafString(iface.Name); name != "" {
			names = append(names, name)
		}
		for _, logical := range iface.LogicalInterface {
			if name := leafString(logical.Name); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// ipAddressesFromTerse maps each PHYSICAL interface name to the first
// inet (IPv4) address found across any of its logical units, in
// ready-made CIDR form (e.g. "192.168.14.14/24" -- see
// junosTerseInterfacesEnvelope's doc comment for why terse, not the
// fuller command, is the source for this). A physical interface with no
// inet address configured on any of its units (e.g. lo0's loopback unit
// normally carries one, but a purely L2/down interface may not) simply
// has no entry -- callers look it up with the map's ordinary
// zero-value-on-miss behavior ("").
//
// "First found" matches CiscoIOSDriver's own InterfaceInfo.IPAddress
// convention of one flat address per top-level interface, not a list --
// a physical interface with multiple logical units each carrying their
// own inet address (uncommon, but possible) only surfaces the first
// one's; anything beyond that is a known, documented limitation, not a
// bug, same as InterfaceInfo's own doc comment's IPAddress caveat.
func ipAddressesFromTerse(terse junosTerseInterfacesEnvelope) map[string]string {
	result := make(map[string]string)
	if len(terse.InterfaceInformation) == 0 {
		return result
	}
	for _, iface := range terse.InterfaceInformation[0].PhysicalInterface {
		name := leafString(iface.Name)
		if name == "" {
			continue
		}
		if _, already := result[name]; already {
			continue
		}
		for _, logical := range iface.LogicalInterface {
			for _, af := range logical.AddressFamily {
				if !strings.EqualFold(leafString(af.AddressFamilyName), "inet") {
					continue
				}
				for _, addr := range af.InterfaceAddress {
					if ip := leafString(addr.IfaLocal); ip != "" {
						result[name] = ip
					}
				}
			}
		}
	}
	return result
}

// junosSpeedPattern matches Junos's `show interfaces | display json`
// speed field, e.g. "1000mbps" or "800mbps" -- confirmed live. Also
// confirmed live: non-numeric values like "Unlimited" (seen on this
// device's lo0), which simply don't match -- parseJunosSpeed returns 0
// for those rather than erroring, same "degrade gracefully" posture as
// every other best-effort field in this file.
var junosSpeedPattern = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)\s*(mbps|gbps|kbps)?$`)

// parseJunosSpeed converts a Junos speed string to Mbps, matching
// CiscoIOSDriver's InterfaceInfo.SpeedMbps convention (NAPALM's own
// get_interfaces() unit).
func parseJunosSpeed(raw string) float64 {
	m := junosSpeedPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return 0
	}
	value, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	switch strings.ToLower(m[2]) {
	case "gbps":
		return value * 1000
	case "kbps":
		return value / 1000
	default: // "mbps", or no unit suffix at all
		return value
	}
}

// parseJuniperInterfaces builds the InterfaceInfo map GetInterfaces
// returns: one entry per PHYSICAL interface in full (logical
// sub-interfaces are not given their own top-level entries -- same
// physical-only granularity as CiscoIOSDriver.parseInterfaces, where a
// Cisco physical interface has no separate logical layer to begin with),
// with IPAddress filled in from ipByPhysical (see ipAddressesFromTerse).
//
// MTU/Speed use strconv/parseJunosSpeed respectively and simply leave
// the zero value on a parse failure (e.g. MTU "Unlimited", confirmed
// live on lo0) rather than erroring -- consistent with every other
// best-effort field parsed in this package.
func parseJuniperInterfaces(full junosFullInterfacesEnvelope, ipByPhysical map[string]string) map[string]InterfaceInfo {
	result := make(map[string]InterfaceInfo)
	if len(full.InterfaceInformation) == 0 {
		return result
	}
	for _, iface := range full.InterfaceInformation[0].PhysicalInterface {
		name := leafString(iface.Name)
		if name == "" {
			continue
		}
		info := InterfaceInfo{
			IsUp:        strings.EqualFold(leafString(iface.OperStatus), "up"),
			IsEnabled:   strings.EqualFold(leafString(iface.AdminStatus), "up"),
			Description: leafString(iface.Description),
			MACAddress:  leafString(iface.CurrentPhysicalAddress),
			IPAddress:   ipByPhysical[name],
			SpeedMbps:   parseJunosSpeed(leafString(iface.Speed)),
		}
		if mtu, err := strconv.Atoi(leafString(iface.MTU)); err == nil {
			info.MTU = mtu
		}
		result[name] = info
	}
	return result
}

// GetInterfaces implements InterfaceProvider: sends "set cli
// screen-length 0" then runs two "| display json" commands through
// runJSONCommand's retry-on-incomplete-response wrapper -- terse for the
// IP-address lookup, the fuller command for everything else (see
// junosTerseInterfacesEnvelope's and junosFullInterfacesEnvelope's doc
// comments for why each is needed) -- and combines them into the same
// per-interface shape CiscoIOSDriver.GetInterfaces returns.
//
// A transport-level failure on either command (runJSONCommand returning
// a non-nil error) fails GetInterfaces as a whole. Beyond that,
// completeness is handled differently per command, same posture as
// before runJSONCommand's retry existed: terse's JSON is parsed
// best-effort (a failure here just means no IP addresses get filled in
// below, matching ipAddressesFromTerse's own "a physical interface with
// no entry just resolves to \"\"" convention) -- unlike Cisco's
// free-text output (where a truncated response still degrades to "less
// information parsed, no hard error"), the FULL command's JSON failing
// to parse (including after runJSONCommand's retries are exhausted) IS
// still a hard error here, since a truncated-but-still-syntactically-
// plausible JSON object could otherwise silently drop trailing
// interfaces, which would look like "these interfaces don't exist"
// rather than "the response was incomplete".
func (d *JuniperDriver) GetInterfaces() (map[string]InterfaceInfo, error) {
	if err := d.disablePaging(); err != nil {
		return nil, err
	}

	terseOutput, err := d.runJSONCommand("show interfaces terse | display json")
	if err != nil {
		return nil, err
	}
	var terse junosTerseInterfacesEnvelope
	_ = json.Unmarshal([]byte(terseOutput), &terse) // best-effort: a failure here just means no IP addresses get filled in below
	ipByPhysical := ipAddressesFromTerse(terse)

	fullOutput, err := d.runJSONCommand("show interfaces | display json")
	if err != nil {
		return nil, err
	}
	var full junosFullInterfacesEnvelope
	if err := json.Unmarshal([]byte(fullOutput), &full); err != nil {
		return nil, olterr.New(olterr.TypeProtocol, `GetInterfaces: failed to parse "show interfaces | display json" output: `+err.Error())
	}

	return parseJuniperInterfaces(full, ipByPhysical), nil
}

// GetConfig implements ConfigGetter: runs "set cli screen-length 0" then
// "show configuration", and returns the second command's output
// verbatim -- Junos's own hierarchical curly-brace config format (e.g.
// `system { login { user rabi { ... } } }`), NOT "| display json" --
// this is the read-only half of the config seam, meant to be read/edited
// by a human, same as CiscoIOSDriver.GetConfig's `show running-config`.
// Confirmed live: includes Junos's own "## SECRET-DATA" markers next to
// encrypted password hashes, same as real `show configuration` output;
// this driver does not attempt to redact or otherwise alter it.
//
// IMPORTANT asymmetry with CiscoIOSDriver: this curly-brace output is
// NOT what LoadConfig accepts as a candidate (see its own doc comment
// for why) -- unlike Cisco, where GetConfig()'s own output is exactly
// what LoadConfig expects back, edited. A caller wanting a real
// candidate to edit should instead run `show configuration | display
// set` via RunCommands/`/v1/execute` -- Junos's own flat "set"/"delete"
// form, one statement per line.
func (d *JuniperDriver) GetConfig() (string, error) {
	results, err := d.RunCommands([]string{"set cli screen-length 0", "show configuration"})
	if err != nil {
		return "", err
	}
	if len(results) < 2 || results[0].Error != "" || results[1].Error != "" {
		return "", olterr.New(
			olterr.TypeProtocol,
			`GetConfig: "set cli screen-length 0" or "show configuration" failed -- see the command results for the actual error`,
		)
	}
	if results[1].Truncated {
		return "", olterr.New(
			olterr.TypeProtocol,
			"GetConfig: response was truncated -- device output exceeded gearctl's per-command capture limit",
		)
	}
	return outputOrEmpty(results[1]), nil
}

// junosSyntaxErrorPattern matches Junos's own rejection convention for a
// bad configuration-mode command -- confirmed live: typing an invalid
// "set" command produces one or more lines like:
//
//	          ^
//	syntax error.
//
// (lowercase, trailing period, no "%"/"error:" prefix the way Cisco/most
// vendors format a rejection). Confirmed live that Junos validates
// INCREMENTALLY as a line is received, not just once at the end -- a
// single bad command can produce this message MORE THAN ONCE in the
// same response, interleaved with backspace/redraw noise as the CLI's
// own interactive completion re-evaluates what's been typed so far.
// Matching case-insensitively and substring-only (not anchored to a
// single occurrence) is deliberately robust to that repetition.
//
// NOT live-confirmed: a semantically-invalid-but-syntactically-valid
// command (e.g. referencing a real keyword in the wrong context) may
// produce different Junos wording this pattern wouldn't catch --
// detectJunosCommitFailure (checked separately, at "commit check"/
// "commit" time) is the backstop for anything that slips past this.
var junosSyntaxErrorPattern = regexp.MustCompile(`(?i)syntax error`)

// junosCommitErrorPattern matches Junos's general "error: ..." rejection
// convention -- confirmed live via a real semantic (not syntax) commit
// check failure: setting a second logical unit on an interface whose
// encapsulation only allows unit 0 produced
//
//	[edit interfaces ge-0/0/0]
//	  'unit 1'
//	    Only unit 0 is valid for this encapsulation
//	error: configuration check-out failed
//
// -- junosSyntaxErrorPattern alone ("syntax error") does NOT match this,
// which let a rejected line slip through detectJunosRejection undetected
// during the per-line apply loop in real testing (only this driver's own
// automatic "commit check" step downstream caught it, by luck of also
// checking for the literal "check succeeds" substring). Matched
// line-anchored so it doesn't false-positive on the word "error"
// appearing inside otherwise-legitimate device output.
var junosCommitErrorPattern = regexp.MustCompile(`(?im)^\s*error:`)

// detectJunosRejection inspects one apply-mode CommandResult the same
// way CiscoIOSDriver.detectRejection does -- a non-empty, human-readable
// reason if the device appears to have rejected the command, "" if it
// looks accepted. Junos reports a bad configuration-mode command as
// normal output text (not a transport-level failure), same as Cisco --
// RunCommands alone can't tell "accepted" from "rejected" on its own.
//
// Also treats Truncated as a rejection, same reasoning as Cisco's own
// detectRejection: config-mode commands don't normally paginate (paging
// is already disabled via GetConfig's own "set cli screen-length 0"
// convention, sent once per session -- see enterConfigPrivate), so
// truncation here is itself an anomaly worth aborting on rather than
// trusting partial output.
func detectJunosRejection(result transport.CommandResult) string {
	if result.Truncated {
		return "response was truncated -- treating as a failed/uncertain apply rather than trusting partial output"
	}
	if result.Output == nil {
		return ""
	}
	// Strip the bare trailing "[edit]" context-refresh line BEFORE
	// extracting a human-readable reason -- confirmed live this is
	// otherwise the actual last line of a config-mode response (see
	// stripTrailingEditContext's own doc comment), which would make the
	// reported reason end in the meaningless "[edit]" instead of the
	// real rejection text sitting just above it.
	output := stripTrailingEditContext(*result.Output)
	if junosSyntaxErrorPattern.MatchString(output) || junosCommitErrorPattern.MatchString(output) {
		return strings.TrimSpace(output)
	}
	return ""
}

// enterConfigPrivate sends "configure private" -- Junos's per-session
// isolated candidate configuration, confirmed live to be the right
// choice for a shared multi-user device: unlike plain "configure"
// (shared candidate, merged with whatever every OTHER concurrent session
// is also editing -- confirmed live this device has several: "Users
// currently editing the configuration: root terminal u0 ... idle
// 15:13:14"), "private" gives this session its own candidate that
// doesn't collide with anyone else's in-progress edits. Confirmed live:
// response includes "warning: uncommitted changes will be discarded on
// exit" and "Entering configuration mode", and the prompt itself changes
// from "rabi>" to "rabi#" (with a separate "[edit]" context line printed
// just before it, not part of the prompt text itself).
func (d *JuniperDriver) enterConfigPrivate() error {
	results, err := d.RunCommands([]string{"configure private"})
	if err != nil {
		return err
	}
	if len(results) < 1 || results[0].Error != "" {
		return olterr.New(olterr.TypeProtocol, `"configure private" failed -- see the command result for the actual error`)
	}
	// NOT live-confirmed: this device never actually refused "configure
	// private" (no competing exclusive session was ever active during
	// testing), so there's no confirmed real rejection text to check for
	// here. Best-effort: Junos's own "syntax error" convention would
	// still be recognized if this line existed at all somewhere else,
	// but a distinct "configure private refused" message (if Junos has
	// one) is not specifically detected.
	return nil
}

// exitConfig sends "exit" to leave configuration mode -- confirmed live:
// "Exiting configuration mode", prompt reverts to "rabi>". Best-effort,
// same posture as CiscoIOSDriver.applyMergeConfig's own "end" call: its
// outcome isn't itself treated as fatal when called during cleanup after
// something else already went wrong, since the caller is already
// reporting that earlier problem.
func (d *JuniperDriver) exitConfig() {
	_, _ = d.RunCommands([]string{"exit"})
}

// discardAndExitConfig sends "rollback 0" (discard every uncommitted
// change in the current private candidate -- confirmed live: silent/
// empty output on success) then exitConfig -- the dry-run/rejection
// cleanup path, leaving the device's actual committed config completely
// untouched.
func (d *JuniperDriver) discardAndExitConfig() {
	_, _ = d.RunCommands([]string{"rollback 0"})
	d.exitConfig()
}

// applyJuniperMergeConfig enters config mode, replays lines one at a
// time (checking each for rejection via detectJunosRejection, same
// fail-fast-on-rejection posture as CiscoIOSDriver.applyMergeConfig),
// and -- if every line was accepted -- captures the diff via Junos's OWN
// native "show | compare" (confirmed live to produce a real, accurate
// unified-style diff, e.g. "[edit interfaces ge-0/0/0 unit 0]" /
// "+    description gearctl-cfgtest;" -- NOT a heuristic reconstruction
// the way CiscoIOSDriver.projectMergedConfig is, since Junos generates
// this from its own candidate/committed comparison directly).
//
// Three outcomes, mirroring CiscoIOSDriver.applyMergeConfig's own:
//  1. err != nil: a transport-level failure hit mid-apply. Config mode
//     is deliberately NOT exited (same reasoning as Cisco's version --
//     the device may still be mid-response to something).
//  2. rejection != "" (err == nil): a line was cleanly rejected.
//     discardAndExitConfig WAS called (best-effort) to leave the device
//     in a known, clean, uncommitted state.
//  3. Neither: every line was accepted; diff holds the real `show |
//     compare` output, config mode is still OPEN (the caller -- dry-run
//     cleanup or commit -- decides what happens next; this function
//     never itself discards or commits a successful run).
func (d *JuniperDriver) applyJuniperMergeConfig(lines []string) (diff string, rejection string, err error) {
	if err := d.enterConfigPrivate(); err != nil {
		return "", "", err
	}

	for i, line := range lines {
		results, err := d.RunCommands([]string{line})
		if err != nil {
			return "", "", err
		}
		if len(results) < 1 {
			return "", "", olterr.New(olterr.TypeProtocol, fmt.Sprintf("no result for line %d (%q)", i+1, line))
		}
		if results[0].Error != "" {
			// Transport-level failure (timeout/session_closed), not a
			// device rejection -- detectJunosRejection can't see this
			// (Output is nil whenever Error is set), so it's checked
			// explicitly here rather than falling through and being
			// silently treated as "line accepted, no output". Session
			// state is indeterminate (see ShellSession's contract) --
			// deliberately NOT attempting discardAndExitConfig, same
			// posture CiscoIOSDriver.applyMergeConfig takes on a
			// connection drop mid-apply.
			return "", "", olterr.New(
				olterr.TypeConnection,
				fmt.Sprintf("connection lost after applying %d of %d lines (line %d, %q: %s) -- device state not verified",
					i, len(lines), i+1, line, results[0].Error),
			)
		}
		if reason := detectJunosRejection(results[0]); reason != "" {
			d.discardAndExitConfig()
			return "", fmt.Sprintf("line %d (%q): %s", i+1, line, reason), nil
		}
	}

	results, err := d.RunCommands([]string{"show | compare"})
	if err != nil {
		return "", "", err
	}
	if len(results) < 1 || results[0].Error != "" {
		return "", "", olterr.New(olterr.TypeProtocol, `"show | compare" failed -- see the command result for the actual error`)
	}
	return strings.TrimSpace(stripTrailingEditContext(outputOrEmpty(results[0]))), "", nil
}

// junosBareEditContextLine matches ONLY a bare "[edit]" line (optional
// surrounding whitespace) -- deliberately NOT "[edit <path>]" (a real,
// populated context header, e.g. `show | compare`'s own "[edit
// interfaces ge-0/0/0 unit 0]" line, which IS meaningful diff content
// and must never be stripped).
var junosBareEditContextLine = regexp.MustCompile(`^\[edit\]\s*$`)

// stripTrailingEditContext removes a trailing bare "[edit]" line (and
// any blank line immediately before it) from Junos configuration-mode
// output. Confirmed live: Junos reprints this bare "[edit]" line
// immediately before ITS OWN NEXT PROMPT on every response while in
// configuration mode -- e.g. "show | compare"'s real captured output is
// `[edit interfaces ge-0/0/0 unit 0]\n+    description X;\n\n[edit]`,
// with that trailing "\n\n[edit]" being prompt-adjacent context noise,
// not part of the diff. gearctl's generic, vendor-agnostic
// prompt-detection only knows to strip the PROMPT text itself (e.g.
// "rabi#"); it has no way to know Junos additionally reprints this
// separate context line right before it, so this is handled here,
// Junos-specifically, rather than in the shared transport layer.
func stripTrailingEditContext(output string) string {
	lines := strings.Split(output, "\n")
	for len(lines) > 0 {
		last := lines[len(lines)-1]
		if strings.TrimSpace(last) == "" || junosBareEditContextLine.MatchString(strings.TrimSpace(last)) {
			lines = lines[:len(lines)-1]
			continue
		}
		break
	}
	return strings.Join(lines, "\n")
}

// junosReservedCommandLinePattern matches a delta line that duplicates a
// control step LoadMergeCommands/LoadConfig ALREADY manage automatically:
// entering config mode ("configure"/"configure private"/"configure
// exclusive"/"configure batch") or committing ("commit", with or without
// an argument like "check"/"and-quit"/"confirmed ...").
//
// This is NOT just redundant -- confirmed live, via a real user
// including "commit"/"commit check" as their own delta lines, that it's
// actively dangerous: applyJuniperMergeConfig replays EVERY line in
// commands for real, REGARDLESS of dryRun (dry-run only controls what
// happens AFTER replay -- discard vs commit-check+commit), so a
// caller-supplied "commit" line gets executed for real DURING what the
// caller believes is a safe, no-device-write dry-run preview -- entirely
// bypassing the dry-run-first/hash-freshness/type-"apply"-to-confirm
// flow this whole interface exists to enforce. Refused outright (fail
// closed, before ever connecting), same posture
// CiscoIOSDriver.findMultiLineDirective takes for a different class of
// dangerous candidate content.
//
// Deliberately does NOT match "rollback" -- "rollback N" is the
// documented, legitimate way a caller replays reverseCommands (see
// LoadMergeCommands' own doc comment: "rollback 1" is expected, ordinary
// delta content, not a control command this driver needs to guard
// against).
var junosReservedCommandLinePattern = regexp.MustCompile(`(?im)^\s*(configure(\s+(private|exclusive|batch))?|commit(\s+\S.*)?)\s*$`)

// findJunosReservedCommandLine returns the first line matching
// junosReservedCommandLinePattern, or "" if none.
func findJunosReservedCommandLine(lines []string) string {
	for _, line := range lines {
		if junosReservedCommandLinePattern.MatchString(line) {
			return line
		}
	}
	return ""
}

// junosMergeCmdDisclaimer parallels CiscoIOSDriver's mergeCmdDisclaimer,
// but with the opposite conclusion: Junos's own "show | compare" is a
// REAL diff generated by the device comparing its candidate against the
// committed config, not a heuristic textual reconstruction -- so unlike
// Cisco's disclaimer (which exists specifically to warn the projection
// can miss things), this one is a positive statement of what backs the
// diff shown.
const junosMergeCmdDisclaimer = "NOTE: this diff is Junos's own \"show | compare\" output -- " +
	"a real comparison of the candidate configuration against the committed one, not a heuristic reconstruction.\n\n"

// LoadMergeCommands implements ConfigLoader's delta-based entry point --
// see driver.go's ConfigLoader doc comment for the shared dry-run/
// hash-freshness/apply contract (identical to CiscoIOSDriver's).
//
// Refuses outright (before ever calling GetConfig or connecting further)
// if commands contains a line matching junosReservedCommandLinePattern
// ("configure"/"configure private"/.../"commit"/"commit check"/...) --
// see that pattern's own doc comment for the real, live-reproduced
// safety hole this closes: those steps are already handled automatically
// below, and a caller supplying one themselves would have it replayed
// for real during the dry-run pass, regardless of dryRun.
//
// dryRun: applyJuniperMergeConfig runs and captures the real `show |
// compare` diff, then discardAndExitConfig cleanly abandons the private
// candidate -- the device's committed config is completely untouched.
//
// Apply: after the same hash-freshness check CiscoIOSDriver.LoadConfig
// uses, runs "commit check" (validates without committing -- confirmed
// live: "configuration check succeeds" on success) then "commit"
// (confirmed live: "commit complete") then exitConfig. A rejection at
// either step is treated the same as a rejected line during replay --
// discardAndExitConfig, a descriptive error, applied=false.
//
// reverseCommands is always exactly ["rollback 1"] -- Junos's own
// native "revert candidate to the state before the last commit"
// primitive (confirmed live: "load complete", followed by "show |
// compare" correctly showing the inverse diff, a real commit, and the
// change genuinely gone afterward). This is dramatically simpler AND
// more reliable than CiscoIOSDriver.reverseCommands' own per-line
// heuristic derivation: a caller wanting "commit confirmed" semantics
// (see driver.go's doc comment) just calls LoadMergeCommands again with
// commands=["rollback 1"] -- entering config mode, running it, and
// committing again all happen automatically via this same function,
// with no special-casing needed anywhere.
func (d *JuniperDriver) LoadMergeCommands(
	commands []string, dryRun bool, expectedConfigHash string,
) (diff string, reverseCmds []string, configHash string, applied bool, err error) {
	lines := parseCandidateLines(strings.Join(commands, "\n"))
	reverseCmds = []string{"rollback 1"}

	if reserved := findJunosReservedCommandLine(lines); reserved != "" {
		return "", reverseCmds, "", false, olterr.New(
			olterr.TypeValidation,
			fmt.Sprintf(
				"candidate contains %q, which LoadMergeCommands/LoadConfig already handle automatically "+
					"(entering \"configure private\" and running \"commit check\"/\"commit\" at the right time, gated by dry-run/apply) -- "+
					"including it yourself bypasses that safety flow entirely and can commit for real during what should be a safe dry-run preview -- "+
					"just supply the set/delete delta lines",
				reserved,
			),
		)
	}

	current, err := d.GetConfig()
	if err != nil {
		return "", reverseCmds, "", false, err
	}
	currentHash := configHashOf(current)

	rawDiff, rejection, err := d.applyJuniperMergeConfig(lines)
	if err != nil {
		return "", reverseCmds, currentHash, false, err
	}
	if rejection != "" {
		return "", reverseCmds, currentHash, false, olterr.New(
			olterr.TypeValidation,
			fmt.Sprintf("device rejected the candidate (%s) -- configuration mode was discarded and exited cleanly, no further lines were attempted", rejection),
		)
	}
	diff = junosMergeCmdDisclaimer + rawDiff

	if dryRun {
		d.discardAndExitConfig()
		return diff, reverseCmds, currentHash, false, nil
	}

	if expectedConfigHash == "" {
		d.discardAndExitConfig()
		return diff, reverseCmds, currentHash, false, olterr.New(
			olterr.TypeValidation,
			"expectedConfigHash is required to apply -- call with dryRun=true first and pass back the configHash it returns",
		)
	}
	if expectedConfigHash != currentHash {
		d.discardAndExitConfig()
		return diff, reverseCmds, currentHash, false, olterr.New(
			olterr.TypeValidation,
			"device config has changed since the dry-run this hash came from -- re-run dry-run and review the new diff before applying",
		)
	}

	checkResult, err := d.RunCommandUntilComplete("commit check", isNonWhitespace)
	if err != nil {
		return diff, reverseCmds, currentHash, false, err
	}
	if checkResult.Error != "" {
		// A transport-level failure (timeout/session_closed), not a
		// device rejection -- the session's state is indeterminate (see
		// ShellSession's contract), so deliberately NOT attempting
		// discardAndExitConfig here, same posture
		// CiscoIOSDriver.applyMergeConfig takes on a connection drop
		// mid-apply.
		return diff, reverseCmds, currentHash, false, olterr.New(
			olterr.TypeConnection, `connection lost running "commit check": `+checkResult.Error,
		)
	}
	if reason := detectJunosRejection(checkResult); reason != "" || !strings.Contains(outputOrEmpty(checkResult), "check succeeds") {
		d.discardAndExitConfig()
		msg := reason
		if msg == "" {
			msg = strings.TrimSpace(stripTrailingEditContext(outputOrEmpty(checkResult)))
		}
		return diff, reverseCmds, currentHash, false, olterr.New(
			olterr.TypeValidation,
			fmt.Sprintf(`"commit check" failed (%s) -- configuration mode was discarded and exited cleanly, nothing was committed`, msg),
		)
	}

	commitResult, err := d.RunCommandUntilComplete("commit", isNonWhitespace)
	if err != nil {
		return diff, reverseCmds, currentHash, false, err
	}
	if commitResult.Error != "" {
		return diff, reverseCmds, currentHash, false, olterr.New(
			olterr.TypeConnection, `connection lost running "commit": `+commitResult.Error,
		)
	}
	if !strings.Contains(outputOrEmpty(commitResult), "commit complete") {
		reason := detectJunosRejection(commitResult)
		if reason == "" {
			reason = strings.TrimSpace(stripTrailingEditContext(outputOrEmpty(commitResult)))
		}
		d.discardAndExitConfig()
		return diff, reverseCmds, currentHash, false, olterr.New(
			olterr.TypeValidation,
			fmt.Sprintf(`"commit" failed (%s) -- configuration mode was discarded and exited cleanly, nothing was committed`, reason),
		)
	}

	d.exitConfig()
	return diff, reverseCmds, currentHash, true, nil
}

// isNonWhitespace is the isComplete predicate for "commit check"/
// "commit" (see RunCommandUntilComplete) -- both ALWAYS produce real,
// non-empty confirmation or rejection text on a genuine Junos response
// ("configuration check succeeds"/"commit complete", or a rejection
// detectJunosRejection recognizes), unlike a plain "set"/"delete" line
// or "show | compare" with no diff, which can correctly be empty on
// success. This exists because of a real, live-reproduced bug: "commit
// check" can take noticeably longer than a plain config-mode command
// (the device is doing real validation work), and the ordinary
// quiescence-fallback wait was found to conclude "done" on a
// whitespace-only settled buffer (a bare newline that arrived right
// after the command's own echo) BEFORE the real confirmation text
// showed up moments later -- confirmed live: reproducible specifically
// when "commit check" follows a realistic full sequence (GetConfig +
// configure private + delta lines + show | compare beforehand), not in
// isolation. The existing "settled == \"\"" quiescence guard (see
// waitForPrompt's own doc comment) already handles a LITERALLY empty
// buffer; this closes the gap for a buffer that's arrived but is still
// only whitespace.
func isNonWhitespace(settled string) bool {
	return strings.TrimSpace(settled) != ""
}

// LoadConfig implements ConfigLoader's full-candidate entry point.
//
// UNLIKE CiscoIOSDriver.LoadConfig, candidate here must be Junos's flat
// "set"/"delete" form (e.g. from `show configuration | display set`),
// ONE STATEMENT PER LINE -- NOT GetConfig()'s own curly-brace hierarchical
// output (see GetConfig's own doc comment for this asymmetry and why).
// This is a deliberate safety choice: Junos's native way to load a full
// hierarchical candidate is "load merge terminal", an interactive
// paste-then-Ctrl-D flow this driver deliberately does NOT attempt to
// automate -- getting Ctrl-D delivery exactly right over a scripted,
// non-interactive SSH/Telnet channel is a real foot-gun (confirmed
// firsthand: an early attempt at automating it left multiple SSH
// sessions hung on this exact lab device, requiring manual `request
// system logout terminal <pts>` cleanup). Flat "set"/"delete" form
// avoids that entirely -- it replays exactly like LoadMergeCommands,
// one plain line at a time, no special multi-line input mode involved.
//
// Implemented as a thin wrapper around LoadMergeCommands (splitting
// candidate into lines first, via the same parseCandidateLines Cisco's
// own LoadConfig uses) -- reverseCommands is dropped since LoadConfig's
// signature has no slot for it; that information isn't lost, it's just
// not part of this call's return shape (a caller wanting it can call
// LoadMergeCommands directly with the same lines instead).
func (d *JuniperDriver) LoadConfig(candidate string, dryRun bool, expectedConfigHash string) (string, string, bool, error) {
	lines := parseCandidateLines(candidate)
	diff, _, configHash, applied, err := d.LoadMergeCommands(lines, dryRun, expectedConfigHash)
	return diff, configHash, applied, err
}
