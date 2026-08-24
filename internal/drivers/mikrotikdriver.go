package drivers

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/onu-platform/gearctl/internal/olterr"
	"github.com/onu-platform/gearctl/internal/transport"
)

// MikroTikDriver targets RouterOS (MikroTik's CLI) over SSH -- confirmed
// live against a real RouterOS 6.49.19 CHR (Cloud Hosted Router VM) at
// 192.168.14.18.
//
// RouterOS's SSH console has a hang/truncation hazard NOT specific to
// this driver, fixed generically in the transport layer instead (see
// transport/cursorposition.go): it queries the terminal's cursor
// position (ESC[6n) the moment a PTY attaches and, before this codebase
// answered that query, kept redrawing/re-echoing the input line waiting
// for a reply that never came -- and promptdetector.go's fast
// substring-match path latched onto one of those early redraws as
// "command complete", truncating real output (confirmed live: "/system
// identity print"'s entire "name: ..." line silently disappeared). That
// fix alone was sufficient to make every command return correct data
// with a plain, unmodified username.
//
// Separately, RouterOS's console still emits ANSI SGR color codes
// (ESC[36m, etc.) by default, and paginates any response taller than the
// PTY's reported height (this codebase requests 40 rows, see
// OpenSSHSession) using a pager prompt this codebase's generic
// paginationMarkerPattern does NOT recognize (untested here -- this
// lab's command outputs never got long enough to trigger it). Both are
// avoidable at the source: RouterOS supports modifiers appended to the
// SSH login name after "+" (a documented RouterOS feature, not a
// gearctl invention) -- see mikrotikLoginModifiers and Connect below.
// Confirmed LIVE that "+e" (disable escape-sequence *processing*, as
// opposed to +c which only disables color) reintroduces the exact same
// truncation bug the transport-level CPR fix above solves -- e appears
// to change how RouterOS negotiates terminal capabilities entirely
// (possibly dropping the CPR query in favor of some other unanswered
// handshake), not just how it renders. mikrotikLoginModifiers therefore
// deliberately excludes "e".
//
// No enable/config-terminal modal concept, same as EXOS -- every command
// runs directly at the single top-level prompt (confirmed live: the
// prompt "[user@identity] > " never changes across commands in this
// driver's own Getters). "mikrotik" is correctly NOT in
// enablevendors.go's escalation allowlist.
//
// Read-only capabilities only (FactsProvider, InterfaceProvider,
// LLDPNeighborsProvider, ConfigGetter) -- ConfigLoader (config push) is
// deliberately NOT implemented yet. RouterOS's own config-change
// mechanism is script import (/import), not a line-oriented modal CLI --
// CiscoIOSDriver's stanza-diffing LoadMergeCommands machinery doesn't
// carry over any more than it did for EXOS (see ExtremeDriver's own
// doc comment for the same reasoning), and a safe merge/rollback design
// for RouterOS scripting is real, scoped follow-up work, not a small
// addition.
type MikroTikDriver struct {
	BaseDriver
}

func NewMikroTikDriver() *MikroTikDriver {
	return &MikroTikDriver{}
}

func (d *MikroTikDriver) Vendor() string {
	return "mikrotik"
}

// mikrotikLoginModifiers is appended to whatever username the caller
// supplies (unless it already looks like it has modifiers of its own --
// see Connect) -- "c" disables ANSI color codes, "w4095h4095" reports a
// huge terminal size so no realistic response ever triggers RouterOS's
// pager. Confirmed live: this combination produces clean, complete,
// un-truncated output for every command this driver sends (identity,
// resource, routerboard, interface, address, export), on top of (not a
// replacement for) the transport-level CPR fix -- removing the CPR fix
// and keeping only this modifier was NOT sufficient on its own (see the
// type doc comment).
const mikrotikLoginModifiers = "+cw4095h4095"

// Connect appends mikrotikLoginModifiers to params.Username before
// opening the session -- see the type doc comment and
// mikrotikLoginModifiers' own doc comment for why. Skipped if the
// username already contains "+" (the caller supplied their own RouterOS
// login modifiers explicitly; not overridden here).
func (d *MikroTikDriver) Connect(params transport.ConnectionParams) error {
	if !strings.Contains(params.Username, "+") {
		params.Username += mikrotikLoginModifiers
	}
	return d.BaseDriver.Connect(params)
}

