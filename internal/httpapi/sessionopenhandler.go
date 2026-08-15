package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/onu-platform/gearctl/internal/drivers"
	"github.com/onu-platform/gearctl/internal/security"
)

// SessionOpenHandler dials a fresh connection exactly like every other
// handler's fresh-connection path, but instead of running one operation
// and closing it, registers it in sessionStore and returns a token --
// see SessionOpenResponse's doc comment. The connection this opens stays
// open until POST /v1/session/close, or until sessionIdleTimeout of no
// activity closes it automatically.
func SessionOpenHandler(w http.ResponseWriter, r *http.Request) {
	var req SessionOpenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSessionOpenValidationError(w, "invalid JSON body: "+err.Error())
		return
	}

	if msg := req.Validate(); msg != "" {
		writeSessionOpenValidationError(w, msg)
		return
	}

	if !security.IsAllowedOltTarget(req.Host) {
		writeSessionOpenValidationError(w, fmt.Sprintf("Target host %q is not allowed", req.Host))
		return
	}

	startedAt := time.Now()
	driver := drivers.Resolve(req.Vendor)

	if err := driver.Connect(req.ToConnectionParams()); err != nil {
		writeJSON(w, http.StatusOK, SessionOpenResponse{
			Status:          "failed",
			Error:           toExecuteError(err),
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	sess, err := sessionStore.Open(driver, driver.Vendor())
	if err != nil {
		// Registering the session failed (e.g. the store is shutting
		// down) -- the connection we just opened would otherwise leak,
		// so tear it down before reporting the error.
		_ = driver.Disconnect()
		writeJSON(w, http.StatusOK, SessionOpenResponse{
			Status:          "failed",
			Error:           &ExecuteError{Type: "protocol", Message: "failed to register session: " + err.Error()},
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	connectedAt := sess.ConnectedAt.UTC().Format(time.RFC3339Nano)
	writeJSON(w, http.StatusOK, SessionOpenResponse{
		Status:          "success",
		SessionID:       sess.ID,
		Vendor:          sess.Vendor,
		ConnectedAt:     &connectedAt,
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	})
}

func writeSessionOpenValidationError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, SessionOpenResponse{
		Status: "failed",
		Error:  &ExecuteError{Type: "validation", Message: msg},
	})
}
