// Command gearctl is dual-mode:
//
//   - Default: a standalone CLI client. Connects directly and either runs
//     -cmd commands, or with -facts/-interfaces/-lldp-neighbors/-config/
//     -merge-config fetches structured facts / per-interface state /
//     LLDP topology / running config / pushes a config merge -- no HTTP
//     server, no GUI, no platform-api round-trip required.
//   - -serve: the stateless HTTP API server platform-api talks to (see
//     README.md) -- POST /v1/execute, POST /v1/facts, POST /v1/interfaces,
//     POST /v1/lldp-neighbors, POST /v1/config, POST /v1/merge-config,
//     GET /health. Reads PORT/GEARCTL_SHARED_SECRET from the environment;
//     every other flag below is ignored in this mode.
//
// Both modes share the exact same driver layer (internal/drivers), so a
// vendor driver (see internal/drivers/ciscoiosdriver.go) is written once
// and works from either.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/onu-platform/gearctl/internal/config"
	"github.com/onu-platform/gearctl/internal/drivers"
	"github.com/onu-platform/gearctl/internal/httpapi"
	"github.com/onu-platform/gearctl/internal/olterr"
	"github.com/onu-platform/gearctl/internal/transport"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin, drivers.Resolve, os.Getenv, os.ReadFile))
}

// resolveFunc matches drivers.Resolve's signature -- injectable so tests
// can supply a fake driver instead of opening a real connection.
type resolveFunc func(vendor string) drivers.CliDriver

// commandList collects repeated -cmd flags in order, e.g.:
//
//	gearctl -host ... -cmd "show version" -cmd "show ip interface brief"
type commandList []string

func (c *commandList) String() string { return strings.Join(*c, ",") }
func (c *commandList) Set(value string) error {
	*c = append(*c, value)
	return nil
}