// mikrotikANSIPattern strips ANSI/VT100 CSI escape sequences (ESC [
// ...letter) left in RouterOS console output -- SGR color codes
// (ESC[36m) mostly suppressed already by the "c" login modifier (see
// mikrotikLoginModifiers), but erase-line/cursor sequences (ESC[K) are
// not covered by "c" and still appear (confirmed live, a single leading
// "ESC[K" artifact on every command's output) -- stripped here rather
// than by a second login modifier since "+e" is confirmed live to break
// completion detection entirely (see the type doc comment).
var mikrotikANSIPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func stripMikrotikANSI(s string) string {
	return mikrotikANSIPattern.ReplaceAllString(s, "")
}

// runSingle runs exactly one RouterOS command and returns its
// ANSI-cleaned output. No prompt-hint prediction needed (unlike
// ExtremeDriver's runSingle): RouterOS's prompt is a stable
// "[user@identity] > " across ordinary commands (confirmed live,
// unchanged across every command this driver sends), so the default
// fast substring-match path already applies without help.
func (d *MikroTikDriver) runSingle(command string) (output string, truncated bool, errStr string, err error) {
	result, err := d.RunCommandWithHint(command, "")
	if err != nil {
		return "", false, "", err
	}
	if result.Output != nil {
		output = stripMikrotikANSI(*result.Output)
	}
	return output, result.Truncated, result.Error, nil
}

var (
	mikrotikIdentityPattern      = regexp.MustCompile(`(?m)^\s*name:\s*(.+?)\s*$`)
	mikrotikVersionPattern       = regexp.MustCompile(`(?m)^\s*version:\s*(.+?)\s*$`)
	mikrotikUptimePattern        = regexp.MustCompile(`(?m)^\s*uptime:\s*(.+?)\s*$`)
	mikrotikBoardNamePattern     = regexp.MustCompile(`(?m)^\s*board-name:\s*(.+?)\s*$`)
	mikrotikRouterboardPattern   = regexp.MustCompile(`(?m)^\s*routerboard:\s*(.+?)\s*$`)
	mikrotikRBModelPattern       = regexp.MustCompile(`(?m)^\s*model:\s*(.+?)\s*$`)
	mikrotikRBSerialPattern      = regexp.MustCompile(`(?m)^\s*serial-number:\s*(.+?)\s*$`)
	mikrotikInterfaceNamePattern = regexp.MustCompile(`(?m)^\s*\d+\s+\S*\s+name="?([^"\s]+)"?`)
)

// GetFacts implements FactsProvider. Runs "/system identity print"
// (hostname), "/system resource print" (version/uptime/model for
// non-RouterBOARD platforms like this lab's CHR), "/system routerboard
// print" (serial_number -- and a model override -- on real RouterBOARD
// hardware), and "/interface print terse" (interface_list) -- all
// confirmed live except the routerboard=yes branch: this lab's CHR VM
// always reports "routerboard: no" (it's virtualized, not real
// RouterBOARD hardware), so mikrotikRBModelPattern/mikrotikRBSerialPattern
// are built against RouterOS's documented field names for that case, not
// a real capture. Validate against a real RouterBOARD device before
// relying on serial_number/the routerboard-sourced model in production.
func (d *MikroTikDriver) GetFacts() (map[string]interface{}, error) {
	identityOutput, _, errStr, err := d.runSingle("/system identity print")
	if err != nil {
		return nil, err
	}
	if errStr != "" {
		return nil, olterr.New(olterr.TypeProtocol, "GetFacts: \"/system identity print\" failed: "+errStr)
	}

	resourceOutput, _, errStr, err := d.runSingle("/system resource print")
	if err != nil {
		return nil, err
	}
	if errStr != "" {
		return nil, olterr.New(olterr.TypeProtocol, "GetFacts: \"/system resource print\" failed: "+errStr)
	}

	routerboardOutput, _, errStr, err := d.runSingle("/system routerboard print")
	if err != nil {
		return nil, err
	}
	if errStr != "" {
		return nil, olterr.New(olterr.TypeProtocol, "GetFacts: \"/system routerboard print\" failed: "+errStr)
	}

	interfaceOutput, _, errStr, err := d.runSingle("/interface print terse")
	if err != nil {
		return nil, err
	}
	if errStr != "" {
		return nil, olterr.New(olterr.TypeProtocol, "GetFacts: \"/interface print terse\" failed: "+errStr)
	}

	return parseMikrotikFacts(identityOutput, resourceOutput, routerboardOutput, interfaceOutput), nil
}

