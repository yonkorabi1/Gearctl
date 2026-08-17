package drivers

import (
	"reflect"
	"testing"
	"time"

	"github.com/onu-platform/gearctl/internal/transport"
)

// sampleShowSystemInformation is real "show system information |
// display json" output captured live against a Juniper vMX (Junos
// 18.2R1.9) -- including host-name coming back as `[{}]` (present, but
// with no "data" key at all), which is this device's genuine state: no
// hostname configured, not a parsing artifact.
const sampleShowSystemInformation = `{
    "system-information" : [
    {
        "hardware-model" : [
        {
            "data" : "vmx"
        }
        ],
        "os-name" : [
        {
            "data" : "junos"
        }
        ],
        "os-version" : [
        {
            "data" : "18.2R1.9"
        }
        ],
        "serial-number" : [
        {
            "data" : "VM6A809BE3DC"
        }
        ],
        "host-name" : [
        {
        }
        ]
    }
    ]
}`

// sampleShowSystemUptime is real "show system uptime | display json"
// output captured live -- trimmed to the fields GetFacts actually reads
// (up-time), but every value shown is byte-for-byte what the device
// returned, not synthesized.
const sampleShowSystemUptime = `{
    "system-uptime-information" : [
    {
        "uptime-information" : [
        {
            "up-time" : [
            {
                "data" : "14:21",
                "attributes" : {"junos:seconds" : "51660"}
            }
            ]
        }
        ]
    }
    ]
}`

// sampleShowInterfacesTerse is real "show interfaces terse | display
// json" output captured live, trimmed to two physical interfaces:
// ge-0/0/0 (which has a logical unit .0 carrying the real inet address
// this lab session used to reach the device, 192.168.14.14/24 -- the
// SSH session itself was over this address) and ge-0/0/1 (confirmed
// live to have NO logical-interface entry at all -- an unconfigured
// physical interface with no unit created, not a parsing gap).
const sampleShowInterfacesTerse = `{
    "interface-information" : [
    {
        "attributes" : {"xmlns" : "http://xml.juniper.net/junos/18.2R1/junos-interface", "junos:style" : "terse"},
        "physical-interface" : [
        {
            "name" : [
            {
                "data" : "ge-0/0/0"
            }
            ],
            "admin-status" : [
            {
                "data" : "up"
            }
            ],
            "oper-status" : [
            {
                "data" : "up"
            }
            ],
            "logical-interface" : [
            {
                "name" : [
                {
                    "data" : "ge-0/0/0.0"
                }
                ],
                "admin-status" : [
                {
                    "data" : "up"
                }
                ],
                "oper-status" : [
                {
                    "data" : "up"
                }
                ],
                "address-family" : [
                {
                    "address-family-name" : [
                    {
                        "data" : "inet"
                    }
                    ],
                    "interface-address" : [
                    {
                        "ifa-local" : [
                        {
                            "data" : "192.168.14.14/24",
                            "attributes" : {"junos:emit" : "emit"}
                        }
                        ]
                    }
                    ]
                },
                {
                    "address-family-name" : [
                    {
                        "data" : "multiservice"
                    }
                    ]
                }
                ]
            }
            ]
        },
        {
            "name" : [
            {
                "data" : "ge-0/0/1"
            }
            ],
            "admin-status" : [
            {
                "data" : "up"
            }
            ],
            "oper-status" : [
            {
                "data" : "up"
            }
            ]
        }
        ]
    }
    ]
}`