func run(
	args []string,
	stdout, stderr io.Writer,
	stdin io.Reader,
	resolve resolveFunc,
	getenv func(string) string,
	readFile func(string) ([]byte, error),
) int {
	fs := flag.NewFlagSet("gearctl", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `gearctl -- vendor-agnostic SSH/Telnet automation for network gear.

Usage:
  gearctl -host <ip> -username <user> -password <pass> -cmd "<command>" [-cmd "<command>" ...]
  gearctl -host <ip> -username <user> -password <pass> -facts
  gearctl -host <ip> -username <user> -password <pass> -interfaces
  gearctl -host <ip> -username <user> -password <pass> -lldp-neighbors
  gearctl -host <ip> -username <user> -password <pass> -config
  gearctl -host <ip> -username <user> -password <pass> -merge-config <file> [-apply [-yes]]
  gearctl -host <ip> -username <user> -password <pass> -merge-cmd "<command>" [-merge-cmd "<command>" ...] [-apply [-yes]]
  gearctl -serve

Examples:
  gearctl -host 10.0.0.1 -username admin -password secret \
    -vendor cisco -cmd "show version" -cmd "show ip interface brief"
  gearctl -host 10.0.0.1 -username admin -password secret \
    -vendor cisco -facts -json
  gearctl -host 10.0.0.1 -username admin -password secret \
    -vendor cisco -interfaces -json
  gearctl -host 10.0.0.1 -username admin -password secret \
    -vendor cisco -lldp-neighbors
  gearctl -host 10.0.0.1 -username admin -password secret \
    -vendor cisco -config
  gearctl -host 10.0.0.1 -username admin -password secret \
    -vendor cisco -facts -interfaces -config -json
  gearctl -host 10.0.0.1 -username admin -password secret \
    -vendor cisco -merge-config candidate.cfg
  gearctl -host 10.0.0.1 -username admin -password secret \
    -vendor cisco -merge-config candidate.cfg -apply
  gearctl -host 10.0.0.1 -username admin -password secret \
    -vendor cisco -merge-cmd "interface GigabitEthernet1" -merge-cmd "description new-desc" -apply
  GEARCTL_SHARED_SECRET=... gearctl -serve

-cmd/-facts/-interfaces/-lldp-neighbors/-config can be combined --
gearctl opens ONE connection and runs all requested ones over it,
instead of paying a separate connect cost per invocation. -merge-config/
-merge-cmd stay exclusive of everything (including each other) -- their
dry-run/confirm/apply flow doesn't compose the same way.

-merge-config/-merge-cmd are a MERGE, not a replace -- see README.md
before using either. Without -apply they only show a diff/preview;
nothing is written to the device.

See README.md for the full HTTP API reference and vendor support matrix.

Flags:
`)
		fs.PrintDefaults()
	}

	serve := fs.Bool("serve", false, "run as the HTTP API server (POST /v1/execute, POST /v1/facts, GET /health) "+
		"instead of a one-shot CLI command -- see README.md. Reads PORT/GEARCTL_SHARED_SECRET from the "+
		"environment; every flag below is ignored in this mode.")

	host := fs.String("host", "", "device host/IP (required unless -serve)")
	port := fs.Int("port", 22, "device port")
	protocol := fs.String("protocol", "ssh", `transport: "ssh" or "telnet"`)
	vendor := fs.String("vendor", "", `vendor hint (e.g. "cisco") -- selects the driver and gates enable-mode escalation`)
	username := fs.String("username", "", "login username (required unless -serve)")
	password := fs.String("password", "", "login password (required unless -serve; falls back to $OLT_CLI_PASSWORD)")
	enablePassword := fs.String("enable-password", "", "enable/privileged-mode password, if the vendor needs one (falls back to $OLT_CLI_ENABLE_PASSWORD)")
	disablePaging := fs.String("disable-paging", "", `command sent first to disable output paging, e.g. "terminal length 0"`)
	connectTimeoutMs := fs.Int("connect-timeout-ms", transport.DefaultConnectTimeoutMs, "connect timeout in milliseconds")
	perCommandTimeoutMs := fs.Int("per-command-timeout-ms", transport.DefaultPerCommandTimeoutMs, "per-command timeout in milliseconds")
	wantFacts := fs.Bool("facts", false, "fetch structured facts (GetFacts()) instead of running -cmd commands -- "+
		"fails if the resolved vendor driver doesn't support it")
	wantInterfaces := fs.Bool("interfaces", false, "fetch richer per-interface state (GetInterfaces()) -- "+
		"up/down, enabled, description, MTU, speed, MAC -- instead of running -cmd commands. Fails if the "+
		"resolved vendor driver doesn't support it")
	wantLLDPNeighbors := fs.Bool("lldp-neighbors", false, "fetch LLDP topology (GetLLDPNeighbors()) -- which local "+
		"port sees which remote device/port -- instead of running -cmd commands. Requires LLDP enabled on the "+
		"device (\"lldp run\"). Fails if the resolved vendor driver doesn't support it")
	wantConfig := fs.Bool("config", false, "fetch the device's running configuration (GetConfig()) instead of "+
		"running -cmd commands -- fails if the resolved vendor driver doesn't support it")
	mergeConfigFile := fs.String("merge-config", "", "path to a candidate config file to merge onto the device "+
		"(LoadConfig()) -- NOT a replace, see README.md. Without -apply, only shows a diff; nothing is written")
	applyMerge := fs.Bool("apply", false, "actually apply -merge-config's/-merge-cmd's candidate (default is "+
		"dry-run/diff-only). Prompts for interactive confirmation unless -yes is also passed")
	skipConfirm := fs.Bool("yes", false, "skip -apply's interactive confirmation prompt (for scripted use) -- "+
		"has no effect without -apply")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON instead of human-readable text")
	var commands commandList
	fs.Var(&commands, "cmd", `a command to run (repeatable, e.g. -cmd "show version" -cmd "show ip interface brief")`)
	var mergeCmd commandList
	fs.Var(&mergeCmd, "merge-cmd", `a command to merge onto the device (LoadMergeCommands()) -- repeatable, `+
		`e.g. -merge-cmd "interface GigabitEthernet1" -merge-cmd "description new-desc". NOT a replace, see `+
		`README.md. Without -apply, only shows a projected preview diff; nothing is written`)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// -h/-help/--help isn't a usage error -- flag.FlagSet already
			// printed the (custom) usage text above via fs.Usage().
			return 0
		}
		return 2
	}

	if *serve {
		return runServe(stdout, stderr)
	}

	if *password == "" {
		*password = getenv("OLT_CLI_PASSWORD")
	}
	if *enablePassword == "" {
		*enablePassword = getenv("OLT_CLI_ENABLE_PASSWORD")
	}

	if *host == "" {
		fmt.Fprintln(stderr, "error: -host is required")
		return 2
	}
	if *username == "" {
		fmt.Fprintln(stderr, "error: -username is required")
		return 2
	}
	if *password == "" {
		fmt.Fprintln(stderr, "error: -password is required (flag or $OLT_CLI_PASSWORD)")
		return 2
	}
	if *protocol != "ssh" && *protocol != "telnet" {
		fmt.Fprintln(stderr, `error: -protocol must be "ssh" or "telnet"`)
		return 2
	}
	// -merge-config and -merge-cmd stay fully exclusive of everything,
	// including each other -- their dry-run/confirm/apply flow doesn't
	// compose with running other things over the same connection (what
	// should the rest of a combined call do if the merge step needs
	// interactive confirmation, or fails partway through applying?). Every
	// other mode -- -cmd, -facts, -interfaces, -lldp-neighbors, -config --
	// is "batchable": any non-empty combination of them runs together over
	// a single connection instead of requiring one gearctl invocation per
	// mode (see runBatchModes' doc comment for why -- the same
	// NAPALM/Netmiko benchmark finding POST /v1/batch fixes on the HTTP
	// side). len(commands) > 0 counts as one "-cmd mode" regardless of how
	// many -cmd flags were repeated; same for -merge-cmd.
	mergeModesSelected := 0
	if *mergeConfigFile != "" {
		mergeModesSelected++
	}
	if len(mergeCmd) > 0 {
		mergeModesSelected++
	}

	batchModesSelected := 0
	if len(commands) > 0 {
		batchModesSelected++
	}
	if *wantFacts {
		batchModesSelected++
	}
	if *wantInterfaces {
		batchModesSelected++
	}
	if *wantLLDPNeighbors {
		batchModesSelected++
	}
	if *wantConfig {
		batchModesSelected++
	}

	if mergeModesSelected == 0 && batchModesSelected == 0 {
		fmt.Fprintln(stderr, "error: provide at least one -cmd, or pass -facts, -interfaces, -lldp-neighbors, -config, -merge-config, or -merge-cmd")
		return 2
	}
	if mergeModesSelected > 1 {
		fmt.Fprintln(stderr, "error: -merge-config and -merge-cmd are mutually exclusive -- pass exactly one")
		return 2
	}
	if mergeModesSelected == 1 && batchModesSelected > 0 {
		fmt.Fprintln(stderr, "error: -merge-config/-merge-cmd are mutually exclusive of -cmd/-facts/-interfaces/-lldp-neighbors/-config -- pass a merge mode on its own")
		return 2
	}
	if (*applyMerge || *skipConfirm) && *mergeConfigFile == "" && len(mergeCmd) == 0 {
		fmt.Fprintln(stderr, "error: -apply/-yes only apply to -merge-config/-merge-cmd")
		return 2
	}

	var mergeConfigCandidate string
	if *mergeConfigFile != "" {
		content, err := readFile(*mergeConfigFile)
		if err != nil {
			fmt.Fprintf(stderr, "error: failed to read -merge-config file %q: %v\n", *mergeConfigFile, err)
			return 2
		}
		mergeConfigCandidate = string(content)
	}

	params := transport.ConnectionParams{
		Host:                 *host,
		Port:                 *port,
		Protocol:             *protocol,
		Vendor:               *vendor,
		Username:             *username,
		Password:             *password,
		EnablePassword:       *enablePassword,
		ConnectTimeoutMs:     *connectTimeoutMs,
		PerCommandTimeoutMs:  *perCommandTimeoutMs,
		DisablePagingCommand: *disablePaging,
	}

	return runCLI(
		stdout, stderr, stdin, resolve(*vendor), *vendor, params, commands,
		*wantFacts, *wantInterfaces, *wantLLDPNeighbors, *wantConfig, mergeConfigCandidate, mergeCmd,
		*applyMerge, *skipConfirm, *jsonOutput,
	)
}