// parseMikrotikFacts is GetFacts' parsing logic, pulled out as a pure
// function over already-ANSI-cleaned command output so it's testable
// against fixed fixtures without a live device -- see
// mikrotikdriver_test.go.
func parseMikrotikFacts(identityOutput, resourceOutput, routerboardOutput, interfaceTerseOutput string) map[string]interface{} {
	facts := map[string]interface{}{
		"vendor":         "MikroTik",
		"model":          "",
		"hostname":       "",
		"fqdn":           "",
		"os_version":     "",
		"serial_number":  "",
		"uptime":         "",
		"interface_list": []string{},
	}

	if m := mikrotikIdentityPattern.FindStringSubmatch(identityOutput); m != nil {
		facts["hostname"] = m[1]
		facts["fqdn"] = m[1] // RouterOS's identity has no separate domain-name concept surfaced here
	}
	if m := mikrotikVersionPattern.FindStringSubmatch(resourceOutput); m != nil {
		facts["os_version"] = m[1]
	}
	if m := mikrotikUptimePattern.FindStringSubmatch(resourceOutput); m != nil {
		facts["uptime"] = m[1]
	}
	if m := mikrotikBoardNamePattern.FindStringSubmatch(resourceOutput); m != nil {
		facts["model"] = m[1]
	}

	isRouterboard := false
	if m := mikrotikRouterboardPattern.FindStringSubmatch(routerboardOutput); m != nil {
		isRouterboard = strings.EqualFold(strings.TrimSpace(m[1]), "yes")
	}
	if isRouterboard {
		if m := mikrotikRBModelPattern.FindStringSubmatch(routerboardOutput); m != nil {
			facts["model"] = m[1]
		}
		if m := mikrotikRBSerialPattern.FindStringSubmatch(routerboardOutput); m != nil {
			facts["serial_number"] = m[1]
		}
	}

	names := make([]string, 0)
	for _, m := range mikrotikInterfaceNamePattern.FindAllStringSubmatch(interfaceTerseOutput, -1) {
		names = append(names, m[1])
	}
	sort.Strings(names)
	facts["interface_list"] = names

	return facts
}

// mikrotikRecordFieldPattern extracts one key=value pair from a
// (possibly line-wrapped) "print detail" record -- key is a bare
// identifier, value is either a double-quoted string (RouterOS quotes
// string-typed properties, e.g. name="ether1") or a bare token
// (unquoted, e.g. mtu=1500, mac-address=50:00:00:13:00:00). Deliberately
// does NOT anchor to line starts: RouterOS wraps a single record's
// key=value pairs across multiple physical lines at the terminal width
// (confirmed live, e.g. "/interface print detail"'s "actual-mtu=1500
// mac-address=..." continuing on the line below "name=... mtu=1500"),
// with no repeated index/flags prefix on continuation lines -- searching
// the whole joined record for this pattern sidesteps needing to know
// where those wraps fall.
var mikrotikRecordFieldPattern = regexp.MustCompile(`([a-zA-Z][\w-]*)=("(?:[^"\\]|\\.)*"|\S+)`)

// mikrotikFlagsPattern captures a "print detail" record's leading index
// and single-letter flags column, e.g. " 0  R  name=..." -> flags "R", or
// " 0   address=..." -> flags "" (present but empty -- confirmed live on
// "/ip address print detail" for an entry with no dynamic/disabled/
// invalid flag set).
var mikrotikFlagsPattern = regexp.MustCompile(`^\s*\d+\s+([A-Za-z]*)\s`)

// mikrotikFieldsOf parses record (all physical lines already joined with
// a space) into a key->value map, unquoting any double-quoted value.
func mikrotikFieldsOf(record string) map[string]string {
	fields := make(map[string]string)
	for _, m := range mikrotikRecordFieldPattern.FindAllStringSubmatch(record, -1) {
		key, value := m[1], m[2]
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		fields[key] = value
	}
	return fields
}

