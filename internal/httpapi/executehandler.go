package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/onu-platform/olt-cli-gateway/internal/drivers"
	"github.com/onu-platform/olt-cli-gateway/internal/olterr"
	"github.com/onu-platform/olt-cli-gateway/internal/security"
	"github.com/onu-platform/olt-cli-gateway/internal/transport"
)

func ExecuteHandler(w http.ResponseWriter, r *http.Request) {
	var req ExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body: "+err.Error())
		return
	}

	if msg := req.Validate(); msg != "" {
		writeValidationError(w, msg)
		return
	}

	if !security.IsAllowedOltTarget(req.Host) {
		writeValidationError(w, fmt.Sprintf("Target host %q is not allowed", req.Host))
		return
	}

	startedAt := time.Now()
	params := req.ToConnectionParams()
	driver := drivers.Resolve(req.Vendor)

	if err := driver.Connect(params); err != nil {
		writeJSON(w, http.StatusOK, ExecuteResponse{
			Status:          "failed",
			Results:         []transport.CommandResult{},
			Error:           toExecuteError(err),
			ConnectedAt:     nil,
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}
	defer func() {
		// Best-effort cleanup -- a failure to tear down cleanly shouldn't
		// change the response already sent to the caller.
		_ = driver.Disconnect()
	}()

	connectedAt := time.Now().UTC().Format(time.RFC3339Nano)
	results, err := driver.RunCommands(req.Commands)
	if err != nil {
		writeJSON(w, http.StatusOK, ExecuteResponse{
			Status:          "failed",
			Results:         []transport.CommandResult{},
			Error:           toExecuteError(err),
			ConnectedAt:     nil,
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	anyFailed := false
	allFailed := true // matches JS Array.prototype.every on an empty array
	for _, res := range results {
		if res.Error != "" {
			anyFailed = true
		} else {
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

	writeJSON(w, http.StatusOK, ExecuteResponse{
		Status:          status,
		Results:         results,
		Error:           nil,
		ConnectedAt:     &connectedAt,
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	})
}

func toExecuteError(err error) *ExecuteError {
	if cliErr, ok := err.(*olterr.OltCliError); ok {
		return &ExecuteError{Type: string(cliErr.ErrType), Message: cliErr.Msg}
	}
	return &ExecuteError{Type: "connection", Message: err.Error()}
}

func writeValidationError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, ExecuteResponse{
		Status:          "failed",
		Results:         []transport.CommandResult{},
		Error:           &ExecuteError{Type: "validation", Message: msg},
		ConnectedAt:     nil,
		TotalDurationMs: 0,
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