// runServe starts the HTTP API server, identical to this binary's original
// (pre dual-mode) behavior.
func runServe(stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "config error: %v\n", err)
		return 2
	}
	handler := httpapi.NewServer(cfg.SharedSecret)
	addr := fmt.Sprintf(":%d", cfg.Port)
	fmt.Fprintf(stdout, "gearctl listening on %s\n", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		fmt.Fprintf(stderr, "server error: %v\n", err)
		return 1
	}
	return 0
}

// runCLI connects, runs -cmd commands / -facts / -config / -merge-config /
// -merge-cmd (exactly one, enforced by run()), prints the result, and
// returns the process exit code: 0 on full success, 1 on any
// connection/command/facts/config/merge-level failure, matching normal
// Unix conventions for scripting.
//
// -facts/-interfaces/-lldp-neighbors/-config/-merge-config/-merge-cmd
// check FactsProvider/InterfaceProvider/LLDPNeighborsProvider/
// ConfigGetter/ConfigLoader support BEFORE connecting -- same order as
// httpapi.FactsHandler/InterfacesHandler/LLDPNeighborsHandler/
// ConfigHandler/MergeConfigHandler/MergeCmdHandler -- so an unsupported
// vendor fails immediately instead of wasting a dial (and a timeout) on
// a device that could never produce facts/interfaces/LLDP data/config/
// apply a merge.
func runCLI(
	stdout, stderr io.Writer,
	stdin io.Reader,
	driver drivers.CliDriver,
	vendor string,
	params transport.ConnectionParams,
	commands []string,
	wantFacts bool,
	wantInterfaces bool,
	wantLLDPNeighbors bool,
	wantConfig bool,
	mergeConfigCandidate string,
	mergeCmdCommands []string,
	applyMerge bool,
	skipConfirm bool,
	jsonOutput bool,
) int {
	startedAt := time.Now()

	// More than one of -cmd/-facts/-interfaces/-lldp-neighbors/-config
	// selected together -- run them all in ONE connection instead of the
	// single-mode paths below. See runBatchModes' doc comment for why
	// (the NAPALM/Netmiko benchmark finding this closes) and why
	// -merge-config/-merge-cmd are never part of this (run() enforces
	// they can't be combined with anything at all, so reaching this
	// function with either one set means batchModesActive is always 0).
	batchModesActive := 0
	if len(commands) > 0 {
		batchModesActive++
	}
	if wantFacts {
		batchModesActive++
	}
	if wantInterfaces {
		batchModesActive++
	}
	if wantLLDPNeighbors {
		batchModesActive++
	}
	if wantConfig {
		batchModesActive++
	}
	if batchModesActive > 1 {
		return runBatchModes(
			stdout, driver, vendor, params, commands,
			wantFacts, wantInterfaces, wantLLDPNeighbors, wantConfig, jsonOutput, startedAt,
		)
	}

	var factsProvider drivers.FactsProvider
	if wantFacts {
		var ok bool
		factsProvider, ok = driver.(drivers.FactsProvider)
		if !ok {
			err := olterr.New(olterr.TypeValidation, fmt.Sprintf("vendor %q has no facts-capable driver", vendor))
			return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
		}
	}

	var interfaceProvider drivers.InterfaceProvider
	if wantInterfaces {
		var ok bool
		interfaceProvider, ok = driver.(drivers.InterfaceProvider)
		if !ok {
			err := olterr.New(olterr.TypeValidation, fmt.Sprintf("vendor %q has no interfaces-capable driver", vendor))
			return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
		}
	}

	var lldpProvider drivers.LLDPNeighborsProvider
	if wantLLDPNeighbors {
		var ok bool
		lldpProvider, ok = driver.(drivers.LLDPNeighborsProvider)
		if !ok {
			err := olterr.New(olterr.TypeValidation, fmt.Sprintf("vendor %q has no LLDP-capable driver", vendor))
			return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
		}
	}

	var configGetter drivers.ConfigGetter
	if wantConfig {
		var ok bool
		configGetter, ok = driver.(drivers.ConfigGetter)
		if !ok {
			err := olterr.New(olterr.TypeValidation, fmt.Sprintf("vendor %q has no config-capable driver", vendor))
			return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
		}
	}

	var configLoader drivers.ConfigLoader
	if mergeConfigCandidate != "" || len(mergeCmdCommands) > 0 {
		var ok bool
		configLoader, ok = driver.(drivers.ConfigLoader)
		if !ok {
			err := olterr.New(olterr.TypeValidation, fmt.Sprintf("vendor %q has no config-push-capable driver", vendor))
			return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
		}
	}

	if err := driver.Connect(params); err != nil {
		return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
	}
	defer func() { _ = driver.Disconnect() }()

	if wantFacts {
		facts, err := factsProvider.GetFacts()
		if err != nil {
			return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
		}
		return reportFacts(stdout, jsonOutput, facts, time.Since(startedAt))
	}

	if wantInterfaces {
		interfaces, err := interfaceProvider.GetInterfaces()
		if err != nil {
			return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
		}
		return reportInterfaces(stdout, jsonOutput, interfaces, time.Since(startedAt))
	}

	if wantLLDPNeighbors {
		neighbors, err := lldpProvider.GetLLDPNeighbors()
		if err != nil {
			return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
		}
		return reportLLDPNeighbors(stdout, jsonOutput, neighbors, time.Since(startedAt))
	}

	if wantConfig {
		config, err := configGetter.GetConfig()
		if err != nil {
			return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
		}
		return reportConfig(stdout, jsonOutput, config, time.Since(startedAt))
	}

	if mergeConfigCandidate != "" {
		return runMergeConfig(
			stdout, stderr, stdin, params.Host, applyMerge, skipConfirm, jsonOutput, startedAt,
			func(dryRun bool, expectedHash string) (string, []string, string, bool, error) {
				// LoadConfig has no reverseCommands equivalent -- see
				// ConfigLoader's doc comment for why (a full-candidate
				// text diff has no per-command operation log to invert).
				diff, configHash, applied, err := configLoader.LoadConfig(mergeConfigCandidate, dryRun, expectedHash)
				return diff, nil, configHash, applied, err
			},
		)
	}

	if len(mergeCmdCommands) > 0 {
		return runMergeConfig(
			stdout, stderr, stdin, params.Host, applyMerge, skipConfirm, jsonOutput, startedAt,
			func(dryRun bool, expectedHash string) (string, []string, string, bool, error) {
				return configLoader.LoadMergeCommands(mergeCmdCommands, dryRun, expectedHash)
			},
		)
	}

	results, err := driver.RunCommands(commands)
	if err != nil {
		return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
	}
	return reportCommands(stdout, jsonOutput, results, time.Since(startedAt))
}