// mikrotikPrintDetailRecords splits a "print detail" command's output
// into individual records -- confirmed live that RouterOS separates each
// record with a blank line (both "/interface print detail" and "/ip
// address print detail"/"/ip neighbor print detail" share this
// convention). The very first record shares its blank-line-delimited
// block with the preceding "Flags: ..." legend line (confirmed live: no
// blank line separates the legend from record 0) -- handled by scanning
// each block's own lines for the first one that looks like a record
// start (leading index number) and dropping anything before it, rather
// than requiring the whole block to start that way.
func mikrotikPrintDetailRecords(output string) []string {
	// RouterOS's console uses CRLF line endings (confirmed live) -- a
	// blank separator line is therefore literally "\r\n\r\n", which the
	// "\n\n" split below would never match unless normalized first. A
	// real bug found live: without this, the ENTIRE multi-interface
	// output collapsed into a single block, whose per-record fields then
	// silently overwrote each other in mikrotikFieldsOf's map (last
	// interface wins) -- "/interface print detail" on 4 real interfaces
	// returned only the last one, ether4, with the other three silently
	// gone.
	output = strings.ReplaceAll(output, "\r\n", "\n")

	var records []string
	for _, block := range strings.Split(output, "\n\n") {
		lines := strings.Split(block, "\n")
		start := -1
		for i, line := range lines {
			if mikrotikFlagsPattern.MatchString(line) {
				start = i
				break
			}
		}
		if start == -1 {
			continue
		}
		joined := strings.Join(strings.Fields(strings.Join(lines[start:], " ")), " ")
		if joined == "" {
			continue
		}
		records = append(records, joined)
	}
	return records
}

// GetInterfaces implements InterfaceProvider: "/interface print detail"
// for admin/link state, MTU, and MAC address, merged with "/ip address
// print detail" for IPAddress (matched by the "interface=" field).
// Confirmed live for the up+enabled case (this lab's ether1-ether4 are
// all virtio NICs RouterOS reports as running); the down ("R" flag
// absent) and disabled ("X" flag present) cases are NOT live-confirmed
// -- this lab had no cabled-down or administratively-disabled interface
// to test against.
//
// SpeedMbps deliberately left at its zero value: RouterOS's per-port
// negotiated speed lives in "/interface ethernet monitor", which -- like
// EXOS's live-refreshing "show ports"/"show ports info" bare forms (see
// ExtremeDriver's own doc comment) -- is a self-refreshing live monitor
// screen, not a one-shot printout; sending it through promptdetector.go
// would never see a quiet prompt return and hang until timeout. Not sent
// here for that reason, not because the data isn't wanted.
func (d *MikroTikDriver) GetInterfaces() (map[string]InterfaceInfo, error) {
	interfaceOutput, _, errStr, err := d.runSingle("/interface print detail")
	if err != nil {
		return nil, err
	}
	if errStr != "" {
		return nil, olterr.New(olterr.TypeProtocol, "GetInterfaces: \"/interface print detail\" failed: "+errStr)
	}

	addressOutput, _, errStr, err := d.runSingle("/ip address print detail")
	if err != nil {
		return nil, err
	}
	if errStr != "" {
		return nil, olterr.New(olterr.TypeProtocol, "GetInterfaces: \"/ip address print detail\" failed: "+errStr)
	}

	return parseMikrotikInterfaces(interfaceOutput, addressOutput), nil
}

// parseMikrotikInterfaces is GetInterfaces' parsing logic, pulled out as
// a pure function over already-ANSI-cleaned command output -- see
// mikrotikdriver_test.go.
func parseMikrotikInterfaces(interfaceDetailOutput, addressDetailOutput string) map[string]InterfaceInfo {
	interfaces := make(map[string]InterfaceInfo)
	for _, record := range mikrotikPrintDetailRecords(interfaceDetailOutput) {
		flags := mikrotikFlagsPattern.FindStringSubmatch(record)[1]
		fields := mikrotikFieldsOf(record)
		name := fields["name"]
		if name == "" {
			continue
		}

		mtu := 0
		mtuStr := fields["actual-mtu"]
		if mtuStr == "" {
			mtuStr = fields["mtu"]
		}
		if n, convErr := strconv.Atoi(mtuStr); convErr == nil {
			mtu = n
		}

		interfaces[name] = InterfaceInfo{
			IsUp:        strings.Contains(flags, "R"),
			IsEnabled:   !strings.Contains(flags, "X"),
			Description: fields["comment"],
			MTU:         mtu,
			MACAddress:  fields["mac-address"],
		}
	}

	for _, record := range mikrotikPrintDetailRecords(addressDetailOutput) {
		fields := mikrotikFieldsOf(record)
		name := fields["interface"]
		address := fields["address"]
		if name == "" || address == "" {
			continue
		}
		info, ok := interfaces[name]
		if !ok {
			continue // an address on an interface "/interface print detail" didn't list -- shouldn't happen live, skip rather than fabricate a bare entry
		}
		info.IPAddress = address
		interfaces[name] = info
	}

	return interfaces
}

