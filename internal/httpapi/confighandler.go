package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/onu-platform/gearctl/internal/drivers"
	"github.com/onu-platform/gearctl/internal/security"
)

// ConfigHandler is /v1/execute's counterpart for the ConfigGetter
// capability: same request shape minus `commands`, but instead of running
// caller-supplied commands it calls the resolved driver's GetConfig()
// (see drivers.ConfigGetter) and returns the device's running
// configuration verbatim. A vendor without a config-capable driver gets a
// clear "unsupported" error, same convention as FactsHandler.
//
// This is deliberately read-only. Config push (drivers.ConfigLoader) is a
// separate, higher-risk capability exposed via POST /v1/merge-config
// (mergeconfighandler.go) -- named "merge", not "config-push" or
// "load-config", because it's a merge/overlay onto the existing config,
// not a replace.
func ConfigHandler(w http.ResponseWriter, r *http.Request) {
	var req ConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeConfigValidationError(w, "invalid JSON body: "+err.Error())
		return
	}

	if msg := req.Validate(); msg != "" {
		writeConfigValidationError(w, msg)
		return
	}

	startedAt := time.Now()

	if req.SessionID != "" {
		runConfigOnSession(w, req.SessionID, startedAt)
		return
	}

	if !security.IsAllowedOltTarget(req.Host) {
		writeConfigValidationError(w, fmt.Sprintf("Target host %q is not allowed", req.Host))
		return
	}

	driver := drivers.Resolve(req.Vendor)

	configGetter, ok := driver.(drivers.ConfigGetter)
	if !ok {
		writeJSON(w, http.StatusOK, ConfigResponse{
			Status: "failed",
			Error: &ExecuteError{
				Type:    "unsupported",
				Message: fmt.Sprintf("vendor %q has no config-capable driver", req.Vendor),
			},
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	if err := driver.Connect(req.ToConnectionParams()); err != nil {
		writeJSON(w, http.StatusOK, ConfigResponse{
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
	config, err := configGetter.GetConfig()
	if err != nil {
		writeJSON(w, http.StatusOK, ConfigResponse{
			Status:          "failed",
			Error:           toExecuteError(err),
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	writeJSON(w, http.StatusOK, ConfigResponse{
		Status:          "success",
		Config:          &config,
		ConnectedAt:     &connectedAt,
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	})
}

// runConfigOnSession is ConfigHandler's session-based path -- see
// runFactsOnSession's doc comment for the general pattern.
func runConfigOnSession(w http.ResponseWriter, sessionID string, startedAt time.Time) {
	sess, sessErr := acquireSession(sessionID)
	if sessErr != nil {
		writeJSON(w, http.StatusOK, ConfigResponse{
			Status:          "failed",
			Error:           sessErr,
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}
	defer sessionStore.Release(sess)

	configGetter, ok := sess.Driver.(drivers.ConfigGetter)
	if !ok {
		writeJSON(w, http.StatusOK, ConfigResponse{
			Status: "failed",
			Error: &ExecuteError{
				Type:    "unsupported",
				Message: fmt.Sprintf("vendor %q has no config-capable driver", sess.Vendor),
			},
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	connectedAt := sess.ConnectedAt.UTC().Format(time.RFC3339Nano)
	config, err := configGetter.GetConfig()
	if err != nil {
		writeJSON(w, http.StatusOK, ConfigResponse{
			Status:          "failed",
			Error:           toExecuteError(err),
			ConnectedAt:     &connectedAt,
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	writeJSON(w, http.StatusOK, ConfigResponse{
		Status:          "success",
		Config:          &config,
		ConnectedAt:     &connectedAt,
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	})
}

func writeConfigValidationError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, ConfigResponse{
		Status: "failed",
		Error:  &ExecuteError{Type: "validation", Message: msg},
	})
}
