package drivers

import (
	"reflect"
	"testing"

	"github.com/onu-platform/gearctl/internal/transport"
)

// All fixtures below are byte-for-byte real captures (CRLF normalized to
// "\n" for readability -- the CR bytes are handled by TrimRight("\r") in
// the parsing code, not by these fixtures being pre-cleaned in a way
// production input wouldn't be) taken live against a real EXOS-VM
// (ExtremeXOS 32.7.2.19) at 192.168.14.16, except where a doc comment
// says otherwise.

const sampleShowVersionExtreme = `Switch          : PN:1N2039    SIM0012-0000 Rev 01 BootROM: 1.2        IMG: 32.7.2.19
PSUCTRL-1       : PN:MEAD      SN:MD1      Rev 01 BootROM: 2.1
PSUCTRL-2       : PN:MEAD      SN:MD2      Rev 11 BootROM: 2.3
mouse-usb       : PN:MOUSE     SN:4321     Rev 11 BootROM: 4.3
floppy-A        :
PSU-1       : Internal PSU-2 PN:1N2039 SN:12345
PSU-2       : Internal PSU-3 PN:1N2039 SN:12345

Image   : ExtremeXOS version 32.7.2.19 by release-manager
          on Thu Jan 16 07:54:39 AM EST 2025
BootROM : 1.2
Diagnostics :
Certified Version : EXOS Linux 5.10.177, Extreme Networks FIPS Object Module 2.0.16

Build Tools Version : exos-x86_64-sdk-3.1.19.1.0
`

const sampleShowSwitchExtreme = `SysName:          EXOS-VM
SysLocation:
SysContact:       https://www.extremenetworks.com/support/
System MAC:       50:00:00:12:00:00
System Type:      EXOS-VM

SysHealth check:  Enabled (Normal)
Recovery Mode:    All
System Watchdog:  Enabled

Current Time:     Sat Aug 22 18:18:35 2026
Timezone:         [Auto DST Disabled] GMT Offset: 0 minutes, name is UTC.
Boot Time:        Sat Aug 22 18:03:30 2026
Boot Count:       2
Next Reboot:      None scheduled
System UpTime:    15 minutes 5 seconds

Current State:    OPERATIONAL
Image Selected:   primary
Image Booted:     primary
Primary ver:      32.7.2.19
Secondary ver:    32.7.2.19

Config Selected:  primary.cfg
Config Booted:    Factory Default
Config Automatic: NONE (Disabled)

primary.cfg       Created by ExtremeXOS version 32.7.2.19
                  158810 bytes saved on Sat Aug 22 18:13:44 2026


LAA MAC:          Locally Administered MAC Address Disabled
`

// sampleShowPortsInformationExtreme: both ports administratively disabled
// (this lab's ports have no cable attached), so only the IsUp=false path
// is confirmed live -- see parseExtremePortsInformation's doc comment.
const sampleShowPortsInformationExtreme = `                                        ELSM/
Port      Flags               Link       OAM/   Link Num  Num   Num Jumbo QOS     Load
                              State     MACsec   UPS STP VLAN Proto  Size profile Master
========================================================================================
1         Dm------e--fMB----- ready     - / -/ -   0   1    1     1  9216 none
2         Dm------e--fMB----- ready     - / -/ -   0   1    1     1  9216 none
========================================================================================
> indicates Port Display Name truncated past 8 characters
Flags : a - Load Sharing Algorithm address-based,
        B - Broadcast Flooding Enabled,
        D - Port Disabled, e - Extreme Discovery Protocol Enabled,
        E - Port Enabled, f - Unicast Flooding Enabled,
        G - MLAG Enabled, i - Isolation,
        j - Jumbo Frame Enabled, l - Load Sharing Enabled,
        L - Extreme Link Status Monitoring Enabled,
        m - MACLearning Enabled, M - Multicast Flooding Enabled,
        n - Ingress TOS Enabled, o - Dot1p Replacement Enabled,
        O - Ethernet OAM Enabled, p - Load Sharing Algorithm port-based,
        P - Software redundant port(Primary),
        r - Load Sharing Algorithm round-robin,
        R - Software redundant port(Redundant),
        s - diffserv Replacement Enabled, S - MACsec Enabled,
        v - Vman Enabled
`