// GetLLDPNeighbors implements LLDPNeighborsProvider via "/ip neighbor
// print detail" -- RouterOS's unified neighbor-discovery table, which
// surfaces MNDP (MikroTik's own protocol), CDP, AND LLDP neighbors
// together rather than an LLDP-only command the way Cisco/Junos have.
// NOT LIVE-VERIFIED: this lab's device has no neighbor announcing over
// any of the three protocols ("/ip neighbor print detail" confirmed live
// to produce empty output, same "zero neighbors" shape as
// ExtremeDriver's own live-confirmed empty case) -- the populated-record
// field names below ("identity"/"interface-name") are RouterOS's
// documented property names for this command, not a real capture.
// Validate against a real neighbor before relying on this in production,
// the same posture CiscoIOSDriver/ExtremeDriver's own LLDP parsers
// document for their own populated-row shape.
func (d *MikroTikDriver) GetLLDPNeighbors() (map[string][]LLDPNeighbor, error) {
	output, _, errStr, err := d.runSingle("/ip neighbor print detail")
	if err != nil {
		return nil, err
	}
	if errStr != "" {
		return nil, olterr.New(olterr.TypeProtocol, "GetLLDPNeighbors: \"/ip neighbor print detail\" failed: "+errStr)
	}

	return parseMikrotikLLDPNeighbors(output), nil
}

// parseMikrotikLLDPNeighbors is GetLLDPNeighbors' parsing logic, pulled
// out as a pure function over already-ANSI-cleaned command output -- see
// mikrotikdriver_test.go and GetLLDPNeighbors' own doc comment for the
// live-verification caveat on non-empty input.
func parseMikrotikLLDPNeighbors(output string) map[string][]LLDPNeighbor {
	result := make(map[string][]LLDPNeighbor)
	for _, record := range mikrotikPrintDetailRecords(output) {
		fields := mikrotikFieldsOf(record)
		localPort := fields["interface"]
		if localPort == "" {
			continue
		}
		remoteName := fields["identity"]
		remotePort := fields["interface-name"]
		if remotePort == "" {
			remotePort = fields["mac-address"] // no remote interface name field -- fall back to MAC, same "something over nothing" posture ExtremeDriver's own parser takes
		}
		result[localPort] = append(result[localPort], LLDPNeighbor{
			RemoteSystemName: remoteName,
			RemotePortID:     remotePort,
		})
	}
	return result
}

// GetConfig implements ConfigGetter: "/export", verbatim -- RouterOS's
// own config-as-script dump (re-playable via "/import"), the closest
// RouterOS equivalent to Cisco's "show running-config"/Junos's "show".
// Confirmed live complete and untruncated against this lab's (small)
// configuration; NOT confirmed against a config large enough to approach
// this codebase's MaxOutputBytes cap or RouterOS's own pager (mitigated,
// not eliminated, by mikrotikLoginModifiers' huge reported terminal
// height -- see the type doc comment).
func (d *MikroTikDriver) GetConfig() (string, error) {
	output, truncated, errStr, err := d.runSingle("/export")
	if err != nil {
		return "", err
	}
	if errStr != "" {
		return "", olterr.New(olterr.TypeProtocol, "GetConfig: \"/export\" failed: "+errStr)
	}
	if truncated {
		return "", olterr.New(
			olterr.TypeProtocol,
			`GetConfig: "/export" response was truncated -- treating as failed rather than returning partial config`,
		)
	}
	return output, nil
}
