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

	if !security.IsAllowedOltTarget(req.Host) {
		writeConfigValidationError(w, fmt.Sprintf("Target host %q is not allowed", req.Host))
		return
	}

	startedAt := time.Now()
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

func writeConfigValidationError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, ConfigResponse{
		Status: "failed",
		Error:  &ExecuteError{Type: "validation", Message: msg},
	})
}
