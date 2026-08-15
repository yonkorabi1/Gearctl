package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/onu-platform/gearctl/internal/drivers"
	"github.com/onu-platform/gearctl/internal/olterr"
	"github.com/onu-platform/gearctl/internal/transport"
)

// fakeDriver is a no-network drivers.CliDriver double, value-receiver so
// fakeDriver{} itself (no pointer needed) satisfies the interface.
type fakeDriver struct {
	connectErr error
	runResults []transport.CommandResult
	runErr     error
}

func (d fakeDriver) Vendor() string                           { return "fake" }
func (d fakeDriver) Connect(transport.ConnectionParams) error { return d.connectErr }
func (d fakeDriver) RunCommands([]string) ([]transport.CommandResult, error) {
	return d.runResults, d.runErr
}
func (d fakeDriver) Disconnect() error { return nil }

// fakeDriverWithFacts additionally satisfies drivers.FactsProvider --
// embeds fakeDriver by value so its Connect/RunCommands/Disconnect
// promote unchanged.
type fakeDriverWithFacts struct {
	fakeDriver
	facts    map[string]interface{}
	factsErr error
}

func (d fakeDriverWithFacts) GetFacts() (map[string]interface{}, error) { return d.facts, d.factsErr }

// fakeDriverWithConfig additionally satisfies drivers.ConfigGetter --
// embeds fakeDriver by value so its Connect/RunCommands/Disconnect
// promote unchanged.
type fakeDriverWithConfig struct {
	fakeDriver
	config    string
	configErr error
}

func (d fakeDriverWithConfig) GetConfig() (string, error) { return d.config, d.configErr }

// fakeDriverWithFactsAndConfig satisfies BOTH drivers.FactsProvider and
// drivers.ConfigGetter -- used only for combined-mode (runBatchModes)
// tests. Deliberately its own type (embedding fakeDriver directly, once)
// rather than trying to embed fakeDriverWithFacts AND fakeDriverWithConfig
// together, which would make Connect/RunCommands/Disconnect ambiguous
// (both of those already embed fakeDriver separately, at the same depth).
type fakeDriverWithFactsAndConfig struct {
	fakeDriver
	facts     map[string]interface{}
	factsErr  error
	config    string
	configErr error
}

func (d fakeDriverWithFactsAndConfig) GetFacts() (map[string]interface{}, error) {
	return d.facts, d.factsErr
}
func (d fakeDriverWithFactsAndConfig) GetConfig() (string, error) { return d.config, d.configErr }

// fakeDriverWithInterfaces additionally satisfies drivers.InterfaceProvider
// -- embeds fakeDriver by value so its Connect/RunCommands/Disconnect
// promote unchanged.
type fakeDriverWithInterfaces struct {
	fakeDriver
	interfaces    map[string]drivers.InterfaceInfo
	interfacesErr error
}

func (d fakeDriverWithInterfaces) GetInterfaces() (map[string]drivers.InterfaceInfo, error) {
	return d.interfaces, d.interfacesErr
}

// fakeDriverWithLLDPNeighbors additionally satisfies
// drivers.LLDPNeighborsProvider -- embeds fakeDriver by value so its
// Connect/RunCommands/Disconnect promote unchanged.
type fakeDriverWithLLDPNeighbors struct {
	fakeDriver
	neighbors    map[string][]drivers.LLDPNeighbor
	neighborsErr error
}

func (d fakeDriverWithLLDPNeighbors) GetLLDPNeighbors() (map[string][]drivers.LLDPNeighbor, error) {
	return d.neighbors, d.neighborsErr
}

// fakeDriverWithConfigLoader additionally satisfies drivers.ConfigLoader
// -- embeds fakeDriver by value so its Connect/RunCommands/Disconnect
// promote unchanged. applyCalls (a pointer, so it's visible across the
// value receiver's copies) counts how many times LoadConfig was called
// with dryRun=false, so tests can assert apply was never reached (e.g.
// when a confirmation prompt is declined). reverseCmds is only ever
// returned by LoadMergeCommands (LoadConfig has no equivalent -- see
// drivers.ConfigLoader's doc comment).
type fakeDriverWithConfigLoader struct {
	fakeDriver
	diff        string
	configHash  string
	applied     bool
	loadErr     error
	applyCalls  *int
	reverseCmds []string
}

func (d fakeDriverWithConfigLoader) LoadConfig(candidate string, dryRun bool, expectedHash string) (string, string, bool, error) {
	diff, _, hash, applied, err := d.loadMerge(dryRun)
	return diff, hash, applied, err
}

func (d fakeDriverWithConfigLoader) LoadMergeCommands(commands []string, dryRun bool, expectedHash string) (string, []string, string, bool, error) {
	return d.loadMerge(dryRun)
}