// sampleShowPortsDescriptionExtreme: port 1 has both a display-string and
// a description-string set (via "configure ports 1 description-string
// ..." / "... display-string ..."), port 2 has neither -- confirmed live
// this renders as a bare trailing-spaces row, not an omitted one.
const sampleShowPortsDescriptionExtreme = `Port   Display String        Description String
====== ====================  ==================================================
1      test1234567           uplink-to-core
2
=====  ====================  ==================================================
`

// sampleShowConfigurationExtreme is a real (truncated for fixture
// brevity -- the live capture ran 252 lines across every EXOS module)
// "show configuration" excerpt covering the confirmed-live shape:
// per-module "# Module <name> configuration." comment headers, most
// emitted empty, with actual non-default settings appearing as plain
// top-level commands under their module -- no indentation/stanza nesting
// anywhere, unlike Cisco's "show running-config". See GetConfig's doc
// comment for why this is a "diff from factory default" script, not a
// full effective-state dump.
const sampleShowConfigurationExtreme = `#
# Module devmgr configuration.
#
configure sys-recovery-level switch reset

#
# Module vlan configuration.
#
configure vlan default delete ports all
configure vr VR-Default delete ports 1-2
configure vr VR-Default add ports 1-2
disable port 1
disable port 2
configure vlan Default add ports 1-2 untagged
configure vlan Mgmt ipaddress 192.168.14.16 255.255.255.0

#
# Module mcmgr configuration.
#

#
# Module exsshd configuration.
#
enable ssh2
`

func TestExtremeDriverVendor(t *testing.T) {
	if got := NewExtremeDriver().Vendor(); got != "extreme" {
		t.Errorf("Vendor() = %q, want \"extreme\"", got)
	}
}

func TestExtremeDriverGetFactsParsesRealCaptures(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		extremeDisablePagingCommand: {output: ""},
		"show version":              {output: sampleShowVersionExtreme},
		"show switch":               {output: sampleShowSwitchExtreme},
		"show ports information":    {output: sampleShowPortsInformationExtreme},
	}}
	d := &ExtremeDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	facts, err := d.GetFacts()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[string]interface{}{
		"vendor":         "Extreme",
		"model":          "EXOS-VM",
		"hostname":       "EXOS-VM",
		"fqdn":           "EXOS-VM",
		"os_version":     "32.7.2.19",
		"serial_number":  "", // confirmed live: this VM's Switch line has no "SN:" token
		"uptime":         "15 minutes 5 seconds",
		"interface_list": []string{"1", "2"},
	}
	for key, wantVal := range want {
		gotVal := facts[key]
		if !reflect.DeepEqual(gotVal, wantVal) {
			t.Errorf("facts[%q] = %#v, want %#v", key, gotVal, wantVal)
		}
	}
}

// TestExtremeSerialPatternIgnoresComponentSerials is a regression test:
// sampleShowVersionExtreme contains several OTHER "SN:" tokens
// (PSUCTRL-1/PSUCTRL-2/PSU-1/PSU-2 component serials) that appear before
// any Switch-line match would -- an earlier, unanchored version of
// extremeSerialPattern (a bare `SN:(\S+)`) wrongly matched
// PSUCTRL-1's "SN:MD1" instead of correctly finding no Switch-line serial
// at all. See extremeSerialPattern's doc comment.
func TestExtremeSerialPatternIgnoresComponentSerials(t *testing.T) {
	if m := extremeSerialPattern.FindStringSubmatch(sampleShowVersionExtreme); m != nil {
		t.Errorf("extremeSerialPattern matched %q, want no match (this fixture's Switch line has no SN: token)", m[1])
	}
}