// batchModeResult is one requested mode's outcome within a combined
// multi-mode CLI invocation (more than one of -cmd/-facts/-interfaces/
// -lldp-neighbors/-config passed together, so they share a single
// connection -- see runBatchModes' doc comment). Mirrors
// httpapi.BatchOperationResult conceptually. Exactly one of
// facts/interfaces/neighbors/config/commands is ever populated, matching
// whichever mode this result is for; err is non-nil for an unsupported
// or failed mode ("cmd" only sets err for a hard RunCommands-level
// failure -- a per-command failure lives in commands[i].Error instead,
// same as -cmd's existing single-mode behavior, with overall status
// derived via commandsStatus).
type batchModeResult struct {
	mode       string
	status     string // "success" | "partial" | "failed"
	err        error
	facts      map[string]interface{}
	interfaces map[string]drivers.InterfaceInfo
	neighbors  map[string][]drivers.LLDPNeighbor
	config     string
	commands   []transport.CommandResult
}

// runBatchModes runs every requested mode -- any combination of -cmd/
// -facts/-interfaces/-lldp-neighbors/-config -- over a SINGLE connection,
// instead of the caller needing a separate gearctl invocation (each
// paying its own connect/disconnect cost) per mode. This is the CLI
// counterpart to POST /v1/batch (internal/httpapi/batchhandler.go) --
// same fix for the same finding, benchmarked live against NAPALM/
// Netmiko: connection reuse across multiple getters was the one place
// NAPALM's session model won a head-to-head speed comparison. Captured
// here with the exact same "still just one process invocation, one
// connection, nothing persists after it exits" property gearctl already
// has -- this isn't a new session/statefulness mechanism, just doing
// more work inside the one connection a single invocation already opens.
//
// Each mode's capability is checked BEFORE connecting, the same
// fail-fast order the single-mode paths use -- an unsupported mode
// doesn't cost a connection attempt, and doesn't stop any OTHER
// requested mode that IS supported from running. If not a single
// requested mode is supported, this returns without ever dialing,
// matching the same "fails immediately" contract callers already rely
// on for one mode at a time.
func runBatchModes(
	stdout io.Writer,
	driver drivers.CliDriver,
	vendor string,
	params transport.ConnectionParams,
	commands []string,
	wantFacts, wantInterfaces, wantLLDPNeighbors, wantConfig bool,
	jsonOutput bool,
	startedAt time.Time,
) int {
	type modeSpec struct {
		mode      string
		requested bool
		supported func() bool
	}
	specs := []modeSpec{
		{"facts", wantFacts, func() bool { _, ok := driver.(drivers.FactsProvider); return ok }},
		{"interfaces", wantInterfaces, func() bool { _, ok := driver.(drivers.InterfaceProvider); return ok }},
		{"lldp-neighbors", wantLLDPNeighbors, func() bool { _, ok := driver.(drivers.LLDPNeighborsProvider); return ok }},
		{"config", wantConfig, func() bool { _, ok := driver.(drivers.ConfigGetter); return ok }},
		{"cmd", len(commands) > 0, func() bool { return true }},
	}

	outcomes := make(map[string]batchModeResult, len(specs))
	anySupported := false
	for _, spec := range specs {
		if !spec.requested {
			continue
		}
		if !spec.supported() {
			outcomes[spec.mode] = batchModeResult{
				mode:   spec.mode,
				status: "failed",
				err:    olterr.New(olterr.TypeValidation, fmt.Sprintf("vendor %q has no %s-capable driver", vendor, spec.mode)),
			}
			continue
		}
		anySupported = true
	}

	if anySupported {
		if err := driver.Connect(params); err != nil {
			for _, spec := range specs {
				if !spec.requested {
					continue
				}
				if _, already := outcomes[spec.mode]; already {
					continue
				}
				outcomes[spec.mode] = batchModeResult{mode: spec.mode, status: "failed", err: err}
			}
		} else {
			defer func() { _ = driver.Disconnect() }()
			for _, spec := range specs {
				if !spec.requested {
					continue
				}
				if _, already := outcomes[spec.mode]; already {
					continue
				}
				outcomes[spec.mode] = runOneBatchMode(driver, spec.mode, commands)
			}
		}
	}

	results := make([]batchModeResult, 0, len(specs))
	for _, spec := range specs {
		if spec.requested {
			results = append(results, outcomes[spec.mode])
		}
	}

	return reportBatchResults(stdout, jsonOutput, results, time.Since(startedAt))
}