func (d fakeDriverWithConfigLoader) loadMerge(dryRun bool) (string, []string, string, bool, error) {
	if !dryRun && d.applyCalls != nil {
		*d.applyCalls++
	}
	if d.loadErr != nil {
		return d.diff, d.reverseCmds, d.configHash, false, d.loadErr
	}
	if dryRun {
		return d.diff, d.reverseCmds, d.configHash, false, nil
	}
	return d.diff, d.reverseCmds, d.configHash, d.applied, nil
}

func noEnv(string) string { return "" }

func noReadFile(string) ([]byte, error) {
	return nil, errors.New("no file configured for this test")
}

func fakeReadFile(content string) func(string) ([]byte, error) {
	return func(string) ([]byte, error) { return []byte(content), nil }
}

func resolverFor(d drivers.CliDriver) resolveFunc {
	return func(string) drivers.CliDriver { return d }
}

func TestRunHelpFlagPrintsUsageAndExitsZero(t *testing.T) {
	for _, helpFlag := range []string{"-h", "-help", "--help"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{helpFlag}, &stdout, &stderr, strings.NewReader(""), resolverFor(fakeDriver{}), noEnv, noReadFile)
		if code != 0 {
			t.Errorf("run(%q): code = %d, want 0 (asking for help isn't a usage error)", helpFlag, code)
		}
		if !strings.Contains(stdout.String(), "-cmd value") || !strings.Contains(stdout.String(), "-serve") {
			t.Errorf("run(%q): stdout = %q, want it to list every flag", helpFlag, stdout.String())
		}
		if !strings.Contains(stdout.String(), "gearctl -- vendor-agnostic") {
			t.Errorf("run(%q): stdout = %q, want the custom usage header", helpFlag, stdout.String())
		}
	}
}

func TestRunRequiresHost(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-username", "u", "-password", "p", "-cmd", "show version"}, &stdout, &stderr, strings.NewReader(""), resolverFor(fakeDriver{}), noEnv, noReadFile)
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "-host is required") {
		t.Errorf("stderr = %q, want it to mention -host is required", stderr.String())
	}
}

func TestRunRequiresPasswordUnlessEnvFallback(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-host", "h", "-username", "u", "-cmd", "show version"}, &stdout, &stderr, strings.NewReader(""), resolverFor(fakeDriver{}), noEnv, noReadFile)
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "-password is required") {
		t.Errorf("stderr = %q, want it to mention -password is required", stderr.String())
	}
}

func TestRunPasswordFallsBackToEnv(t *testing.T) {
	env := map[string]string{"OLT_CLI_PASSWORD": "from-env"}
	getenv := func(key string) string { return env[key] }
	fake := fakeDriver{runResults: []transport.CommandResult{{Command: "show version", Output: strPtr("v1")}}}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-host", "h", "-username", "u", "-cmd", "show version"}, &stdout, &stderr, strings.NewReader(""), resolverFor(fake), getenv, noReadFile)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
}

func TestRunRejectsBadProtocol(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-host", "h", "-username", "u", "-password", "p", "-protocol", "ftp", "-cmd", "x"}, &stdout, &stderr, strings.NewReader(""), resolverFor(fakeDriver{}), noEnv, noReadFile)
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `-protocol must be "ssh" or "telnet"`) {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunRequiresCmdOrFacts(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-host", "h", "-username", "u", "-password", "p"}, &stdout, &stderr, strings.NewReader(""), resolverFor(fakeDriver{}), noEnv, noReadFile)
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "provide at least one -cmd, or pass -facts") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunServeDispatchReachesConfigLoad(t *testing.T) {
	// runServe calls config.Load(), which requires GEARCTL_SHARED_SECRET
	// from the real process environment (not the injectable getenv --
	// that's CLI-mode only). Asserting on a config error (rather than
	// letting a configured run reach http.ListenAndServe, which would
	// block) proves -serve dispatches into runServe without needing a real
	// listener.
	t.Setenv("GEARCTL_SHARED_SECRET", "")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-serve"}, &stdout, &stderr, strings.NewReader(""), resolverFor(fakeDriver{}), noEnv, noReadFile)
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "config error") {
		t.Errorf("stderr = %q, want it to mention a config error", stderr.String())
	}
}

func TestRunCLIConnectFailure(t *testing.T) {
	fake := fakeDriver{connectErr: olterr.New(olterr.TypeConnection, "boom")}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, []string{"show version"}, false, false, false, false, "", nil, false, false, false)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Errorf("stderr = %q, want it to mention the connect error", stderr.String())
	}
}

func TestRunCLIRunsCommandsHumanReadable(t *testing.T) {
	fake := fakeDriver{runResults: []transport.CommandResult{
		{Command: "show version", Output: strPtr("v1.0")},
	}}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, []string{"show version"}, false, false, false, false, "", nil, false, false, false)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "show version") || !strings.Contains(stdout.String(), "v1.0") {
		t.Errorf("stdout = %q, want it to contain the command and its output", stdout.String())
	}
}

