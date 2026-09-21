package drivers

import (
	"reflect"
	"testing"
)

// Fixtures below are hand-built to match VRP's CONFIRMED-LIVE output
// shape (see huaweidriver.go's doc comments for exactly what was
// verified against a real Huawei S6700 core switch) but with synthetic
// values -- not the real device's own hostname/interface
// names/descriptions, which belong to a live production network and
// don't belong committed to this repo. Where the real device's output
// format itself mattered (e.g. the "Quidway" vs "HUAWEI" prefix below),
// the fixture reproduces that exact format.

const huaweiVersionFixture = `Huawei Versatile Routing Platform Software
VRP (R) software, Version 5.170 (S5720 V200R011C10SPC600)
Copyright (C) 2000-2018 HUAWEI TECH CO., LTD
HUAWEI S5720-28X-LI-AC Routing Switch uptime is 0 week, 2 days, 3 hours, 17 minutes
`

// huaweiVersionQuidwayFixture reproduces the REAL prefix wording a live
// Huawei S6700 (VRP 5.150, V200R005C00SPC500) sent instead of "HUAWEI"
// -- confirmed live; an earlier version of huaweiModelUptimePattern that
// only accepted "HUAWEI" silently left model/uptime empty against this
// exact real output.
const huaweiVersionQuidwayFixture = `Huawei Versatile Routing Platform Software
VRP (R) software, Version 5.150 (S6700 V200R005C00SPC500)
Copyright (C) 2000-2015 HUAWEI TECH CO., LTD
Quidway S6700-24-EI Routing Switch uptime is 12 weeks, 0 day, 6 hours, 55 minutes
`

const huaweiInterfaceBriefFixture = `PHY: Physical
*down: administratively down
^down: standby
(l): loopback
(s): spoofing
(b): BFD down
InUti/OutUti: input utility/output utility
Interface                   PHY   Protocol InUti OutUti   inErrors  outErrors
GigabitEthernet0/0/1        up    up          0%     0%          0          0
GigabitEthernet0/0/2        *down down         0%     0%          0          0
MEth0/0/1                   up    up          0%     0%          0          0
`

func TestHuaweiGetFactsParsesDocumentedFixtures(t *testing.T) {
	facts := parseHuaweiFacts(huaweiVersionFixture, "<CoreSwitch01>", huaweiInterfaceBriefFixture)
	want := map[string]interface{}{
		"vendor":         "Huawei",
		"model":          "S5720-28X-LI-AC",
		"hostname":       "CoreSwitch01",
		"fqdn":           "CoreSwitch01",
		"os_version":     "5.170",
		"serial_number":  "",
		"uptime":         "0 week, 2 days, 3 hours, 17 minutes",
		"interface_list": []string{"GigabitEthernet0/0/1", "GigabitEthernet0/0/2", "MEth0/0/1"},
	}
	if !reflect.DeepEqual(facts, want) {
		t.Fatalf("facts = %#v, want %#v", facts, want)
	}
}

func TestHuaweiGetFactsParsesQuidwayBrandedVersionLine(t *testing.T) {
	facts := parseHuaweiFacts(huaweiVersionQuidwayFixture, "<Core_Switch_Kly>", huaweiInterfaceBriefFixture)
	if facts["model"] != "S6700-24-EI" {
		t.Errorf("model = %v, want S6700-24-EI (the \"Quidway\"-prefixed line must parse the same as the \"HUAWEI\"-prefixed one)", facts["model"])
	}
	if facts["uptime"] != "12 weeks, 0 day, 6 hours, 55 minutes" {
		t.Errorf("uptime = %v, want \"12 weeks, 0 day, 6 hours, 55 minutes\"", facts["uptime"])
	}
	if facts["os_version"] != "5.150" {
		t.Errorf("os_version = %v, want 5.150", facts["os_version"])
	}
	if facts["hostname"] != "Core_Switch_Kly" || facts["fqdn"] != "Core_Switch_Kly" {
		t.Errorf("hostname/fqdn = %v/%v, want Core_Switch_Kly/Core_Switch_Kly", facts["hostname"], facts["fqdn"])
	}
}

func TestParseHuaweiPromptHostname(t *testing.T) {
	cases := map[string]string{
		"<CoreSwitch01>":        "CoreSwitch01",
		"<Core_Switch_Kly>":     "Core_Switch_Kly",
		"[CoreSwitch01]":        "CoreSwitch01",
		"[CoreSwitch01-vlan10]": "CoreSwitch01",
		"  <CoreSwitch01>  ":    "CoreSwitch01",
		"":                      "",
		"not a prompt at all":   "",
	}
	for prompt, want := range cases {
		if got := parseHuaweiPromptHostname(prompt); got != want {
			t.Errorf("parseHuaweiPromptHostname(%q) = %q, want %q", prompt, got, want)
		}
	}
}