// sampleShowInterfacesFull is real "show interfaces | display json"
// output captured live, trimmed to the fields GetInterfaces actually
// reads -- three physical interfaces:
//   - ge-0/0/0: up/up, mtu 1514, speed "1000mbps", a real MAC, no
//     description (the field is entirely ABSENT from the JSON, not
//     present-and-empty -- confirmed live, matching Cisco's own
//     "line simply isn't there" convention for the same fact).
//   - lo0: up/up, mtu "Unlimited" (a real non-numeric value Junos
//     returns for loopback, confirmed live) and no "speed" key at all --
//     both left at InterfaceInfo's zero value rather than erroring.
//   - ge-0/0/1: same shape as ge-0/0/0, but WITH a description --
//     confirmed live by deliberately setting one on this real interface
//     ("gearctl-test-desc"), capturing this exact JSON, then removing it
//     again (see README.md/juniperdriver.go for the live session this
//     was verified in).
const sampleShowInterfacesFull = `{
    "interface-information" : [
    {
        "attributes" : {"xmlns" : "http://xml.juniper.net/junos/18.2R1/junos-interface", "junos:style" : "normal"},
        "physical-interface" : [
        {
            "name" : [
            {
                "data" : "ge-0/0/0"
            }
            ],
            "admin-status" : [
            {
                "data" : "up",
                "attributes" : {"junos:format" : "Enabled"}
            }
            ],
            "oper-status" : [
            {
                "data" : "up"
            }
            ],
            "mtu" : [
            {
                "data" : "1514"
            }
            ],
            "speed" : [
            {
                "data" : "1000mbps"
            }
            ],
            "current-physical-address" : [
            {
                "data" : "50:00:00:0b:00:02"
            }
            ]
        },
        {
            "name" : [
            {
                "data" : "lo0"
            }
            ],
            "admin-status" : [
            {
                "data" : "up",
                "attributes" : {"junos:format" : "Enabled"}
            }
            ],
            "oper-status" : [
            {
                "data" : "up"
            }
            ],
            "mtu" : [
            {
                "data" : "Unlimited"
            }
            ]
        },
        {
            "name" : [
            {
                "data" : "ge-0/0/1"
            }
            ],
            "admin-status" : [
            {
                "data" : "up",
                "attributes" : {"junos:format" : "Enabled"}
            }
            ],
            "oper-status" : [
            {
                "data" : "up"
            }
            ],
            "description" : [
            {
                "data" : "gearctl-test-desc"
            }
            ],
            "mtu" : [
            {
                "data" : "1514"
            }
            ],
            "speed" : [
            {
                "data" : "1000mbps"
            }
            ],
            "current-physical-address" : [
            {
                "data" : "50:00:00:0b:00:03"
            }
            ]
        }
        ]
    }
    ]
}`

// sampleShowConfiguration is real "show configuration" (verbatim, NOT
// "| display json") output captured live -- Junos's hierarchical
// curly-brace format, including its own "## SECRET-DATA" markers next to
// encrypted secrets. The two password hashes below are placeholders --
// REDACTED-FOR-TEST-FIXTURE-* -- swapped in for the real device's actual
// hashes; everything else (structure, comments, indentation, semicolons)
// is byte-for-byte what the device returned. GetConfig performs no
// parsing at all (ConfigGetter's contract is verbatim passthrough), so
// the hash values themselves are not load-bearing for this test.
const sampleShowConfiguration = `## Last commit: 2026-08-16 07:17:32 UTC by rabi
version 18.2R1.9;
system {
    login {
        user rabi {
            uid 2000;
            class super-user;
            authentication {
                encrypted-password "REDACTED-FOR-TEST-FIXTURE-1"; ## SECRET-DATA
            }
        }
    }
    root-authentication {
        encrypted-password "REDACTED-FOR-TEST-FIXTURE-2"; ## SECRET-DATA
    }
    services {
        ssh {
            root-login allow;
        }
    }
}
`

func TestJuniperDriverVendor(t *testing.T) {
	if got := NewJuniperDriver().Vendor(); got != "juniper" {
		t.Errorf("Vendor() = %q, want \"juniper\"", got)
	}
}

func TestJuniperDriverGetFactsParsesSystemInformationUptimeAndInterfaces(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show system information | display json": {output: sampleShowSystemInformation},
		"show system uptime | display json":      {output: sampleShowSystemUptime},
		"show interfaces terse | display json":   {output: sampleShowInterfacesTerse},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	facts, err := d.GetFacts()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[string]interface{}{
		"vendor":         "Juniper",
		"model":          "vmx",
		"hostname":       "", // confirmed live: this device genuinely has none configured
		"fqdn":           "",
		"os_version":     "18.2R1.9",
		"serial_number":  "VM6A809BE3DC",
		"uptime":         "14:21",
		"interface_list": []string{"ge-0/0/0", "ge-0/0/0.0", "ge-0/0/1"},
	}
	for key, wantVal := range want {
		gotVal := facts[key]
		if !reflect.DeepEqual(gotVal, wantVal) {
			t.Errorf("facts[%q] = %#v, want %#v", key, gotVal, wantVal)
		}
	}
}