func TestRunCLIRunsCommandsJSON(t *testing.T) {
	fake := fakeDriver{runResults: []transport.CommandResult{
		{Command: "show version", Output: strPtr("v1.0")},
	}}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, []string{"show version"}, false, false, false, false, "", nil, false, false, true)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var decoded cliResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	if decoded.Status != "success" || len(decoded.Results) != 1 || *decoded.Results[0].Output != "v1.0" {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestRunCLIAllCommandsFailedReturnsExitCode1(t *testing.T) {
	fake := fakeDriver{runResults: []transport.CommandResult{
		{Command: "bad", Error: "timeout"},
	}}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, []string{"bad"}, false, false, false, false, "", nil, false, false, false)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}

func TestRunCLIFactsUnsupportedVendor(t *testing.T) {
	fake := fakeDriver{} // does not implement drivers.FactsProvider
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, nil, true, false, false, false, "", nil, false, false, false)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no facts-capable driver") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// TestRunCLIFactsUnsupportedVendorFailsBeforeConnecting pins the fail-fast
// ordering: -facts on a vendor without a facts-capable driver must be
// caught before Connect is even attempted -- same ordering as
// httpapi.FactsHandler -- so it never wastes a dial (or a dial timeout) on
// a device that could never produce facts. connectErr is set to prove
// Connect was never called: if runCLI called it, this would surface as a
// different error than "no facts-capable driver".
func TestRunCLIFactsUnsupportedVendorFailsBeforeConnecting(t *testing.T) {
	fake := fakeDriver{connectErr: olterr.New(olterr.TypeConnection, "Connect should not have been called")}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, nil, true, false, false, false, "", nil, false, false, false)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no facts-capable driver") {
		t.Errorf("stderr = %q, want the unsupported-vendor message, not a connection error", stderr.String())
	}
}

func TestRunCLIFactsSupportedVendor(t *testing.T) {
	fake := fakeDriverWithFacts{facts: map[string]interface{}{"hostname": "switch1", "os_version": "15.2"}}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil, true, false, false, false, "", nil, false, false, false)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "switch1") || !strings.Contains(stdout.String(), "15.2") {
		t.Errorf("stdout = %q, want it to contain the parsed facts", stdout.String())
	}
}

func TestRunCLIFactsSupportedVendorJSON(t *testing.T) {
	fake := fakeDriverWithFacts{facts: map[string]interface{}{"hostname": "switch1"}}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil, true, false, false, false, "", nil, false, false, true)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var decoded cliResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	if decoded.Status != "success" || decoded.Facts["hostname"] != "switch1" {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestRunCLIInterfacesUnsupportedVendor(t *testing.T) {
	fake := fakeDriver{} // does not implement drivers.InterfaceProvider
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, nil, false, true, false, false, "", nil, false, false, false)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no interfaces-capable driver") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// TestRunCLIInterfacesUnsupportedVendorFailsBeforeConnecting mirrors
// TestRunCLIFactsUnsupportedVendorFailsBeforeConnecting: -interfaces on a
// vendor without an interfaces-capable driver must be caught before
// Connect is even attempted, same ordering as httpapi.InterfacesHandler.
func TestRunCLIInterfacesUnsupportedVendorFailsBeforeConnecting(t *testing.T) {
	fake := fakeDriver{connectErr: olterr.New(olterr.TypeConnection, "Connect should not have been called")}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, nil, false, true, false, false, "", nil, false, false, false)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no interfaces-capable driver") {
		t.Errorf("stderr = %q, want the unsupported-vendor message, not a connection error", stderr.String())
	}
}

func TestRunCLIInterfacesSupportedVendor(t *testing.T) {
	fake := fakeDriverWithInterfaces{interfaces: map[string]drivers.InterfaceInfo{
		"GigabitEthernet1": {IsUp: true, IsEnabled: true, MTU: 1500, SpeedMbps: 1000, MACAddress: "5000.0001.0000"},
	}}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil, false, true, false, false, "", nil, false, false, false)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "GigabitEthernet1") || !strings.Contains(stdout.String(), "up") {
		t.Errorf("stdout = %q, want it to contain the interface name and status", stdout.String())
	}
}

func TestRunCLIInterfacesSupportedVendorJSON(t *testing.T) {
	fake := fakeDriverWithInterfaces{interfaces: map[string]drivers.InterfaceInfo{
		"GigabitEthernet1": {IsUp: true, IsEnabled: true, Description: "uplink", MTU: 1500, SpeedMbps: 1000, MACAddress: "5000.0001.0000"},
	}}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil, false, true, false, false, "", nil, false, false, true)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var decoded cliResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	gi1, ok := decoded.Interfaces["GigabitEthernet1"]
	if decoded.Status != "success" || !ok || gi1.Description != "uplink" {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestRunCLILLDPNeighborsUnsupportedVendor(t *testing.T) {
	fake := fakeDriver{} // does not implement drivers.LLDPNeighborsProvider
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, nil, false, false, true, false, "", nil, false, false, false)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no LLDP-capable driver") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// TestRunCLILLDPNeighborsUnsupportedVendorFailsBeforeConnecting mirrors
// TestRunCLIFactsUnsupportedVendorFailsBeforeConnecting: -lldp-neighbors
// on a vendor without an LLDP-capable driver must be caught before
// Connect is even attempted, same ordering as httpapi.LLDPNeighborsHandler.
func TestRunCLILLDPNeighborsUnsupportedVendorFailsBeforeConnecting(t *testing.T) {
	fake := fakeDriver{connectErr: olterr.New(olterr.TypeConnection, "Connect should not have been called")}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, nil, false, false, true, false, "", nil, false, false, false)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no LLDP-capable driver") {
		t.Errorf("stderr = %q, want the unsupported-vendor message, not a connection error", stderr.String())
	}
}

