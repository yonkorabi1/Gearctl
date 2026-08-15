package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/onu-platform/gearctl/internal/drivers"
	"github.com/onu-platform/gearctl/internal/security"
)

// InterfacesHandler is /v1/execute's counterpart for the
// InterfaceProvider capability: same request shape minus `commands`, but
// instead of running caller-supplied commands it calls the resolved
// driver's GetInterfaces() (see drivers.InterfaceProvider) -- richer
// per-interface state (up/down, enabled, description, MTU, speed, MAC)
// than /v1/facts's bare interface_list. A vendor without an
// interfaces-capable driver gets a clear "unsupported" error rather than
// a silent fallback to raw output, same convention as FactsHandler.
func InterfacesHandler(w http.ResponseWriter, r *http.Request) {
	var req InterfacesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeInterfacesValidationError(w, "invalid JSON body: "+err.Error())
		return
	}

	if msg := req.Validate(); msg != "" {
		writeInterfacesValidationError(w, msg)
		return
	}

	if !security.IsAllowedOltTarget(req.Host) {
		writeInterfacesValidationError(w, fmt.Sprintf("Target host %q is not allowed", req.Host))
		return
	}

	startedAt := time.Now()
	driver := drivers.Resolve(req.Vendor)

	interfaceProvider, ok := driver.(drivers.InterfaceProvider)
	if !ok {
		writeJSON(w, http.StatusOK, InterfacesResponse{
			Status: "failed",
			Error: &ExecuteError{
				Type:    "unsupported",
				Message: fmt.Sprintf("vendor %q has no interfaces-capable driver", req.Vendor),
			},
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	if err := driver.Connect(req.ToConnectionParams()); err != nil {
		writeJSON(w, http.StatusOK, InterfacesResponse{
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
	interfaces, err := interfaceProvider.GetInterfaces()
	if err != nil {
		writeJSON(w, http.StatusOK, InterfacesResponse{
			Status:          "failed",
			Error:           toExecuteError(err),
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	writeJSON(w, http.StatusOK, InterfacesResponse{
		Status:          "success",
		Interfaces:      interfaces,
		ConnectedAt:     &connectedAt,
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	})
}

func writeInterfacesValidationError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, InterfacesResponse{
		Status: "failed",
		Error:  &ExecuteError{Type: "validation", Message: msg},
	})
}