// runOneBatchMode actually calls the driver method mode maps to. Only
// ever called after the capability pre-check already confirmed the
// driver satisfies what this mode needs, so the type assertions here are
// never the ones that fail.
func runOneBatchMode(driver drivers.CliDriver, mode string, commands []string) batchModeResult {
	switch mode {
	case "facts":
		facts, err := driver.(drivers.FactsProvider).GetFacts()
		if err != nil {
			return batchModeResult{mode: mode, status: "failed", err: err}
		}
		return batchModeResult{mode: mode, status: "success", facts: facts}

	case "interfaces":
		interfaces, err := driver.(drivers.InterfaceProvider).GetInterfaces()
		if err != nil {
			return batchModeResult{mode: mode, status: "failed", err: err}
		}
		return batchModeResult{mode: mode, status: "success", interfaces: interfaces}

	case "lldp-neighbors":
		neighbors, err := driver.(drivers.LLDPNeighborsProvider).GetLLDPNeighbors()
		if err != nil {
			return batchModeResult{mode: mode, status: "failed", err: err}
		}
		return batchModeResult{mode: mode, status: "success", neighbors: neighbors}

	case "config":
		config, err := driver.(drivers.ConfigGetter).GetConfig()
		if err != nil {
			return batchModeResult{mode: mode, status: "failed", err: err}
		}
		return batchModeResult{mode: mode, status: "success", config: config}

	case "cmd":
		cmdResults, err := driver.RunCommands(commands)
		if err != nil {
			return batchModeResult{mode: mode, status: "failed", err: err}
		}
		return batchModeResult{mode: mode, status: commandsStatus(cmdResults), commands: cmdResults}
	}
	// Unreachable: mode is always one of the switch cases above, since it
	// only ever comes from runBatchModes' own fixed spec list.
	return batchModeResult{mode: mode, status: "failed", err: fmt.Errorf("unknown batch mode %q", mode)}
}