func TestRunCLILLDPNeighborsSupportedVendor(t *testing.T) {
	fake := fakeDriverWithLLDPNeighbors{neighbors: map[string][]drivers.LLDPNeighbor{
		"Gi1": {{RemoteSystemName: "switch1.example.com", RemotePortID: "Gi0/1"}},
	}}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil, false, false, true, false, "", nil, false, false, false)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Gi1") || !strings.Contains(stdout.String(), "switch1.example.com") {
		t.Errorf("stdout = %q, want it to contain the local interface and remote system name", stdout.String())
	}
}

func TestRunCLILLDPNeighborsNoneSeen(t *testing.T) {
	fake := fakeDriverWithLLDPNeighbors{neighbors: map[string][]drivers.LLDPNeighbor{}}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil, false, false, true, false, "", nil, false, false, false)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No LLDP neighbors seen") {
		t.Errorf("stdout = %q, want a clear \"none seen\" message", stdout.String())
	}
}

func TestRunCLILLDPNeighborsSupportedVendorJSON(t *testing.T) {
	fake := fakeDriverWithLLDPNeighbors{neighbors: map[string][]drivers.LLDPNeighbor{
		"Gi1": {{RemoteSystemName: "switch1.example.com", RemotePortID: "Gi0/1"}},
	}}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil, false, false, true, false, "", nil, false, false, true)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var decoded cliResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	gi1, ok := decoded.Neighbors["Gi1"]
	if decoded.Status != "success" || !ok || len(gi1) != 1 || gi1[0].RemoteSystemName != "switch1.example.com" {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestRunCLIConfigUnsupportedVendor(t *testing.T) {
	fake := fakeDriver{} // does not implement drivers.ConfigGetter
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, nil, false, false, false, true, "", nil, false, false, false)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no config-capable driver") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// TestRunCLIConfigUnsupportedVendorFailsBeforeConnecting mirrors
// TestRunCLIFactsUnsupportedVendorFailsBeforeConnecting: -config on a
// vendor without a config-capable driver must be caught before Connect is
// even attempted, same ordering as httpapi.ConfigHandler.
func TestRunCLIConfigUnsupportedVendorFailsBeforeConnecting(t *testing.T) {
	fake := fakeDriver{connectErr: olterr.New(olterr.TypeConnection, "Connect should not have been called")}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, nil, false, false, false, true, "", nil, false, false, false)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no config-capable driver") {
		t.Errorf("stderr = %q, want the unsupported-vendor message, not a connection error", stderr.String())
	}
}

func TestRunCLIConfigSupportedVendor(t *testing.T) {
	fake := fakeDriverWithConfig{config: "hostname ISR\ninterface GigabitEthernet1\n"}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil, false, false, false, true, "", nil, false, false, false)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "hostname ISR") {
		t.Errorf("stdout = %q, want it to contain the raw config", stdout.String())
	}
}

func TestRunCLIConfigSupportedVendorJSON(t *testing.T) {
	fake := fakeDriverWithConfig{config: "hostname ISR\n"}
	var stdout, stderr bytes.Buffer
	code := runCLI(&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil, false, false, false, true, "", nil, false, false, true)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var decoded cliResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	if decoded.Status != "success" || decoded.Config == nil || *decoded.Config != "hostname ISR\n" {
		t.Errorf("decoded = %+v", decoded)
	}
}

// TestRunCombinesFactsAndConfigInOneConnection is the regression test for
// the NAPALM/Netmiko benchmark finding: combining two batchable modes
// used to be a hard error; now they run together over ONE connection
// (see runBatchModes) instead of requiring two separate gearctl
// invocations that would each pay their own connect cost.
func TestRunCombinesFactsAndConfigInOneConnection(t *testing.T) {
	fake := fakeDriverWithFactsAndConfig{
		facts:  map[string]interface{}{"hostname": "switch1"},
		config: "hostname switch1\n",
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"-host", "h", "-username", "u", "-password", "p", "-facts", "-config"}, &stdout, &stderr, strings.NewReader(""), resolverFor(fake), noEnv, noReadFile)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "=== facts ===") || !strings.Contains(stdout.String(), "switch1") {
		t.Errorf("stdout = %q, want a facts section containing the hostname", stdout.String())
	}
	if !strings.Contains(stdout.String(), "=== config ===") || !strings.Contains(stdout.String(), "hostname switch1") {
		t.Errorf("stdout = %q, want a config section containing the raw config", stdout.String())
	}
}

