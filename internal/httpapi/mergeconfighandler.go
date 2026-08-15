package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/onu-platform/gearctl/internal/drivers"
	"github.com/onu-platform/gearctl/internal/security"
)

// MergeConfigHandler is /v1/execute's counterpart for the ConfigLoader
// capability (see drivers.ConfigLoader and MergeConfigRequest's doc
// comments for the dry-run/hash-echo contract and merge-vs-replace
// semantics). A vendor without a config-push-capable driver gets a clear
// "unsupported" error, same convention as FactsHandler/ConfigHandler.
//
// Diff/ConfigHash are populated whenever LoadConfig computed them, even
// on a failed apply attempt (rejection, connection drop, stale hash) --
// they're computed before any write is attempted, so they're the record
// of what was being proposed regardless of outcome. Status reflects
// whether THIS CALL completed as expected (a dry run that successfully
// computed a diff is "success" even though nothing was written) --
// callers must check Applied, not Status, to know whether a device write
// actually happened.
func MergeConfigHandler(w http.ResponseWriter, r *http.Request) {
	var req MergeConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeMergeConfigValidationError(w, "invalid JSON body: "+err.Error())
		return
	}

	if msg := req.Validate(); msg != "" {
		writeMergeConfigValidationError(w, msg)
		return
	}

	startedAt := time.Now()

	if req.SessionID != "" {
		runMergeConfigOnSession(w, req.SessionID, req.Candidate, *req.DryRun, req.ExpectedConfigHash, startedAt)
		return
	}

	if !security.IsAllowedOltTarget(req.Host) {
		writeMergeConfigValidationError(w, fmt.Sprintf("Target host %q is not allowed", req.Host))
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
	diff, configHash, applied, err := configLoader.LoadConfig(req.Candidate, *req.DryRun, req.ExpectedConfigHash)

	resp := MergeConfigResponse{
		Applied:         applied,
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

// runMergeConfigOnSession is MergeConfigHandler's session-based path --
// see runFactsOnSession's doc comment for the general pattern. Config
// push is deliberately included here, not just read-only operations --
// unlike POST /v1/batch (which intentionally excludes it, see
// BatchRequest's doc comment: a merge's dry-run/confirm/apply flow
// doesn't compose with "run N things and report what happened"), a
// session-based call is still exactly one call in, one call out, same as
// any other -- there's no batch-wide outcome to reconcile a merge's
// separate dry-run-then-apply steps against.
func runMergeConfigOnSession(w http.ResponseWriter, sessionID, candidate string, dryRun bool, expectedConfigHash string, startedAt time.Time) {
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
	diff, configHash, applied, err := configLoader.LoadConfig(candidate, dryRun, expectedConfigHash)

	resp := MergeConfigResponse{
		Applied:         applied,
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

func writeMergeConfigValidationError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, MergeConfigResponse{
		Status: "failed",
		Error:  &ExecuteError{Type: "validation", Message: msg},
	})
}