// reportBatchResults prints/encodes a runBatchModes outcome. Overall
// status: "success" only if every requested mode succeeded, "partial" if
// some did and some didn't, "failed" if none did. jsonOutput reuses
// cliResult directly -- it already has optional fields for every mode's
// payload; ModeErrors carries per-mode errors when the overall status is
// "partial" (some succeeded, so there's no single overall Error to
// report, but the failed ones still need their own error surfaced).
// Human-readable output prints one "=== mode ===" section per requested
// mode, in the same fixed order they're always checked/run in, reusing
// each mode's own print*Body helper so the formatting is identical to
// that mode's single-mode output.
func reportBatchResults(stdout io.Writer, jsonOutput bool, results []batchModeResult, elapsed time.Duration) int {
	anyFailed := false
	allFailed := true
	for _, res := range results {
		if res.status != "success" {
			anyFailed = true
		}
		if res.status != "failed" {
			allFailed = false
		}
	}
	status := "success"
	switch {
	case allFailed:
		status = "failed"
	case anyFailed:
		status = "partial"
	}

	if jsonOutput {
		out := cliResult{Status: status, TotalDurationMs: elapsed.Milliseconds()}
		var modeErrors map[string]*cliError
		for _, res := range results {
			switch res.mode {
			case "facts":
				if res.status == "success" {
					out.Facts = res.facts
				}
			case "interfaces":
				if res.status == "success" {
					out.Interfaces = res.interfaces
				}
			case "lldp-neighbors":
				if res.status == "success" {
					out.Neighbors = res.neighbors
				}
			case "config":
				if res.status == "success" {
					c := res.config
					out.Config = &c
				}
			case "cmd":
				if res.commands != nil {
					out.Results = res.commands
				}
			}
			if res.status != "success" && res.err != nil {
				if modeErrors == nil {
					modeErrors = make(map[string]*cliError)
				}
				modeErrors[res.mode] = toCliError(res.err)
			}
		}
		out.ModeErrors = modeErrors
		_ = json.NewEncoder(stdout).Encode(out)
		if status != "success" {
			return 1
		}
		return 0
	}

	for _, res := range results {
		fmt.Fprintf(stdout, "=== %s ===\n", res.mode)
		switch {
		case res.mode == "cmd" && res.commands != nil:
			printCommandsBody(stdout, res.commands)
		case res.status != "success":
			fmt.Fprintf(stdout, "failed: %v\n", res.err)
		case res.mode == "facts":
			printFactsBody(stdout, res.facts)
		case res.mode == "interfaces":
			printInterfacesBody(stdout, res.interfaces)
		case res.mode == "lldp-neighbors":
			printLLDPNeighborsBody(stdout, res.neighbors)
		case res.mode == "config":
			fmt.Fprintln(stdout, res.config)
		}
		fmt.Fprintln(stdout)
	}

	if status != "success" {
		return 1
	}
	return 0
}

// runMergeConfig always dry-runs first (fetching a diff + configHash --
// no device write yet). Without applyMerge, that's the entire operation.
// With applyMerge, it prints the diff and requires a typed "apply"
// confirmation on stdin (unless skipConfirm, for scripted use) before
// calling load again with dryRun=false, echoing back the same configHash
// the dry run returned (see drivers.ConfigLoader's doc comment for why --
// it's the freshness check that refuses to apply if the device changed
// since this dry run).
//
// load is a closure over either configLoader.LoadConfig(candidate, ...)
// or configLoader.LoadMergeCommands(commands, ...) -- -merge-config and
// -merge-cmd share this exact same dry-run/confirm/apply/report flow,
// differing only in which ConfigLoader method (and which candidate data)
// they call. reverseCommands is only ever non-nil for -merge-cmd (see
// ConfigLoader's doc comment on why LoadConfig has no equivalent) --
// the -merge-config closure always returns nil for it.
//
// Interactive confirmation is deliberately incompatible with jsonOutput:
// reading from stdin while also trying to emit clean JSON to stdout is an
// unclear combination, so it's refused outright rather than guessed at --
// pass -yes for scripted/JSON apply.
func runMergeConfig(
	stdout, stderr io.Writer,
	stdin io.Reader,
	host string,
	applyMerge, skipConfirm, jsonOutput bool,
	startedAt time.Time,
	load func(dryRun bool, expectedHash string) (diff string, reverseCommands []string, configHash string, applied bool, err error),
) int {
	diff, reverseCommands, configHash, _, err := load(true, "")
	if err != nil {
		return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
	}

	if !applyMerge {
		return reportMergeConfig(stdout, jsonOutput, diff, reverseCommands, configHash, false, false, time.Since(startedAt))
	}

	diffAlreadyShown := false
	if !skipConfirm {
		if jsonOutput {
			err := olterr.New(
				olterr.TypeValidation,
				"interactive confirmation isn't supported with -json -- pass -yes to confirm apply in scripted/JSON mode",
			)
			return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
		}
		fmt.Fprint(stdout, diff)
		diffAlreadyShown = true
		fmt.Fprintf(stdout, "\nApply these changes to %s? Type \"apply\" to confirm: ", host)
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if strings.TrimSpace(line) != "apply" {
			fmt.Fprintln(stdout, "Aborted -- no changes applied.")
			return 1
		}
	}

	_, _, _, applied, err := load(false, configHash)
	if err != nil {
		return reportFailure(stdout, stderr, jsonOutput, err, time.Since(startedAt))
	}
	return reportMergeConfig(stdout, jsonOutput, diff, reverseCommands, configHash, applied, diffAlreadyShown, time.Since(startedAt))
}