func TestRunRejectsCombiningLLDPNeighborsAndConfigStillRejectsUnrelatedFlagErrors(t *testing.T) {
	// -lldp-neighbors + -config is now a VALID combination (batchable) --
	// this just pins that combining them no longer errors, using a fake
	// that supports neither (RawDriver-equivalent), so it fails immediately
	// with "unsupported" per mode rather than a usage error.
	var stdout, stderr bytes.Buffer
	code := run([]string{"-host", "h", "-username", "u", "-password", "p", "-lldp-neighbors", "-config"}, &stdout, &stderr, strings.NewReader(""), resolverFor(fakeDriver{}), noEnv, noReadFile)
	if code != 1 {
		t.Errorf("code = %d, want 1 (both modes unsupported by the fake driver, not a usage error)", code)
	}
	if strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr = %q, want no mutual-exclusivity error -- these two modes are batchable now", stderr.String())
	}
}

func TestRunCombinesCmdAndConfigInOneConnection(t *testing.T) {
	fake := fakeDriverWithConfig{
		fakeDriver: fakeDriver{runResults: []transport.CommandResult{{Command: "show version", Output: strPtr("v1.0")}}},
		config:     "hostname R1\n",
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"-host", "h", "-username", "u", "-password", "p", "-cmd", "show version", "-config"}, &stdout, &stderr, strings.NewReader(""), resolverFor(fake), noEnv, noReadFile)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "=== cmd ===") || !strings.Contains(stdout.String(), "v1.0") {
		t.Errorf("stdout = %q, want a cmd section containing the command output", stdout.String())
	}
	if !strings.Contains(stdout.String(), "=== config ===") || !strings.Contains(stdout.String(), "hostname R1") {
		t.Errorf("stdout = %q, want a config section containing the raw config", stdout.String())
	}
}

// TestRunCombinedModesJSONPopulatesBothFieldsInOneObject confirms
// combined-mode JSON output reuses cliResult directly -- both Facts and
// Config populated in the SAME object, not two separate JSON blobs.
func TestRunCombinedModesJSONPopulatesBothFieldsInOneObject(t *testing.T) {
	fake := fakeDriverWithFactsAndConfig{
		facts:  map[string]interface{}{"hostname": "switch1"},
		config: "hostname switch1\n",
	}
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"-host", "h", "-username", "u", "-password", "p", "-facts", "-config", "-json"},
		&stdout, &stderr, strings.NewReader(""), resolverFor(fake), noEnv, noReadFile,
	)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var decoded cliResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	if decoded.Status != "success" {
		t.Errorf("decoded.Status = %q, want \"success\"", decoded.Status)
	}
	if decoded.Facts["hostname"] != "switch1" {
		t.Errorf("decoded.Facts = %+v, want hostname=switch1", decoded.Facts)
	}
	if decoded.Config == nil || *decoded.Config != "hostname switch1\n" {
		t.Errorf("decoded.Config = %v, want \"hostname switch1\\n\"", decoded.Config)
	}
}

// TestRunCombinedModesPartialFailureDoesNotBlockOtherModes confirms the
// core batch-specific behavior: a vendor lacking the capability ONE
// requested mode needs doesn't stop any OTHER requested mode (that the
// vendor DOES support) from running -- mirrors
// TestBatchHandlerReportsUnsupportedPerOperationNotWholeBatch on the
// HTTP side. fakeDriverWithFacts doesn't implement ConfigGetter, so
// -config is unsupported here while -facts still succeeds.
func TestRunCombinedModesPartialFailureDoesNotBlockOtherModes(t *testing.T) {
	fake := fakeDriverWithFacts{facts: map[string]interface{}{"hostname": "switch1"}}
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"-host", "h", "-username", "u", "-password", "p", "-facts", "-config", "-json"},
		&stdout, &stderr, strings.NewReader(""), resolverFor(fake), noEnv, noReadFile,
	)
	if code != 1 {
		t.Errorf("code = %d, want 1 (partial: config unsupported, facts succeeded)", code)
	}
	var decoded cliResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	if decoded.Status != "partial" {
		t.Errorf("decoded.Status = %q, want \"partial\"", decoded.Status)
	}
	if decoded.Facts["hostname"] != "switch1" {
		t.Errorf("decoded.Facts = %+v, want hostname=switch1 (facts must still succeed)", decoded.Facts)
	}
	if decoded.Config != nil {
		t.Errorf("decoded.Config = %v, want nil (unsupported)", decoded.Config)
	}
	// CLI-layer "no X-capable driver" errors use TypeValidation, same
	// convention the single-mode paths already use (contrast the HTTP
	// layer's BatchOperationResult, which uses "unsupported" -- CLI and
	// HTTP have always named this differently, not something new here).
	configErr, ok := decoded.ModeErrors["config"]
	if !ok || configErr.Type != "validation" || !strings.Contains(configErr.Message, "no config-capable driver") {
		t.Errorf("decoded.ModeErrors[\"config\"] = %+v, want type=validation mentioning no config-capable driver", configErr)
	}
}