// TestJuniperDriverGetFactsDisablesPagingFirst mirrors
// TestCiscoIOSDriverGetFactsDisablesPagingFirst -- "set cli
// screen-length 0" is Junos's equivalent of "terminal length 0", sent
// proactively for the same reason (see promptdetector.go's Junos
// pagination fix, confirmed live in this same lab session, for why
// relying solely on the generic fallback is riskier).
func TestJuniperDriverGetFactsDisablesPagingFirst(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show system information | display json": {output: sampleShowSystemInformation},
		"show system uptime | display json":      {output: sampleShowSystemUptime},
		"show interfaces terse | display json":   {output: sampleShowInterfacesTerse},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, err := d.GetFacts(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.sent) < 1 || fake.sent[0] != "set cli screen-length 0" {
		t.Errorf("sent = %v, want \"set cli screen-length 0\" sent first", fake.sent)
	}
}

func TestJuniperDriverGetInterfacesCombinesTerseIPWithFullState(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show interfaces terse | display json": {output: sampleShowInterfacesTerse},
		"show interfaces | display json":       {output: sampleShowInterfacesFull},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	got, err := d.GetInterfaces()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[string]InterfaceInfo{
		"ge-0/0/0": {
			IsUp: true, IsEnabled: true,
			IPAddress: "192.168.14.14/24", // from terse, real live-captured CIDR
			MTU:       1514, SpeedMbps: 1000,
			MACAddress: "50:00:00:0b:00:02",
		},
		"lo0": {
			IsUp: true, IsEnabled: true,
			// MTU/SpeedMbps left at zero value: "Unlimited" doesn't parse
			// as a number (confirmed live), and there's no "speed" key
			// at all for lo0.
		},
		"ge-0/0/1": {
			IsUp: true, IsEnabled: true,
			Description: "gearctl-test-desc", // confirmed live, see sampleShowInterfacesFull's doc comment
			MTU:         1514, SpeedMbps: 1000,
			MACAddress: "50:00:00:0b:00:03",
			// No IPAddress: confirmed live that ge-0/0/1 has no
			// logical-interface entry in terse at all (unconfigured,
			// no unit created) -- map lookup miss correctly leaves "".
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetInterfaces() = %#v, want %#v", got, want)
	}
}

// sampleShowInterfacesFullTruncated is sampleShowInterfacesFull cut off
// partway through -- genuinely incomplete/invalid JSON.
var sampleShowInterfacesFullTruncated = sampleShowInterfacesFull[:len(sampleShowInterfacesFull)/2]

// TestJuniperDriverGetInterfacesRefusesTruncatedJSON is a regression
// test: unlike Cisco's free-text parsing (which degrades gracefully on
// a partial response), a Junos JSON response cut off mid-object either
// fails to parse outright or -- worse -- happens to still be
// syntactically valid while silently missing trailing interfaces, which
// would misleadingly look like "these interfaces don't exist". Using
// plain fakeShellSession here (which doesn't implement
// transport.CompletionAwareSession, so runJSONCommand's completion-aware
// wait is never even consulted -- see
// TestJuniperDriverRunJSONCommandUsesCompletionAwareSessionWhenAvailable
// for that path) confirms GetInterfaces still surfaces a hard error on a
// genuinely truncated response rather than silently returning a
// partial/wrong result.
func TestJuniperDriverGetInterfacesRefusesTruncatedJSON(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show interfaces terse | display json": {output: sampleShowInterfacesTerse},
		"show interfaces | display json":       {output: sampleShowInterfacesFullTruncated, truncated: true},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, err := d.GetInterfaces(); err == nil {
		t.Fatal("expected an error when the interfaces response was truncated")
	}
}

// completionAwareStubSession is a minimal ShellSession +
// transport.CompletionAwareSession double: it invokes the isComplete
// function it's given against the FIRST response, and if that reports
// "not complete", returns the SECOND response instead -- modeling
// exactly what promptdetector.go's real waitForPrompt does (keep
// extending the SAME read until isComplete is satisfied), just without
// needing real quiescence-timer timing in a unit test. Exists to prove
// JuniperDriver.runJSONCommand actually wires a working isComplete
// (json.Valid) through BaseDriver.RunCommandUntilComplete down to the
// session, not just that it compiles.
type completionAwareStubSession struct {
	firstAttempt, secondAttempt fakeResponse
	receivedIsComplete          func(string) bool
}

func (c *completionAwareStubSession) Open(transport.ConnectionParams) error { return nil }

func (c *completionAwareStubSession) SendAndWaitForPrompt(command string, timeout time.Duration) (string, bool, error) {
	return c.SendAndWaitForPromptUntilComplete(command, timeout, "", nil)
}

func (c *completionAwareStubSession) SendAndWaitForPromptHint(command string, timeout time.Duration, expectedPrompt string) (string, bool, error) {
	return c.SendAndWaitForPromptUntilComplete(command, timeout, expectedPrompt, nil)
}

func (c *completionAwareStubSession) SendAndWaitForPromptUntilComplete(
	_ string, _ time.Duration, _ string, isComplete func(string) bool,
) (string, bool, error) {
	c.receivedIsComplete = isComplete
	if isComplete != nil && !isComplete(c.firstAttempt.output) {
		return c.secondAttempt.output, c.secondAttempt.truncated, c.secondAttempt.err
	}
	return c.firstAttempt.output, c.firstAttempt.truncated, c.firstAttempt.err
}

func (c *completionAwareStubSession) CurrentPrompt() string { return "" }
func (c *completionAwareStubSession) Close() error          { return nil }

// TestJuniperDriverRunJSONCommandUsesCompletionAwareSessionWhenAvailable
// confirms runJSONCommand's isComplete (json.Valid) is real and correctly
// wired: given a session that models "first attempt was incomplete JSON,
// only becomes complete once isComplete says so" (see
// completionAwareStubSession's own doc comment -- this is the unit-test
// equivalent of promptdetector.go's real quiescence-extension behavior,
// see TestWaitForPromptIsCompleteDefersQuiescenceConclusion in the
// transport package for that live mechanism), GetInterfaces succeeds
// using the eventually-complete response.
func TestJuniperDriverRunJSONCommandUsesCompletionAwareSessionWhenAvailable(t *testing.T) {
	stub := &completionAwareStubSession{
		firstAttempt:  fakeResponse{output: sampleShowInterfacesFullTruncated}, // incomplete JSON
		secondAttempt: fakeResponse{output: sampleShowInterfacesFull},          // complete JSON, returned once isComplete rejects the first
	}
	d := &JuniperDriver{BaseDriver{session: stub, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	got, err := d.runJSONCommand("show interfaces | display json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != sampleShowInterfacesFull {
		t.Error("expected runJSONCommand to return the eventually-complete response, not the initially-incomplete one")
	}
	if stub.receivedIsComplete == nil {
		t.Fatal("expected a non-nil isComplete to have been passed through")
	}
	if !stub.receivedIsComplete(sampleShowInterfacesFull) {
		t.Error("isComplete(complete valid JSON) should be true")
	}
	if stub.receivedIsComplete(sampleShowInterfacesFullTruncated) {
		t.Error("isComplete(truncated JSON) should be false")
	}
}

func TestJuniperDriverGetConfigReturnsVerbatim(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show configuration": {output: sampleShowConfiguration},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	got, err := d.GetConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != sampleShowConfiguration {
		t.Errorf("GetConfig() = %q, want %q", got, sampleShowConfiguration)
	}
	if len(fake.sent) < 1 || fake.sent[0] != "set cli screen-length 0" {
		t.Errorf("sent = %v, want \"set cli screen-length 0\" sent first", fake.sent)
	}
}

func TestJuniperDriverGetConfigRefusesTruncatedOutput(t *testing.T) {
	fake := &fakeShellSession{responses: map[string]fakeResponse{
		"show configuration": {output: sampleShowConfiguration, truncated: true},
	}}
	d := &JuniperDriver{BaseDriver{session: fake, params: transport.ConnectionParams{PerCommandTimeoutMs: 1000}}}

	if _, err := d.GetConfig(); err == nil {
		t.Fatal("expected an error when the configuration response was truncated")
	}
}

func TestParseJunosSpeed(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"1000mbps", 1000}, // confirmed live, ge-0/0/0
		{"800mbps", 800},   // confirmed live, another interface on the same device
		{"Unlimited", 0},   // confirmed live, lo0 -- non-numeric, degrades to 0
		{"", 0},
		{"1.5gbps", 1500},
		{"100kbps", 0.1},
	}
	for _, tc := range cases {
		if got := parseJunosSpeed(tc.in); got != tc.want {
			t.Errorf("parseJunosSpeed(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestLeafStringHandlesAllThreeRealShapes(t *testing.T) {
	cases := []struct {
		name string
		in   junosLeaf
		want string
	}{
		{"absent (nil slice)", nil, ""},
		{"empty array", junosLeaf{}, ""},
		{"present but empty object -- confirmed live, host-name on this device", junosLeaf{{Data: ""}}, ""},
		{"present with data", junosLeaf{{Data: "vmx"}}, "vmx"},
	}
	for _, tc := range cases {
		if got := leafString(tc.in); got != tc.want {
			t.Errorf("%s: leafString(%#v) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
