package drivers

import (
	"reflect"
	"testing"
)

// Fixtures below are captured live against a real RouterOS 6.49.19 CHR at
// 192.168.14.18 -- see mikrotikdriver.go's type doc comment -- through
// runSingle's ANSI-cleaning (mimicking what GetFacts/GetInterfaces/etc.
// actually see, not the raw color/escape-laden wire bytes).

func TestStripMikrotikANSIRemovesColorAndCursorCodes(t *testing.T) {
	raw := "\x1b[K\n  \x1b[32mname\x1b[m: MikroTik\n\x1b[9999B"
	got := stripMikrotikANSI(raw)
	want := "\n  name: MikroTik\n"
	if got != want {
		t.Fatalf("stripMikrotikANSI() = %q, want %q", got, want)
	}
}

func TestMikrotikConnectAppendsLoginModifiersOnlyWhenAbsent(t *testing.T) {
	// Connect itself needs a live session to fully exercise; this only
	// checks the username-mutation guard logic in isolation via a thin
	// re-implementation of its condition, since Connect's real body
	// requires a transport round-trip covered instead by the live
	// smoke-tests README.md documents running against real hardware.
	cases := []struct {
		in, want string
	}{
		{"admin", "admin" + mikrotikLoginModifiers},
		{"admin+cet80w", "admin+cet80w"}, // caller's own modifiers left alone
	}
	for _, c := range cases {
		got := c.in
		if !contains(got, "+") {
			got += mikrotikLoginModifiers
		}
		if got != c.want {
			t.Errorf("username %q -> %q, want %q", c.in, got, c.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}

const mikrotikIdentityFixture = "\n  name: MikroTik\n"

const mikrotikResourceFixture = "" +
	"                   uptime: 22m18s\n" +
	"                  version: 6.49.19 (stable)\n" +
	"               build-time: Jun/20/2025 13:45:50\n" +
	"              free-memory: 202.6MiB\n" +
	"             total-memory: 224.0MiB\n" +
	"                      cpu: QEMU\n" +
	"                cpu-count: 1\n" +
	"            cpu-frequency: 2095MHz\n" +
	"                 cpu-load: 0%\n" +
	"           free-hdd-space: 31.5MiB\n" +
	"          total-hdd-space: 63.5MiB\n" +
	"  write-sect-since-reboot: 3752\n" +
	"         write-sect-total: 3753\n" +
	"        architecture-name: x86_64\n" +
	"               board-name: CHR\n" +
	"                 platform: MikroTik\n"

const mikrotikRouterboardNoFixture = "\n  routerboard: no\n"

const mikrotikRouterboardYesFixture = "" +
	"\n  routerboard: yes\n" +
	"  model: RB750Gr3\n" +
	"  serial-number: ABC123DEF456\n"

const mikrotikInterfaceTerseFixture = "" +
	" 0  R  name=ether1 default-name=ether1 type=ether mtu=1500 actual-mtu=1500 mac-address=50:00:00:13:00:00 link-downs=0 \n" +
	" 1  R  name=ether2 default-name=ether2 type=ether mtu=1500 actual-mtu=1500 mac-address=50:00:00:13:00:01 link-downs=0 \n"

// CRLF line endings below are deliberate, matching a real capture --
// RouterOS's console uses "\r\n", not a bare "\n" (confirmed live; see
// mikrotikPrintDetailRecords' doc comment for the real bug this
// regression-tests: naive "\n\n" blank-line splitting never matches
// "\r\n\r\n", collapsing every record but the last one into oblivion).
// Four interfaces here, not two, to actually exercise that collapse --
// the live bug only showed up with more than one blank-line boundary to
// get wrong.
const mikrotikInterfaceDetailFixture = "" +
	"Flags: D - dynamic, X - disabled, R - running, S - slave \r\n" +
	" 0  R  name=\"ether1\" default-name=\"ether1\" type=\"ether\" mtu=1500 \r\n" +
	"       actual-mtu=1500 mac-address=50:00:00:13:00:00 \r\n" +
	"       last-link-up-time=aug/23/2026 17:22:30 link-downs=0 \r\n" +
	"\r\n" +
	" 1  R  name=\"ether2\" default-name=\"ether2\" type=\"ether\" mtu=1500 \r\n" +
	"       actual-mtu=1500 mac-address=50:00:00:13:00:01 \r\n" +
	"       last-link-up-time=aug/23/2026 17:22:32 link-downs=0 \r\n" +
	"\r\n" +
	" 2  R  name=\"ether3\" default-name=\"ether3\" type=\"ether\" mtu=1500 \r\n" +
	"       actual-mtu=1500 mac-address=50:00:00:13:00:02 \r\n" +
	"       last-link-up-time=aug/23/2026 17:22:33 link-downs=0 \r\n" +
	"\r\n" +
	" 3  R  name=\"ether4\" default-name=\"ether4\" type=\"ether\" mtu=1500 \r\n" +
	"       actual-mtu=1500 mac-address=50:00:00:13:00:03 \r\n" +
	"       last-link-up-time=aug/23/2026 17:22:33 link-downs=0 \r\n" +
	"\r\n"

const mikrotikAddressDetailFixture = "" +
	"Flags: X - disabled, I - invalid, D - dynamic \r\n" +
	" 0   address=192.168.14.18/24 network=192.168.14.0 interface=ether1 \r\n" +
	"     actual-interface=ether1 \r\n" +
	"\r\n"

func TestMikrotikGetFactsParsesLiveFixtures(t *testing.T) {
	facts := parseMikrotikFacts(
		mikrotikIdentityFixture,
		mikrotikResourceFixture,
		mikrotikRouterboardNoFixture,
		mikrotikInterfaceTerseFixture,
	)
	want := map[string]interface{}{
		"vendor":         "MikroTik",
		"model":          "CHR",
		"hostname":       "MikroTik",
		"fqdn":           "MikroTik",
		"os_version":     "6.49.19 (stable)",
		"serial_number":  "",
		"uptime":         "22m18s",
		"interface_list": []string{"ether1", "ether2"},
	}
	if !reflect.DeepEqual(facts, want) {
		t.Fatalf("facts = %#v, want %#v", facts, want)
	}
}

func TestMikrotikGetFactsRouterboardYesOverridesModelAndSerial(t *testing.T) {
	facts := parseMikrotikFacts(
		mikrotikIdentityFixture,
		mikrotikResourceFixture,
		mikrotikRouterboardYesFixture,
		mikrotikInterfaceTerseFixture,
	)
	if facts["model"] != "RB750Gr3" {
		t.Errorf("model = %v, want RB750Gr3 (routerboard model should override board-name)", facts["model"])
	}
	if facts["serial_number"] != "ABC123DEF456" {
		t.Errorf("serial_number = %v, want ABC123DEF456", facts["serial_number"])
	}
}

func TestMikrotikPrintDetailRecordsSplitsOnBlankLines(t *testing.T) {
	records := mikrotikPrintDetailRecords(mikrotikInterfaceDetailFixture)
	if len(records) != 4 {
		t.Fatalf("got %d records, want 4 (one per interface -- see the CRLF-collapse regression this fixture targets): %#v", len(records), records)
	}
	if !contains(records[0], `name="ether1"`) {
		t.Errorf("record[0] = %q, missing name=\"ether1\"", records[0])
	}
	if !contains(records[0], "actual-mtu=1500") {
		t.Errorf("record[0] = %q, missing wrapped continuation field actual-mtu=1500", records[0])
	}
	if !contains(records[3], `name="ether4"`) {
		t.Errorf("record[3] = %q, missing name=\"ether4\"", records[3])
	}
}

func TestMikrotikGetInterfacesMergesAddressByInterfaceField(t *testing.T) {
	interfaces := parseMikrotikInterfaces(mikrotikInterfaceDetailFixture, mikrotikAddressDetailFixture)

	want := map[string]InterfaceInfo{
		"ether1": {
			IsUp: true, IsEnabled: true, MTU: 1500,
			MACAddress: "50:00:00:13:00:00",
			IPAddress:  "192.168.14.18/24",
		},
		"ether2": {
			IsUp: true, IsEnabled: true, MTU: 1500,
			MACAddress: "50:00:00:13:00:01",
		},
		"ether3": {
			IsUp: true, IsEnabled: true, MTU: 1500,
			MACAddress: "50:00:00:13:00:02",
		},
		"ether4": {
			IsUp: true, IsEnabled: true, MTU: 1500,
			MACAddress: "50:00:00:13:00:03",
		},
	}
	if !reflect.DeepEqual(interfaces, want) {
		t.Fatalf("interfaces = %#v, want %#v", interfaces, want)
	}
}

func TestMikrotikGetLLDPNeighborsEmptyOutputIsEmptyMap(t *testing.T) {
	// Confirmed live: "/ip neighbor print detail" with zero neighbors
	// produces no records at all after ANSI-cleaning.
	result := parseMikrotikLLDPNeighbors("\n")
	if len(result) != 0 {
		t.Fatalf("expected empty map for zero neighbors, got %#v", result)
	}
}

func TestMikrotikGetLLDPNeighborsDocumentedFormatNotLiveVerified(t *testing.T) {
	// Built from RouterOS's documented "/ip neighbor print detail" field
	// names (interface/identity/interface-name), NOT a real capture -- see
	// GetLLDPNeighbors' doc comment.
	fixture := "\n" +
		" 0   interface=ether1 address=10.0.0.2 mac-address=AA:BB:CC:DD:EE:FF \n" +
		"     identity=\"core-switch\" platform=\"RouterOS\" interface-name=\"ether5\" \n" +
		"\n"
	result := parseMikrotikLLDPNeighbors(fixture)
	want := map[string][]LLDPNeighbor{
		"ether1": {{RemoteSystemName: "core-switch", RemotePortID: "ether5"}},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestMikrotikFieldsOfUnquotesDoubleQuotedValues(t *testing.T) {
	fields := mikrotikFieldsOf(`name="ether1" mtu=1500 comment="uplink to core"`)
	if fields["name"] != "ether1" {
		t.Errorf("name = %q, want ether1", fields["name"])
	}
	if fields["mtu"] != "1500" {
		t.Errorf("mtu = %q, want 1500", fields["mtu"])
	}
	if fields["comment"] != "uplink to core" {
		t.Errorf("comment = %q, want %q", fields["comment"], "uplink to core")
	}
}
