package drivers

import (
	"reflect"
	"strings"
	"testing"

	"github.com/onu-platform/gearctl/internal/olterr"
	"github.com/onu-platform/gearctl/internal/transport"
)

// Canned `show version` output in the standard Cisco IOS format. See
// ciscoiosdriver.go's doc comment: this has not been verified against a
// real device, only against the documented output shape.
const sampleShowVersion = `Cisco IOS Software, C3560E Software (C3560E-UNIVERSALK9-M), Version 15.2(2)E1, RELEASE SOFTWARE (fc3)
Technical Support: http://www.cisco.com/techsupport
Copyright (c) 1986-2014 by Cisco Systems, Inc.
Compiled Wed 22-Oct-14 03:20 by prod_rel_team

ROM: Bootstrap program is C3560E boot loader
BOOTLDR: C3560E Boot Loader (C3560E-HBOOT-M) Version 12.2(53r)SE2, RELEASE SOFTWARE (fc1)

switch1 uptime is 6 weeks, 3 days, 4 hours, 22 minutes
System returned to ROM by power-on
System restarted at 09:23:41 UTC Mon Jan 5 2026
System image file is "flash:/c3560e-universalk9-mz.152-2.E1/c3560e-universalk9-mz.152-2.E1.bin"

cisco WS-C3560E-24TD (PowerPC405) processor (revision A0) with 131072K bytes of memory.
Processor board ID FOC1234X56Y
Last reset from power-on
1 Virtual Ethernet interface
24 Gigabit Ethernet interfaces
2 Ten Gigabit Ethernet interfaces
The password-recovery mechanism is enabled.

512K bytes of flash-simulated non-volatile configuration memory.
Base ethernet MAC Address       : 00:1A:2B:3C:4D:5E
Motherboard assembly number     : 73-11005-06
Configuration register is 0xF
`

const sampleShowIPInterfaceBrief = `Interface              IP-Address      OK? Method Status                Protocol
Vlan1                   unassigned      YES NVRAM  administratively down down
GigabitEthernet0/1     192.168.1.1     YES NVRAM  up                    up
GigabitEthernet0/2     unassigned      YES unset  down                  down
`

// sampleShowInterfaces is real "show interfaces" output captured live
// against a Cisco ISRv (IOS-XE 17.01.01) -- unlike sampleShowVersion
// above, every line shape here (including "Description:"/"Internet
// address is ..." appearing only when actually configured, and the
// exact "administratively down" admin-status wording) has been
// confirmed against the real device, not just the documented format.
// Covers three interfaces in one fixture: up with an IP configured but
// no description, up with a description configured but no IP, and
// administratively down with neither.
const sampleShowInterfaces = `GigabitEthernet1 is up, line protocol is up
  Hardware is CSR vNIC, address is 5000.0001.0000 (bia 5000.0001.0000)
  Internet address is 192.168.14.13/24
  MTU 1500 bytes, BW 1000000 Kbit/sec, DLY 10 usec,
     reliability 255/255, txload 1/255, rxload 1/255
  Encapsulation ARPA, loopback not set
  Keepalive set (10 sec)
  Full Duplex, 1000Mbps, link type is auto, media type is Virtual
  output flow-control is unsupported, input flow-control is unsupported
  ARP type: ARPA, ARP Timeout 04:00:00
  Last input never, output never, output hang never
GigabitEthernet2 is up, line protocol is up
  Hardware is CSR vNIC, address is 5000.0001.0001 (bia 5000.0001.0001)
  Description: sample-desc-for-parsing
  MTU 1500 bytes, BW 1000000 Kbit/sec, DLY 10 usec,
     reliability 255/255, txload 1/255, rxload 1/255
  Encapsulation ARPA, loopback not set
  Keepalive set (10 sec)
  Full Duplex, 1000Mbps, link type is auto, media type is Virtual
GigabitEthernet3 is administratively down, line protocol is down
  Hardware is CSR vNIC, address is 5000.0001.0002 (bia 5000.0001.0002)
  MTU 1500 bytes, BW 1000000 Kbit/sec, DLY 10 usec,
     reliability 255/255, txload 1/255, rxload 1/255
  Encapsulation ARPA, loopback not set
`

func TestCiscoIOSDriverVendor(t *testing.T) {
	if got := NewCiscoIOSDriver().Vendor(); got != "cisco" {
		t.Errorf("Vendor() = %q, want \"cisco\"", got)
	}
}