// cliResult is the CLI's -json output shape -- deliberately the same
// field names/semantics as httpapi.ExecuteResponse/FactsResponse/
// ConfigResponse (status, error.type/message, totalDurationMs) so a
// script that already parses the HTTP API's JSON can parse this with no
// changes.
type cliResult struct {
	Status     string                            `json:"status"`
	Results    []transport.CommandResult         `json:"results,omitempty"`
	Facts      map[string]interface{}            `json:"facts,omitempty"`
	Interfaces map[string]drivers.InterfaceInfo  `json:"interfaces,omitempty"`
	Neighbors  map[string][]drivers.LLDPNeighbor `json:"neighbors,omitempty"`
	Config     *string                           `json:"config,omitempty"`
	Diff       *string                           `json:"diff,omitempty"`
	ConfigHash *string                           `json:"configHash,omitempty"`
	// ReverseCommands is only ever populated by -merge-cmd (LoadConfig
	// has no equivalent -- see drivers.ConfigLoader's doc comment) -- the
	// commands that would undo this merge, for scripting a "confirm
	// within N minutes or auto-revert" workflow on top.
	ReverseCommands []string  `json:"reverseCommands,omitempty"`
	Applied         bool      `json:"applied,omitempty"`
	Error           *cliError `json:"error,omitempty"`
	// ModeErrors is only ever populated when combining more than one of
	// -cmd/-facts/-interfaces/-lldp-neighbors/-config in a single
	// invocation (see runBatchModes) AND some of them failed while others
	// succeeded -- keyed by mode name ("facts", "interfaces",
	// "lldp-neighbors", "config", "cmd"). Distinct from Error, which
	// stays reserved for "the whole call never got anywhere" (e.g. every
	// mode connection failure) -- a single-mode invocation never sets
	// this field at all, only Error.
	ModeErrors      map[string]*cliError `json:"modeErrors,omitempty"`
	TotalDurationMs int64                `json:"totalDurationMs"`
}

type cliError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func toCliError(err error) *cliError {
	if cliErr, ok := err.(*olterr.OltCliError); ok {
		return &cliError{Type: string(cliErr.ErrType), Message: cliErr.Msg}
	}
	return &cliError{Type: "connection", Message: err.Error()}
}

func reportFailure(stdout, stderr io.Writer, jsonOutput bool, err error, elapsed time.Duration) int {
	cliErr := toCliError(err)
	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(cliResult{
			Status:          "failed",
			Error:           cliErr,
			TotalDurationMs: elapsed.Milliseconds(),
		})
	} else {
		fmt.Fprintf(stderr, "failed (%s): %s\n", cliErr.Type, cliErr.Message)
	}
	return 1
}

// printFactsBody/printInterfacesBody/printLLDPNeighborsBody/
// printCommandsBody are the human-readable rendering for each mode,
// factored out of report{Facts,Interfaces,LLDPNeighbors,Commands} so
// runBatchModes (multiple modes combined in one connection -- see its
// doc comment) can print each section using the EXACT same formatting
// a single-mode invocation would, without duplicating it.
func printFactsBody(stdout io.Writer, facts map[string]interface{}) {
	keys := make([]string, 0, len(facts))
	for k := range facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(stdout, "%-15s %v\n", k+":", facts[k])
	}
}

func reportFacts(stdout io.Writer, jsonOutput bool, facts map[string]interface{}, elapsed time.Duration) int {
	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(cliResult{
			Status:          "success",
			Facts:           facts,
			TotalDurationMs: elapsed.Milliseconds(),
		})
		return 0
	}
	printFactsBody(stdout, facts)
	return 0
}

func printInterfacesBody(stdout io.Writer, interfaces map[string]drivers.InterfaceInfo) {
	names := make([]string, 0, len(interfaces))
	for name := range interfaces {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		info := interfaces[name]
		status := "down"
		if info.IsUp {
			status = "up"
		}
		if !info.IsEnabled {
			status = "administratively down"
		}
		desc := info.Description
		if desc == "" {
			desc = "-"
		}
		ip := info.IPAddress
		if ip == "" {
			ip = "-"
		}
		fmt.Fprintf(stdout, "%-20s %-22s ip=%-18s mtu=%-6d speed=%-8gMbps mac=%-14s desc=%s\n",
			name, status, ip, info.MTU, info.SpeedMbps, info.MACAddress, desc)
	}
}

func reportInterfaces(stdout io.Writer, jsonOutput bool, interfaces map[string]drivers.InterfaceInfo, elapsed time.Duration) int {
	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(cliResult{
			Status:          "success",
			Interfaces:      interfaces,
			TotalDurationMs: elapsed.Milliseconds(),
		})
		return 0
	}
	printInterfacesBody(stdout, interfaces)
	return 0
}