func TestRunRejectsApplyOrYesWithoutMergeConfig(t *testing.T) {
	for _, flag := range []string{"-apply", "-yes"} {
		var stdout, stderr bytes.Buffer
		code := run(
			[]string{"-host", "h", "-username", "u", "-password", "p", "-cmd", "show version", flag},
			&stdout, &stderr, strings.NewReader(""), resolverFor(fakeDriver{}), noEnv, noReadFile,
		)
		if code != 2 {
			t.Errorf("run(%q): code = %d, want 2", flag, code)
		}
		if !strings.Contains(stderr.String(), "only apply to -merge-config") {
			t.Errorf("run(%q): stderr = %q, want it to mention -merge-config", flag, stderr.String())
		}
	}
}

func TestRunMergeConfigReadsCandidateFileAndDryRuns(t *testing.T) {
	fake := fakeDriverWithConfigLoader{diff: "+hostname R1\n", configHash: "abc123"}
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"-host", "h", "-username", "u", "-password", "p", "-merge-config", "candidate.cfg"},
		&stdout, &stderr, strings.NewReader(""), resolverFor(fake), noEnv, fakeReadFile("hostname R1\n"),
	)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "+hostname R1") {
		t.Errorf("stdout = %q, want it to contain the diff", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Dry run only") {
		t.Errorf("stdout = %q, want the dry-run-only notice (no -apply was passed)", stdout.String())
	}
}

func TestRunMergeConfigFileReadErrorIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"-host", "h", "-username", "u", "-password", "p", "-merge-config", "missing.cfg"},
		&stdout, &stderr, strings.NewReader(""), resolverFor(fakeDriver{}), noEnv, noReadFile,
	)
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "missing.cfg") {
		t.Errorf("stderr = %q, want it to name the file that failed to read", stderr.String())
	}
}

func TestRunCLIMergeConfigUnsupportedVendorFailsBeforeConnecting(t *testing.T) {
	fake := fakeDriver{connectErr: olterr.New(olterr.TypeConnection, "Connect should not have been called")}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, nil,
		false, false, false, false, "hostname R1\n", nil, false, false, false,
	)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no config-push-capable driver") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunCLIMergeConfigApplyWithYesSkipsConfirmation(t *testing.T) {
	applyCalls := 0
	fake := fakeDriverWithConfigLoader{diff: "+hostname R1\n", configHash: "abc123", applied: true, applyCalls: &applyCalls}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil,
		false, false, false, false, "hostname R1\n", nil, true, true, false,
	)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if applyCalls != 1 {
		t.Errorf("applyCalls = %d, want 1", applyCalls)
	}
	if !strings.Contains(stdout.String(), "Applied.") {
		t.Errorf("stdout = %q, want it to confirm the apply", stdout.String())
	}
	if !strings.Contains(stdout.String(), "+hostname R1") {
		t.Errorf("stdout = %q, want the diff still shown even with -yes", stdout.String())
	}
}

func TestRunCLIMergeConfigApplyWithConfirmationAccepted(t *testing.T) {
	applyCalls := 0
	fake := fakeDriverWithConfigLoader{diff: "+hostname R1\n", configHash: "abc123", applied: true, applyCalls: &applyCalls}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader("apply\n"), fake, "cisco", transport.ConnectionParams{}, nil,
		false, false, false, false, "hostname R1\n", nil, true, false, false,
	)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if applyCalls != 1 {
		t.Errorf("applyCalls = %d, want 1", applyCalls)
	}
}

func TestRunCLIMergeConfigApplyWithConfirmationDeclined(t *testing.T) {
	applyCalls := 0
	fake := fakeDriverWithConfigLoader{diff: "+hostname R1\n", configHash: "abc123", applyCalls: &applyCalls}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader("no\n"), fake, "cisco", transport.ConnectionParams{}, nil,
		false, false, false, false, "hostname R1\n", nil, true, false, false,
	)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if applyCalls != 0 {
		t.Errorf("applyCalls = %d, want 0 -- apply must never be attempted after a declined confirmation", applyCalls)
	}
	if !strings.Contains(stdout.String(), "Aborted") {
		t.Errorf("stdout = %q, want it to say the apply was aborted", stdout.String())
	}
}

func TestRunCLIMergeConfigApplyWithJSONRequiresYes(t *testing.T) {
	applyCalls := 0
	fake := fakeDriverWithConfigLoader{diff: "+hostname R1\n", configHash: "abc123", applyCalls: &applyCalls}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil,
		false, false, false, false, "hostname R1\n", nil, true, false, true,
	)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if applyCalls != 0 {
		t.Errorf("applyCalls = %d, want 0", applyCalls)
	}
}