func TestCiscoIOSDriverGetFactsParsesShowVersionAndInterfaces(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show version":            {output: sampleShowVersion},
		"show ip interface brief": {output: sampleShowIPInterfaceBrief},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	facts, err := d.GetFacts()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[string]interface{}{
		"vendor":        "Cisco",
		"model":         "WS-C3560E-24TD",
		"hostname":      "switch1",
		"fqdn":          "switch1",
		"os_version":    "15.2(2)E1",
		"serial_number": "FOC1234X56Y",
		"uptime":        "6 weeks, 3 days, 4 hours, 22 minutes",
		"interface_list": []string{
			"Vlan1", "GigabitEthernet0/1", "GigabitEthernet0/2",
		},
	}
	for key, wantVal := range want {
		gotVal := facts[key]
		if !reflect.DeepEqual(gotVal, wantVal) {
			t.Errorf("facts[%q] = %#v, want %#v", key, gotVal, wantVal)
		}
	}
}

// TestCiscoIOSDriverGetFactsDisablesPagingFirst is a regression test:
// confirmed live against a real Cisco ISRv that a multi-page command got
// silently truncated because paging was never disabled -- see the doc
// comment on GetConfig. GetFacts's commands are shorter and less likely
// to paginate in practice, but the fix applies uniformly for consistency
// and defense-in-depth.
func TestCiscoIOSDriverGetFactsDisablesPagingFirst(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show version":            {output: sampleShowVersion},
		"show ip interface brief": {output: sampleShowIPInterfaceBrief},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, err := d.GetFacts(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.sent) < 1 || fake.sent[0] != "terminal length 0" {
		t.Errorf("sent = %v, want \"terminal length 0\" sent first", fake.sent)
	}
}

func TestCiscoIOSDriverGetFactsFailsIfEitherCommandFails(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show version": {err: olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt")},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, err := d.GetFacts(); err == nil {
		t.Fatal("expected an error when the show version command fails")
	}
}

func TestCiscoIOSDriverGetConfigReturnsRawOutput(t *testing.T) {
	const sampleRunningConfig = "version 17.1\nhostname ISR\ninterface GigabitEthernet1\n no shutdown\nend\n"
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: sampleRunningConfig},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	config, err := d.GetConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if config != sampleRunningConfig {
		t.Errorf("GetConfig() = %q, want %q (verbatim, no parsing)", config, sampleRunningConfig)
	}
}

// TestCiscoIOSDriverGetConfigDisablesPagingFirst is the regression test
// for the real truncation bug found live: "terminal length 0" must be
// sent before "show running-config" so a multi-page config doesn't get
// silently cut off at the first page (see GetConfig's doc comment).
func TestCiscoIOSDriverGetConfigDisablesPagingFirst(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {output: "hostname ISR\n"},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, err := d.GetConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.sent) < 2 || fake.sent[0] != "terminal length 0" || fake.sent[1] != "show running-config" {
		t.Errorf("sent = %v, want [\"terminal length 0\", \"show running-config\"]", fake.sent)
	}
}

func TestCiscoIOSDriverGetConfigFailsIfCommandFails(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show running-config": {err: olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt")},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, err := d.GetConfig(); err == nil {
		t.Fatal("expected an error when the show running-config command fails")
	}
}

func TestParseInterfaceBriefSkipsHeaderAndBlankLines(t *testing.T) {
	got := parseInterfaceBrief("Interface  IP-Address\n\nGigabitEthernet0/1  1.2.3.4\n")
	want := []string{"GigabitEthernet0/1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseInterfaceBrief = %#v, want %#v", got, want)
	}
}

// TestParseInterfaceBriefSkipsTrailingPromptLine is a regression test for
// a bug found live against a real Cisco ISRv (IOS-XE 17.01.01): the raw
// output carried a trailing "ISR#" prompt line after the last real row,
// which a naive "skip the header, take everything else" parser picked up
// as a fake interface named "ISR#".
func TestParseInterfaceBriefSkipsTrailingPromptLine(t *testing.T) {
	got := parseInterfaceBrief(sampleShowIPInterfaceBrief + "ISR#\n")
	want := []string{"Vlan1", "GigabitEthernet0/1", "GigabitEthernet0/2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseInterfaceBrief = %#v, want %#v (the trailing prompt line must be filtered out)", got, want)
	}
}