func printLLDPNeighborsBody(stdout io.Writer, neighbors map[string][]drivers.LLDPNeighbor) {
	localIntfs := make([]string, 0, len(neighbors))
	for name := range neighbors {
		localIntfs = append(localIntfs, name)
	}
	sort.Strings(localIntfs)
	if len(localIntfs) == 0 {
		fmt.Fprintln(stdout, "No LLDP neighbors seen.")
		return
	}
	for _, localIntf := range localIntfs {
		for _, n := range neighbors[localIntf] {
			fmt.Fprintf(stdout, "%-20s -> %-30s (remote port %s)\n", localIntf, n.RemoteSystemName, n.RemotePortID)
		}
	}
}

func reportLLDPNeighbors(stdout io.Writer, jsonOutput bool, neighbors map[string][]drivers.LLDPNeighbor, elapsed time.Duration) int {
	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(cliResult{
			Status:          "success",
			Neighbors:       neighbors,
			TotalDurationMs: elapsed.Milliseconds(),
		})
		return 0
	}
	printLLDPNeighborsBody(stdout, neighbors)
	return 0
}

func reportConfig(stdout io.Writer, jsonOutput bool, config string, elapsed time.Duration) int {
	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(cliResult{
			Status:          "success",
			Config:          &config,
			TotalDurationMs: elapsed.Milliseconds(),
		})
		return 0
	}
	fmt.Fprintln(stdout, config)
	return 0
}

// reportMergeConfig always exits 0 -- reaching this function means the
// dry-run/apply call itself completed without error; applied tells the
// caller (in either output mode) whether a device write actually
// happened, distinct from "the request succeeded". diffAlreadyShown
// avoids printing the diff twice when the interactive confirmation
// prompt already displayed it (see runMergeConfig) -- it's always
// included in the JSON body regardless, since that's a single
// machine-readable record, not a terminal transcript. reverseCommands is
// only ever non-nil for -merge-cmd (see drivers.ConfigLoader's doc
// comment on why -merge-config has no equivalent) -- printed as a
// ready-to-copy "how to undo this" hint only once a write actually
// happened, since there's nothing to undo from a dry run.
func reportMergeConfig(
	stdout io.Writer, jsonOutput bool, diff string, reverseCommands []string, configHash string,
	applied, diffAlreadyShown bool, elapsed time.Duration,
) int {
	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(cliResult{
			Status:          "success",
			Diff:            &diff,
			ConfigHash:      &configHash,
			ReverseCommands: reverseCommands,
			Applied:         applied,
			TotalDurationMs: elapsed.Milliseconds(),
		})
		return 0
	}
	if !diffAlreadyShown {
		fmt.Fprint(stdout, diff)
		// Guarantee a line break before whatever's printed next --
		// CiscoIOSDriver's difflib-based diffs always end with their own
		// trailing "\n" already, but that's not guaranteed for every
		// driver (JuniperDriver's diff is real, trimmed device output --
		// see its own doc comments -- with no such guarantee), and this
		// print site shouldn't have to assume one vendor's convention
		// holds for all of them.
		if !strings.HasSuffix(diff, "\n") {
			fmt.Fprintln(stdout)
		}
	}
	if applied {
		fmt.Fprintln(stdout, "Applied.")
		if len(reverseCommands) > 0 {
			fmt.Fprintln(stdout, "\nTo undo this change, re-run with:")
			for _, cmd := range reverseCommands {
				fmt.Fprintf(stdout, "  -merge-cmd %q\n", cmd)
			}
		}
	} else {
		fmt.Fprintln(stdout, "Dry run only -- no changes applied. Pass -apply to apply.")
	}
	return 0
}

// commandsStatus mirrors ExecuteHandler's status computation exactly --
// "success" only if every command succeeded, "partial" if some did and
// some didn't, "failed" if none did (or there were none to run).
func commandsStatus(results []transport.CommandResult) string {
	anyFailed := false
	allFailed := len(results) > 0
	for _, r := range results {
		if r.Error != "" {
			anyFailed = true
		} else {
			allFailed = false
		}
	}
	switch {
	case allFailed:
		return "failed"
	case anyFailed:
		return "partial"
	default:
		return "success"
	}
}

func printCommandsBody(stdout io.Writer, results []transport.CommandResult) {
	for _, r := range results {
		fmt.Fprintf(stdout, "=== %s ===\n", r.Command)
		if r.Error != "" {
			fmt.Fprintf(stdout, "[error: %s]\n", r.Error)
			continue
		}
		if r.Output != nil {
			fmt.Fprintln(stdout, *r.Output)
		}
		if r.Truncated {
			fmt.Fprintln(stdout, "[output truncated]")
		}
	}
}

func reportCommands(stdout io.Writer, jsonOutput bool, results []transport.CommandResult, elapsed time.Duration) int {
	status := commandsStatus(results)

	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(cliResult{
			Status:          status,
			Results:         results,
			TotalDurationMs: elapsed.Milliseconds(),
		})
	} else {
		printCommandsBody(stdout, results)
	}

	if status != "success" {
		return 1
	}
	return 0
}