func TestRunCLIMergeConfigApplyJSONWithYes(t *testing.T) {
	fake := fakeDriverWithConfigLoader{diff: "+hostname R1\n", configHash: "abc123", applied: true}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil,
		false, false, false, false, "hostname R1\n", nil, true, true, true,
	)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var decoded cliResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	if decoded.Status != "success" || !decoded.Applied || decoded.Diff == nil || decoded.ConfigHash == nil {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestRunRejectsCombiningMergeCmdAndCmd(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"-host", "h", "-username", "u", "-password", "p", "-cmd", "show version", "-merge-cmd", "hostname R1"},
		&stdout, &stderr, strings.NewReader(""), resolverFor(fakeDriver{}), noEnv, noReadFile,
	)
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr = %q, want it to mention the modes are mutually exclusive", stderr.String())
	}
}

func TestRunRejectsApplyOrYesWithoutMergeCmdEither(t *testing.T) {
	// TestRunRejectsApplyOrYesWithoutMergeConfig already covers plain -cmd;
	// this pins that the guard's error message also names -merge-cmd, not
	// just -merge-config, now that both flags share the same -apply/-yes
	// gate.
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"-host", "h", "-username", "u", "-password", "p", "-cmd", "show version", "-apply"},
		&stdout, &stderr, strings.NewReader(""), resolverFor(fakeDriver{}), noEnv, noReadFile,
	)
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "-merge-cmd") {
		t.Errorf("stderr = %q, want it to mention -merge-cmd", stderr.String())
	}
}

func TestRunMergeCmdDryRuns(t *testing.T) {
	fake := fakeDriverWithConfigLoader{diff: "+hostname R1\n", configHash: "abc123"}
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"-host", "h", "-username", "u", "-password", "p", "-merge-cmd", "hostname R1"},
		&stdout, &stderr, strings.NewReader(""), resolverFor(fake), noEnv, noReadFile,
	)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "+hostname R1") {
		t.Errorf("stdout = %q, want it to contain the diff", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Dry run only") {
		t.Errorf("stdout = %q, want the dry-run-only notice (no -apply was passed)", stdout.String())
	}
}

func TestRunMergeCmdAcceptsRepeatedFlags(t *testing.T) {
	// Confirms -merge-cmd is repeatable (like -cmd) and every repetition
	// reaches LoadMergeCommands, not just the first/last -- the fake
	// driver doesn't inspect commands itself, so this is really pinning
	// that flag.Var accumulation + run()'s wiring both work end to end;
	// LoadMergeCommands's own per-command behavior is covered in
	// internal/drivers's table-driven tests.
	fake := fakeDriverWithConfigLoader{diff: "+description new-desc\n", configHash: "abc123"}
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"-host", "h", "-username", "u", "-password", "p",
			"-merge-cmd", "interface GigabitEthernet1", "-merge-cmd", "description new-desc",
		},
		&stdout, &stderr, strings.NewReader(""), resolverFor(fake), noEnv, noReadFile,
	)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "+description new-desc") {
		t.Errorf("stdout = %q, want it to contain the diff", stdout.String())
	}
}

func TestRunCLIMergeCmdUnsupportedVendorFailsBeforeConnecting(t *testing.T) {
	fake := fakeDriver{connectErr: olterr.New(olterr.TypeConnection, "Connect should not have been called")}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader(""), fake, "fake", transport.ConnectionParams{}, nil,
		false, false, false, false, "", []string{"hostname R1"}, false, false, false,
	)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no config-push-capable driver") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunCLIMergeCmdApplyWithYesSkipsConfirmation(t *testing.T) {
	applyCalls := 0
	fake := fakeDriverWithConfigLoader{diff: "+hostname R1\n", configHash: "abc123", applied: true, applyCalls: &applyCalls}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil,
		false, false, false, false, "", []string{"hostname R1"}, true, true, false,
	)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if applyCalls != 1 {
		t.Errorf("applyCalls = %d, want 1", applyCalls)
	}
	if !strings.Contains(stdout.String(), "Applied.") {
		t.Errorf("stdout = %q, want it to confirm the apply", stdout.String())
	}
	if !strings.Contains(stdout.String(), "+hostname R1") {
		t.Errorf("stdout = %q, want the diff still shown even with -yes", stdout.String())
	}
}

func TestRunCLIMergeCmdApplyWithConfirmationAccepted(t *testing.T) {
	applyCalls := 0
	fake := fakeDriverWithConfigLoader{diff: "+hostname R1\n", configHash: "abc123", applied: true, applyCalls: &applyCalls}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader("apply\n"), fake, "cisco", transport.ConnectionParams{}, nil,
		false, false, false, false, "", []string{"hostname R1"}, true, false, false,
	)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if applyCalls != 1 {
		t.Errorf("applyCalls = %d, want 1", applyCalls)
	}
}