// TestParseInterfacesParsesRealCiscoOutput exercises parseInterfaces
// against sampleShowInterfaces -- real, live-captured output covering
// up-with-no-description, up-with-a-description, and
// administratively-down in one fixture.
func TestParseInterfacesParsesRealCiscoOutput(t *testing.T) {
	got := parseInterfaces(sampleShowInterfaces)
	if len(got) != 3 {
		t.Fatalf("got %d interfaces, want 3: %#v", len(got), got)
	}

	gi1, ok := got["GigabitEthernet1"]
	if !ok {
		t.Fatal("GigabitEthernet1 missing")
	}
	if !gi1.IsUp || !gi1.IsEnabled {
		t.Errorf("GigabitEthernet1 = %+v, want IsUp=true IsEnabled=true", gi1)
	}
	if gi1.MACAddress != "5000.0001.0000" {
		t.Errorf("GigabitEthernet1.MACAddress = %q, want %q", gi1.MACAddress, "5000.0001.0000")
	}
	if gi1.Description != "" {
		t.Errorf("GigabitEthernet1.Description = %q, want \"\" (none configured)", gi1.Description)
	}
	if gi1.IPAddress != "192.168.14.13/24" {
		t.Errorf("GigabitEthernet1.IPAddress = %q, want %q", gi1.IPAddress, "192.168.14.13/24")
	}
	if gi1.MTU != 1500 {
		t.Errorf("GigabitEthernet1.MTU = %d, want 1500", gi1.MTU)
	}
	if gi1.SpeedMbps != 1000 {
		t.Errorf("GigabitEthernet1.SpeedMbps = %v, want 1000", gi1.SpeedMbps)
	}

	gi2, ok := got["GigabitEthernet2"]
	if !ok {
		t.Fatal("GigabitEthernet2 missing")
	}
	if gi2.Description != "sample-desc-for-parsing" {
		t.Errorf("GigabitEthernet2.Description = %q, want %q", gi2.Description, "sample-desc-for-parsing")
	}
	if gi2.IPAddress != "" {
		t.Errorf("GigabitEthernet2.IPAddress = %q, want \"\" (none configured)", gi2.IPAddress)
	}
	if gi2.MACAddress != "5000.0001.0001" {
		t.Errorf("GigabitEthernet2.MACAddress = %q, want %q", gi2.MACAddress, "5000.0001.0001")
	}

	gi3, ok := got["GigabitEthernet3"]
	if !ok {
		t.Fatal("GigabitEthernet3 missing")
	}
	if gi3.IsUp {
		t.Error("GigabitEthernet3.IsUp = true, want false (administratively down)")
	}
	if gi3.IsEnabled {
		t.Error("GigabitEthernet3.IsEnabled = true, want false (administratively down)")
	}
}

func TestCiscoIOSDriverGetInterfacesReturnsParsedState(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show interfaces": {output: sampleShowInterfaces},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	got, err := d.GetInterfaces()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d interfaces, want 3: %#v", len(got), got)
	}
	if !got["GigabitEthernet1"].IsUp {
		t.Errorf("GigabitEthernet1 = %+v, want IsUp=true", got["GigabitEthernet1"])
	}
}

// TestCiscoIOSDriverGetInterfacesDisablesPagingFirst mirrors
// TestCiscoIOSDriverGetConfigDisablesPagingFirst -- same real-hardware
// truncation bug, same fix, same regression test shape.
func TestCiscoIOSDriverGetInterfacesDisablesPagingFirst(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show interfaces": {output: sampleShowInterfaces},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, err := d.GetInterfaces(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.sent) < 2 || fake.sent[0] != "terminal length 0" || fake.sent[1] != "show interfaces" {
		t.Errorf("sent = %v, want [\"terminal length 0\", \"show interfaces\"]", fake.sent)
	}
}

func TestCiscoIOSDriverGetInterfacesFailsIfCommandFails(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show interfaces": {err: olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt")},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, err := d.GetInterfaces(); err == nil {
		t.Fatal("expected an error when the show interfaces command fails")
	}
}

// sampleShowLLDPNeighborsEmpty is real "show lldp neighbors" output
// captured live against a Cisco ISRv (IOS-XE 17.01.01) after enabling
// LLDP (`lldp run`) -- the zero-neighbor case, since this lab has no
// other LLDP-speaking device on the same segment. The header line and
// "Total entries displayed: 0" trailer are exactly as observed live.
const sampleShowLLDPNeighborsEmpty = `Capability codes:
    (R) Router, (B) Bridge, (T) Telephone, (C) DOCSIS Cable Device
    (W) WLAN Access Point, (P) Repeater, (S) Station, (O) Other

Device ID           Local Intf     Hold-time  Capability      Port ID

Total entries displayed: 0
`

