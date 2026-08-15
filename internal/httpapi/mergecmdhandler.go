package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/onu-platform/gearctl/internal/drivers"
	"github.com/onu-platform/gearctl/internal/security"
)

// MergeCmdHandler is MergeConfigHandler's delta-based sibling (see
// drivers.ConfigLoader.LoadMergeCommands and MergeCmdRequest's doc
// comments). Same conventions as MergeConfigHandler: a vendor without a
// config-push-capable driver gets "unsupported"; Diff/ConfigHash/
// ReverseCommands are populated whenever LoadMergeCommands computed them
// (even on a failed apply attempt); Status reflects whether this call
// completed as expected, not whether a write happened -- check Applied
// for that.
func MergeCmdHandler(w http.ResponseWriter, r *http.Request) {
	var req MergeCmdRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeMergeCmdValidationError(w, "invalid JSON body: "+err.Error())
		return
	}

	if msg := req.Validate(); msg != "" {
		writeMergeCmdValidationError(w, msg)
		return
	}

	startedAt := time.Now()

	if req.SessionID != "" {
		runMergeCmdOnSession(w, req.SessionID, req.Commands, *req.DryRun, req.ExpectedConfigHash, startedAt)
		return
	}

	if !security.IsAllowedOltTarget(req.Host) {
		writeMergeCmdValidationError(w, fmt.Sprintf("Target host %q is not allowed", req.Host))
		return
	}

	driver := drivers.Resolve(req.Vendor)

	configLoader, ok := driver.(drivers.ConfigLoader)
	if !ok {
		writeJSON(w, http.StatusOK, MergeConfigResponse{
			Status: "failed",
			Error: &ExecuteError{
				Type:    "unsupported",
				Message: fmt.Sprintf("vendor %q has no config-push-capable driver", req.Vendor),
			},
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	if err := driver.Connect(req.ToConnectionParams()); err != nil {
		writeJSON(w, http.StatusOK, MergeConfigResponse{
			Status:          "failed",
			Error:           toExecuteError(err),
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}
	defer func() {
		// Best-effort cleanup, same as ExecuteHandler -- a failure to tear
		// down cleanly shouldn't change the response already sent.
		_ = driver.Disconnect()
	}()

	connectedAt := time.Now().UTC().Format(time.RFC3339Nano)
	diff, reverseCommands, configHash, applied, err := configLoader.LoadMergeCommands(req.Commands, *req.DryRun, req.ExpectedConfigHash)

	resp := MergeConfigResponse{
		Applied:         applied,
		ReverseCommands: reverseCommands,
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	}
	if diff != "" {
		resp.Diff = &diff
	}
	if configHash != "" {
		resp.ConfigHash = &configHash
	}

	if err != nil {
		resp.Status = "failed"
		resp.Error = toExecuteError(err)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.Status = "success"
	resp.ConnectedAt = &connectedAt
	writeJSON(w, http.StatusOK, resp)
}

// runMergeCmdOnSession is MergeCmdHandler's session-based path -- see
// runMergeConfigOnSession's doc comment for why config push is included
// here despite POST /v1/batch deliberately excluding it.
func runMergeCmdOnSession(w http.ResponseWriter, sessionID string, commands []string, dryRun bool, expectedConfigHash string, startedAt time.Time) {
	sess, sessErr := acquireSession(sessionID)
	if sessErr != nil {
		writeJSON(w, http.StatusOK, MergeConfigResponse{
			Status:          "failed",
			Error:           sessErr,
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}
	defer sessionStore.Release(sess)

	configLoader, ok := sess.Driver.(drivers.ConfigLoader)
	if !ok {
		writeJSON(w, http.StatusOK, MergeConfigResponse{
			Status: "failed",
			Error: &ExecuteError{
				Type:    "unsupported",
				Message: fmt.Sprintf("vendor %q has no config-push-capable driver", sess.Vendor),
			},
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	connectedAt := sess.ConnectedAt.UTC().Format(time.RFC3339Nano)
	diff, reverseCommands, configHash, applied, err := configLoader.LoadMergeCommands(commands, dryRun, expectedConfigHash)

	resp := MergeConfigResponse{
		Applied:         applied,
		ReverseCommands: reverseCommands,
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	}
	if diff != "" {
		resp.Diff = &diff
	}
	if configHash != "" {
		resp.ConfigHash = &configHash
	}

	if err != nil {
		resp.Status = "failed"
		resp.Error = toExecuteError(err)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.Status = "success"
	resp.ConnectedAt = &connectedAt
	writeJSON(w, http.StatusOK, resp)
}

func writeMergeCmdValidationError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, MergeConfigResponse{
		Status: "failed",
		Error:  &ExecuteError{Type: "validation", Message: msg},
	})
}
