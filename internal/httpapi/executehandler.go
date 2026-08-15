package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/onu-platform/gearctl/internal/drivers"
	"github.com/onu-platform/gearctl/internal/olterr"
	"github.com/onu-platform/gearctl/internal/security"
	"github.com/onu-platform/gearctl/internal/transport"
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

	startedAt := time.Now()

	if req.SessionID != "" {
		runExecuteOnSession(w, req.SessionID, req.Commands, startedAt)
		return
	}

	if !security.IsAllowedOltTarget(req.Host) {
		writeValidationError(w, fmt.Sprintf("Target host %q is not allowed", req.Host))
		return
	}

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

	writeJSON(w, http.StatusOK, ExecuteResponse{
		Status:          commandsResultStatus(results),
		Results:         results,
		Error:           nil,
		ConnectedAt:     &connectedAt,
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	})
}

// runExecuteOnSession is ExecuteHandler's session-based path -- see
// runFactsOnSession's doc comment for the general pattern. No
// vendor-capability check here, unlike the other session-path
// functions: RunCommands is on the base CliDriver interface every
// resolved driver implements, so there's nothing to be "unsupported".
func runExecuteOnSession(w http.ResponseWriter, sessionID string, commands []string, startedAt time.Time) {
	sess, sessErr := acquireSession(sessionID)
	if sessErr != nil {
		writeJSON(w, http.StatusOK, ExecuteResponse{
			Status:          "failed",
			Results:         []transport.CommandResult{},
			Error:           sessErr,
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}
	defer sessionStore.Release(sess)

	connectedAt := sess.ConnectedAt.UTC().Format(time.RFC3339Nano)
	results, err := sess.Driver.RunCommands(commands)
	if err != nil {
		writeJSON(w, http.StatusOK, ExecuteResponse{
			Status:          "failed",
			Results:         []transport.CommandResult{},
			Error:           toExecuteError(err),
			ConnectedAt:     &connectedAt,
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	writeJSON(w, http.StatusOK, ExecuteResponse{
		Status:          commandsResultStatus(results),
		Results:         results,
		Error:           nil,
		ConnectedAt:     &connectedAt,
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	})
}

// commandsResultStatus is /v1/execute's "some commands failed"
// convention, shared by both the fresh-connection and session-based
// paths: "success" if every command succeeded, "partial" if some did and
// some didn't, "failed" if none did (matches JS's Array.prototype.every
// on an empty array -- allFailed starts true).
func commandsResultStatus(results []transport.CommandResult) string {
	anyFailed := false
	allFailed := true
	for _, res := range results {
		if res.Error != "" {
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