// sampleShowLLDPNeighborsPopulated is NOT live-verified -- built against
// the well-documented Cisco IOS format (see parseLLDPNeighbors's doc
// comment for why no populated real sample was available). Two rows,
// deliberately with a multi-value Capability field ("B,R") on one to
// pin that port ID extraction (the LAST field) doesn't get confused by
// the Capability column's variable width.
const sampleShowLLDPNeighborsPopulated = `Capability codes:
    (R) Router, (B) Bridge, (T) Telephone, (C) DOCSIS Cable Device
    (W) WLAN Access Point, (P) Repeater, (S) Station, (O) Other

Device ID           Local Intf     Hold-time  Capability      Port ID
switch1.example.com Gi1            120        B,R             Gi0/1
SEP001122334455     Gi2            180        T               1/1

Total entries displayed: 2
`

func TestParseLLDPNeighborsHandlesEmptyTable(t *testing.T) {
	got := parseLLDPNeighbors(sampleShowLLDPNeighborsEmpty)
	if len(got) != 0 {
		t.Errorf("got %#v, want an empty map", got)
	}
}

func TestParseLLDPNeighborsParsesPopulatedTable(t *testing.T) {
	got := parseLLDPNeighbors(sampleShowLLDPNeighborsPopulated)
	if len(got) != 2 {
		t.Fatalf("got %d local interfaces, want 2: %#v", len(got), got)
	}

	gi1, ok := got["Gi1"]
	if !ok || len(gi1) != 1 {
		t.Fatalf("Gi1 = %#v, want exactly one neighbor", got["Gi1"])
	}
	if gi1[0].RemoteSystemName != "switch1.example.com" || gi1[0].RemotePortID != "Gi0/1" {
		t.Errorf("Gi1 neighbor = %+v, want {switch1.example.com Gi0/1}", gi1[0])
	}

	gi2, ok := got["Gi2"]
	if !ok || len(gi2) != 1 {
		t.Fatalf("Gi2 = %#v, want exactly one neighbor", got["Gi2"])
	}
	if gi2[0].RemoteSystemName != "SEP001122334455" || gi2[0].RemotePortID != "1/1" {
		t.Errorf("Gi2 neighbor = %+v, want {SEP001122334455 1/1}", gi2[0])
	}
}

func TestCiscoIOSDriverGetLLDPNeighborsReturnsParsedTable(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show lldp neighbors": {output: sampleShowLLDPNeighborsPopulated},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	got, err := d.GetLLDPNeighbors()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d local interfaces, want 2: %#v", len(got), got)
	}
}

// TestCiscoIOSDriverGetLLDPNeighborsDisablesPagingFirst mirrors
// TestCiscoIOSDriverGetConfigDisablesPagingFirst -- same real-hardware
// truncation bug, same fix, same regression test shape.
func TestCiscoIOSDriverGetLLDPNeighborsDisablesPagingFirst(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show lldp neighbors": {output: sampleShowLLDPNeighborsEmpty},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, err := d.GetLLDPNeighbors(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.sent) < 2 || fake.sent[0] != "terminal length 0" || fake.sent[1] != "show lldp neighbors" {
		t.Errorf("sent = %v, want [\"terminal length 0\", \"show lldp neighbors\"]", fake.sent)
	}
}

func TestCiscoIOSDriverGetLLDPNeighborsFailsIfCommandFails(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show lldp neighbors": {err: olterr.New(olterr.TypeTimeout, "Timed out waiting for prompt")},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, err := d.GetLLDPNeighbors(); err == nil {
		t.Fatal("expected an error when the show lldp neighbors command fails")
	}
}

// TestCiscoIOSDriverGetLLDPNeighborsReportsLLDPDisabled is the
// regression test for the real-hardware finding: this exact device
// initially had LLDP disabled and replied "% LLDP is not enabled" to
// "show lldp neighbors" -- a naive parser would have silently returned
// an empty map, indistinguishable from "LLDP is on, zero neighbors".
// GetLLDPNeighbors must surface this as a distinct, descriptive error
// instead.
func TestCiscoIOSDriverGetLLDPNeighborsReportsLLDPDisabled(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show lldp neighbors": {output: "% LLDP is not enabled\n"},
	}}
	d := &CiscoIOSDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	_, err := d.GetLLDPNeighbors()
	if err == nil {
		t.Fatal("expected an error when LLDP is not enabled")
	}
	if !strings.Contains(err.Error(), "not enabled") {
		t.Errorf("err = %v, want it to mention LLDP is not enabled", err)
	}
}
