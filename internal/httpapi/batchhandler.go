package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/onu-platform/gearctl/internal/drivers"
	"github.com/onu-platform/gearctl/internal/security"
)

// BatchHandler runs BatchRequest.Operations in order over a SINGLE
// connection -- the fix for gearctl's per-request reconnect cost
// (confirmed live, benchmarked against NAPALM/Netmiko: connection reuse
// across multiple getters is where NAPALM's session model won). See
// BatchRequest's doc comment for why this stays read-only, and why that
// doesn't compromise gearctl's stateless design at all -- it's still one
// request, one connection, one bounded lifetime.
//
// Each operation's own Status/Error is independent: a vendor missing the
// capability an operation needs marks THAT operation "unsupported"
// before ever connecting, and every other operation in the list still
// runs normally. The overall Status is "success" only if every operation
// succeeded, "partial" if some did and some didn't, "failed" if none did
// (including the case where the connection itself never succeeded, or
// every requested operation turned out unsupported).
func BatchHandler(w http.ResponseWriter, r *http.Request) {
	var req BatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBatchValidationError(w, "invalid JSON body: "+err.Error())
		return
	}

	if msg := req.Validate(); msg != "" {
		writeBatchValidationError(w, msg)
		return
	}

	if !security.IsAllowedOltTarget(req.Host) {
		writeBatchValidationError(w, fmt.Sprintf("Target host %q is not allowed", req.Host))
		return
	}

	startedAt := time.Now()
	driver := drivers.Resolve(req.Vendor)

	// Pre-check every operation's capability BEFORE ever dialing -- same
	// fail-fast posture as the single-purpose handlers (FactsHandler etc.)
	// -- so an operation this vendor could never satisfy doesn't cost a
	// connection attempt, and doesn't block the OTHER operations that are
	// actually supported from running.
	results := make([]BatchOperationResult, len(req.Operations))
	anySupported := false
	for i, op := range req.Operations {
		results[i].Type = op.Type
		if !batchOperationSupported(driver, op.Type) {
			results[i].Status = "failed"
			results[i].Error = &ExecuteError{
				Type:    "unsupported",
				Message: fmt.Sprintf("vendor %q has no driver capable of %q", req.Vendor, op.Type),
			}
			continue
		}
		anySupported = true
	}

	if !anySupported {
		writeJSON(w, http.StatusOK, BatchResponse{
			Status:          "failed",
			Results:         results,
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	if err := driver.Connect(req.ToConnectionParams()); err != nil {
		connErr := toExecuteError(err)
		for i := range results {
			if results[i].Status == "" {
				results[i].Status = "failed"
				results[i].Error = connErr
			}
		}
		writeJSON(w, http.StatusOK, BatchResponse{
			Status:          "failed",
			Results:         results,
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}
	defer func() {
		// Best-effort cleanup, same as every other handler -- a failure to
		// tear down cleanly shouldn't change the response already sent.
		_ = driver.Disconnect()
	}()

	connectedAt := time.Now().UTC().Format(time.RFC3339Nano)

	for i, op := range req.Operations {
		if results[i].Status == "failed" {
			continue // already marked unsupported before connecting
		}
		opStart := time.Now()
		runBatchOperation(driver, op, &results[i])
		results[i].DurationMs = time.Since(opStart).Milliseconds()
	}

	anyFailed := false
	allFailed := true
	for _, res := range results {
		if res.Status != "success" {
			anyFailed = true
		}
		if res.Status != "failed" {
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

	writeJSON(w, http.StatusOK, BatchResponse{
		Status:          status,
		Results:         results,
		ConnectedAt:     &connectedAt,
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	})
}

// batchOperationSupported checks the SAME capability interfaces the
// single-purpose handlers check, keyed by BatchOperation.Type string.
// "execute" always returns true -- RunCommands lives on CliDriver
// itself, every driver (including RawDriver) implements it.
func batchOperationSupported(driver drivers.CliDriver, opType string) bool {
	switch opType {
	case "facts":
		_, ok := driver.(drivers.FactsProvider)
		return ok
	case "interfaces":
		_, ok := driver.(drivers.InterfaceProvider)
		return ok
	case "lldp-neighbors":
		_, ok := driver.(drivers.LLDPNeighborsProvider)
		return ok
	case "config":
		_, ok := driver.(drivers.ConfigGetter)
		return ok
	case "execute":
		return true
	}
	return false
}

// runBatchOperation actually calls the driver method op.Type maps to,
// populating result's matching field (and only that field) plus its own
// Status/Error. Only ever called after batchOperationSupported already
// confirmed the driver satisfies the needed capability, so the type
// assertions here are never the ones that fail.
func runBatchOperation(driver drivers.CliDriver, op BatchOperation, result *BatchOperationResult) {
	switch op.Type {
	case "facts":
		facts, err := driver.(drivers.FactsProvider).GetFacts()
		if err != nil {
			result.Status = "failed"
			result.Error = toExecuteError(err)
			return
		}
		result.Status = "success"
		result.Facts = facts

	case "interfaces":
		interfaces, err := driver.(drivers.InterfaceProvider).GetInterfaces()
		if err != nil {
			result.Status = "failed"
			result.Error = toExecuteError(err)
			return
		}
		result.Status = "success"
		result.Interfaces = interfaces

	case "lldp-neighbors":
		neighbors, err := driver.(drivers.LLDPNeighborsProvider).GetLLDPNeighbors()
		if err != nil {
			result.Status = "failed"
			result.Error = toExecuteError(err)
			return
		}
		result.Status = "success"
		result.Neighbors = neighbors

	case "config":
		config, err := driver.(drivers.ConfigGetter).GetConfig()
		if err != nil {
			result.Status = "failed"
			result.Error = toExecuteError(err)
			return
		}
		result.Status = "success"
		result.Config = &config

	case "execute":
		cmdResults, err := driver.RunCommands(op.Commands)
		if err != nil {
			result.Status = "failed"
			result.Error = toExecuteError(err)
			return
		}
		result.Results = cmdResults
		anyCmdFailed := false
		allCmdsFailed := len(cmdResults) > 0
		for _, r := range cmdResults {
			if r.Error != "" {
				anyCmdFailed = true
			} else {
				allCmdsFailed = false
			}
		}
		switch {
		case allCmdsFailed:
			result.Status = "failed"
		case anyCmdFailed:
			result.Status = "partial"
		default:
			result.Status = "success"
		}
	}
}

func writeBatchValidationError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, BatchResponse{
		Status: "failed",
		Error:  &ExecuteError{Type: "validation", Message: msg},
	})
}