func TestRunCLIMergeCmdApplyWithConfirmationDeclined(t *testing.T) {
	applyCalls := 0
	fake := fakeDriverWithConfigLoader{diff: "+hostname R1\n", configHash: "abc123", applyCalls: &applyCalls}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader("no\n"), fake, "cisco", transport.ConnectionParams{}, nil,
		false, false, false, false, "", []string{"hostname R1"}, true, false, false,
	)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if applyCalls != 0 {
		t.Errorf("applyCalls = %d, want 0 -- apply must never be attempted after a declined confirmation", applyCalls)
	}
	if !strings.Contains(stdout.String(), "Aborted") {
		t.Errorf("stdout = %q, want it to say the apply was aborted", stdout.String())
	}
}

func TestRunCLIMergeCmdApplyWithJSONRequiresYes(t *testing.T) {
	applyCalls := 0
	fake := fakeDriverWithConfigLoader{diff: "+hostname R1\n", configHash: "abc123", applyCalls: &applyCalls}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil,
		false, false, false, false, "", []string{"hostname R1"}, true, false, true,
	)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if applyCalls != 0 {
		t.Errorf("applyCalls = %d, want 0", applyCalls)
	}
}

func TestRunCLIMergeCmdApplyJSONWithYes(t *testing.T) {
	fake := fakeDriverWithConfigLoader{diff: "+hostname R1\n", configHash: "abc123", applied: true}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil,
		false, false, false, false, "", []string{"hostname R1"}, true, true, true,
	)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var decoded cliResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	if decoded.Status != "success" || !decoded.Applied || decoded.Diff == nil || decoded.ConfigHash == nil {
		t.Errorf("decoded = %+v", decoded)
	}
}

// TestRunCLIMergeCmdApplyPrintsReverseCommandsHint confirms the
// human-readable "how to undo this" hint appears after a real apply --
// but only for -merge-cmd, which is the only mode with a reverseCommands
// equivalent (see drivers.ConfigLoader's doc comment on why -merge-config
// has none).
func TestRunCLIMergeCmdApplyPrintsReverseCommandsHint(t *testing.T) {
	fake := fakeDriverWithConfigLoader{
		diff: "+description new-desc\n", configHash: "abc123", applied: true,
		reverseCmds: []string{"interface GigabitEthernet1", "description old-desc"},
	}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil,
		false, false, false, false, "", []string{"interface GigabitEthernet1", "description new-desc"}, true, true, false,
	)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "To undo this change, re-run with:") {
		t.Errorf("stdout = %q, want the undo hint", stdout.String())
	}
	if !strings.Contains(stdout.String(), `-merge-cmd "interface GigabitEthernet1"`) ||
		!strings.Contains(stdout.String(), `-merge-cmd "description old-desc"`) {
		t.Errorf("stdout = %q, want each reverse command shown as a ready-to-copy -merge-cmd flag", stdout.String())
	}
}

// TestRunCLIMergeCmdApplyJSONIncludesReverseCommands confirms
// reverseCommands round-trips into the -json output.
func TestRunCLIMergeCmdApplyJSONIncludesReverseCommands(t *testing.T) {
	fake := fakeDriverWithConfigLoader{
		diff: "+description new-desc\n", configHash: "abc123", applied: true,
		reverseCmds: []string{"interface GigabitEthernet1", "description old-desc"},
	}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil,
		false, false, false, false, "", []string{"interface GigabitEthernet1", "description new-desc"}, true, true, true,
	)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var decoded cliResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	want := []string{"interface GigabitEthernet1", "description old-desc"}
	if len(decoded.ReverseCommands) != len(want) || decoded.ReverseCommands[0] != want[0] || decoded.ReverseCommands[1] != want[1] {
		t.Errorf("decoded.ReverseCommands = %#v, want %#v", decoded.ReverseCommands, want)
	}
}

// TestRunCLIMergeConfigApplyNeverSetsReverseCommands pins that
// -merge-config's closure in runCLI always discards reverseCommands
// (returns nil), even if the underlying driver happened to have some
// configured -- which a real LoadConfig-only driver never would, but
// this confirms the wiring itself, not just the fake's behavior.
func TestRunCLIMergeConfigApplyNeverSetsReverseCommands(t *testing.T) {
	fake := fakeDriverWithConfigLoader{
		diff: "+hostname R1\n", configHash: "abc123", applied: true,
		reverseCmds: []string{"should never appear for -merge-config"},
	}
	var stdout, stderr bytes.Buffer
	code := runCLI(
		&stdout, &stderr, strings.NewReader(""), fake, "cisco", transport.ConnectionParams{}, nil,
		false, false, false, false, "hostname R1\n", nil, true, true, true,
	)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var decoded cliResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	if decoded.ReverseCommands != nil {
		t.Errorf("decoded.ReverseCommands = %#v, want nil for -merge-config", decoded.ReverseCommands)
	}
}

func strPtr(s string) *string { return &s }