func TestExtremeDriverGetFactsDisablesPagingFirst(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		extremeDisablePagingCommand: {output: ""},
		"show version":              {output: sampleShowVersionExtreme},
		"show switch":               {output: sampleShowSwitchExtreme},
		"show ports information":    {output: sampleShowPortsInformationExtreme},
	}}
	d := &ExtremeDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, err := d.GetFacts(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.sent) == 0 || fake.sent[0] != extremeDisablePagingCommand {
		t.Errorf("first command sent = %v, want %q first", fake.sent, extremeDisablePagingCommand)
	}
}

func TestExtremeDriverGetConfigReturnsRawOutput(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		extremeDisablePagingCommand: {output: ""},
		"show configuration":        {output: sampleShowConfigurationExtreme},
	}}
	d := &ExtremeDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	got, err := d.GetConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != sampleShowConfigurationExtreme {
		t.Errorf("GetConfig() = %q, want verbatim sampleShowConfigurationExtreme", got)
	}
}

func TestExtremeDriverGetConfigFailsOnTruncation(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		extremeDisablePagingCommand: {output: ""},
		"show configuration":        {output: "partial...", truncated: true},
	}}
	d := &ExtremeDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, err := d.GetConfig(); err == nil {
		t.Fatal("expected an error for truncated output, got nil")
	}
}

func TestExtremeDriverGetInterfacesMergesInfoAndDescription(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		extremeDisablePagingCommand: {output: ""},
		"show ports information":    {output: sampleShowPortsInformationExtreme},
		"show ports description":    {output: sampleShowPortsDescriptionExtreme},
	}}
	d := &ExtremeDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	got, err := d.GetInterfaces()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[string]InterfaceInfo{
		"1": {IsEnabled: false, IsUp: false, Description: "uplink-to-core"},
		"2": {IsEnabled: false, IsUp: false, Description: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetInterfaces() = %#v, want %#v", got, want)
	}
}

func TestParseExtremePortsDescriptionHandlesEmptyDescription(t *testing.T) {
	got := parseExtremePortsDescription(sampleShowPortsDescriptionExtreme)
	want := map[string]string{"1": "uplink-to-core", "2": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseExtremePortsDescription() = %#v, want %#v", got, want)
	}
}

func TestExtremeDriverGetLLDPNeighborsReturnsEmptyMapForEmptyOutput(t *testing.T) {
	// Confirmed live: zero neighbors means "show lldp neighbors" prints
	// nothing at all -- no header, no trailer.
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		extremeDisablePagingCommand: {output: ""},
		"show lldp neighbors":       {output: ""},
	}}
	d := &ExtremeDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	got, err := d.GetLLDPNeighbors()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("GetLLDPNeighbors() = %#v, want empty map", got)
	}
}

func TestPredictExtremePromptIncrementsCounter(t *testing.T) {
	got := predictExtremePrompt("EXOS-VM.1 #", "show version")
	want := "EXOS-VM.2 #"
	if got != want {
		t.Errorf("predictExtremePrompt() = %q, want %q", got, want)
	}
}

func TestPredictExtremePromptTurnsDirtyOnMutatingCommand(t *testing.T) {
	got := predictExtremePrompt("EXOS-VM.1 #", "disable port 1")
	want := "* EXOS-VM.2 #"
	if got != want {
		t.Errorf("predictExtremePrompt() = %q, want %q", got, want)
	}
}

func TestPredictExtremePromptStaysDirtyOnceSet(t *testing.T) {
	// Confirmed live: the "* " prefix never clears itself back off short
	// of "save configuration" -- a plain "show" command after a mutating
	// one must still predict a dirty prompt.
	got := predictExtremePrompt("* EXOS-VM.2 #", "show version")
	want := "* EXOS-VM.3 #"
	if got != want {
		t.Errorf("predictExtremePrompt() = %q, want %q", got, want)
	}
}

func TestPredictExtremePromptReturnsEmptyForUnrecognizedShape(t *testing.T) {
	if got := predictExtremePrompt("not-an-extreme-prompt", "show version"); got != "" {
		t.Errorf("predictExtremePrompt() = %q, want \"\" (no prediction)", got)
	}
	if got := predictExtremePrompt("", "show version"); got != "" {
		t.Errorf("predictExtremePrompt(\"\", ...) = %q, want \"\"", got)
	}
}
