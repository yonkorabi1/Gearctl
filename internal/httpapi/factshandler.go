package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/onu-platform/gearctl/internal/drivers"
	"github.com/onu-platform/gearctl/internal/security"
)

// FactsHandler is /v1/execute's counterpart for the FactsProvider
// capability: same request shape minus `commands`, but instead of running
// caller-supplied commands it calls the resolved driver's GetFacts() (see
// drivers.FactsProvider). A vendor without a facts-capable driver yet
// (still the common case -- see drivers/registry.go) gets a clear
// "unsupported" error rather than a silent fallback to raw output, so a
// caller can tell "this vendor has no structured facts" apart from "the
// connection failed".
func FactsHandler(w http.ResponseWriter, r *http.Request) {
	var req FactsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeFactsValidationError(w, "invalid JSON body: "+err.Error())
		return
	}

	if msg := req.Validate(); msg != "" {
		writeFactsValidationError(w, msg)
		return
	}

	startedAt := time.Now()

	if req.SessionID != "" {
		runFactsOnSession(w, req.SessionID, startedAt)
		return
	}

	if !security.IsAllowedOltTarget(req.Host) {
		writeFactsValidationError(w, fmt.Sprintf("Target host %q is not allowed", req.Host))
		return
	}

	driver := drivers.Resolve(req.Vendor)

	factsProvider, ok := driver.(drivers.FactsProvider)
	if !ok {
		writeJSON(w, http.StatusOK, FactsResponse{
			Status: "failed",
			Error: &ExecuteError{
				Type:    "unsupported",
				Message: fmt.Sprintf("vendor %q has no facts-capable driver", req.Vendor),
			},
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	if err := driver.Connect(req.ToConnectionParams()); err != nil {
		writeJSON(w, http.StatusOK, FactsResponse{
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
	facts, err := factsProvider.GetFacts()
	if err != nil {
		writeJSON(w, http.StatusOK, FactsResponse{
			Status:          "failed",
			Error:           toExecuteError(err),
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	writeJSON(w, http.StatusOK, FactsResponse{
		Status:          "success",
		Facts:           facts,
		ConnectedAt:     &connectedAt,
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	})
}

// runFactsOnSession is FactsHandler's session-based path: instead of
// resolving a vendor and dialing a fresh connection, it acquires an
// already-open session from the pool and runs GetFacts() against its
// existing driver -- see acquireSession's doc comment for the
// lock/release contract.
func runFactsOnSession(w http.ResponseWriter, sessionID string, startedAt time.Time) {
	sess, sessErr := acquireSession(sessionID)
	if sessErr != nil {
		writeJSON(w, http.StatusOK, FactsResponse{
			Status:          "failed",
			Error:           sessErr,
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}
	defer sessionStore.Release(sess)

	factsProvider, ok := sess.Driver.(drivers.FactsProvider)
	if !ok {
		writeJSON(w, http.StatusOK, FactsResponse{
			Status: "failed",
			Error: &ExecuteError{
				Type:    "unsupported",
				Message: fmt.Sprintf("vendor %q has no facts-capable driver", sess.Vendor),
			},
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	connectedAt := sess.ConnectedAt.UTC().Format(time.RFC3339Nano)
	facts, err := factsProvider.GetFacts()
	if err != nil {
		writeJSON(w, http.StatusOK, FactsResponse{
			Status:          "failed",
			Error:           toExecuteError(err),
			ConnectedAt:     &connectedAt,
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	writeJSON(w, http.StatusOK, FactsResponse{
		Status:          "success",
		Facts:           facts,
		ConnectedAt:     &connectedAt,
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	})
}

func writeFactsValidationError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, FactsResponse{
		Status: "failed",
		Error:  &ExecuteError{Type: "validation", Message: msg},
	})
}