func TestHuaweiInterfaceBriefSkipsLegendLines(t *testing.T) {
	names := parseHuaweiInterfaceBrief(huaweiInterfaceBriefFixture)
	want := []string{"GigabitEthernet0/0/1", "GigabitEthernet0/0/2", "MEth0/0/1"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %#v, want %#v (legend lines like \"*down: administratively down\" must not be mistaken for data rows)", names, want)
	}
}

const huaweiInterfaceDetailFixture = `GigabitEthernet0/0/1 current state : UP
Line protocol current state : UP
Description:uplink-to-core
Switch Port,Link Type : trunk, PVID : 1, TPID : 8100(Hex)
The Maximum Frame Length is 9216
Internet Address is 192.168.1.1/24
IP Sending Frames' Format is PKTFMT_ETHNT_2, Hardware address is 4c1f-cc00-0001-0001
Speed : 1000, Loopback : NONE
GigabitEthernet0/0/2 current state : Administratively DOWN
Line protocol current state : DOWN
Description:
Switch Port,Link Type : access, PVID : 1, TPID : 8100(Hex)
The Maximum Frame Length is 9216
IP Sending Frames' Format is PKTFMT_ETHNT_2, Hardware address is 4c1f-cc00-0001-0002
Speed : 1000, Loopback : NONE
`

func TestHuaweiGetInterfacesParsesUpAndAdministrativelyDownBlocks(t *testing.T) {
	interfaces := parseHuaweiInterfaces(huaweiInterfaceDetailFixture)

	want := map[string]InterfaceInfo{
		"GigabitEthernet0/0/1": {
			IsUp: true, IsEnabled: true,
			Description: "uplink-to-core",
			IPAddress:   "192.168.1.1/24",
			MACAddress:  "4c1f-cc00-0001-0001",
			MTU:         9216,
			SpeedMbps:   1000,
		},
		"GigabitEthernet0/0/2": {
			IsUp: false, IsEnabled: false,
			Description: "",
			MACAddress:  "4c1f-cc00-0001-0002",
			MTU:         9216,
			SpeedMbps:   1000,
		},
	}
	if !reflect.DeepEqual(interfaces, want) {
		t.Fatalf("interfaces = %#v, want %#v", interfaces, want)
	}
}

func TestHuaweiGetLLDPNeighborsEmptyOutputIsEmptyMap(t *testing.T) {
	result := parseHuaweiLLDPNeighbors("")
	if len(result) != 0 {
		t.Fatalf("expected empty map when the header never appears, got %#v", result)
	}
}

func TestHuaweiGetLLDPNeighborsDocumentedFormatNotLiveVerified(t *testing.T) {
	// Column layout confirmed live against a real Huawei S6700 (see
	// parseHuaweiLLDPNeighbors' doc comment); the specific interface/
	// hostname values here are synthetic, not the real device's own.
	fixture := "Local Intf   Neighbor Dev             Neighbor Intf             Exptime\n" +
		"GE0/0/1      CoreSwitch02             GE0/0/5                   100    \n" +
		"\n"
	result := parseHuaweiLLDPNeighbors(fixture)
	want := map[string][]LLDPNeighbor{
		"GE0/0/1": {{RemoteSystemName: "CoreSwitch02", RemotePortID: "GE0/0/5"}},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestHuaweiGetLLDPNeighborsSameLocalInterfaceAccumulatesMultipleNeighbors(t *testing.T) {
	// Confirmed live: a real port on the test S6700 had 6 distinct
	// neighbors (sub-interfaces sharing one physical link) -- this
	// fixture reproduces that shape (synthetic values) to guard the
	// append-not-overwrite behavior that live case depended on.
	fixture := "Local Intf   Neighbor Dev             Neighbor Intf             Exptime\n" +
		"XGE0/0/22    RemoteBox                sub-a                     77     \n" +
		"XGE0/0/22    RemoteBox                sub-b                     77     \n" +
		"\n"
	result := parseHuaweiLLDPNeighbors(fixture)
	if len(result["XGE0/0/22"]) != 2 {
		t.Fatalf("XGE0/0/22 neighbors = %#v, want 2 entries (one local interface can see more than one neighbor)", result["XGE0/0/22"])
	}
}

func TestHuaweiGetLLDPNeighborsTruncatedLongNamesPassThroughVerbatim(t *testing.T) {
	// Confirmed live: VRP's own fixed-width "brief" columns truncate long
	// device/port names with a literal "..." (a real neighbor came back
	// as "Jalpa-Laghubittya-EoM..."/"bridge520-IntrAnet/vpl...") -- this
	// is the most that command tells you, not a gearctl parsing bug, so
	// the truncated text should pass through exactly as sent, not be
	// mangled further.
	fixture := "Local Intf   Neighbor Dev             Neighbor Intf             Exptime\n" +
		"XGE0/0/24    Some-Really-Long-Name... some-really-long-port...  108    \n" +
		"\n"
	result := parseHuaweiLLDPNeighbors(fixture)
	want := map[string][]LLDPNeighbor{
		"XGE0/0/24": {{RemoteSystemName: "Some-Really-Long-Name...", RemotePortID: "some-really-long-port..."}},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}
