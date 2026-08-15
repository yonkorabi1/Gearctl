package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/onu-platform/gearctl/internal/drivers"
	"github.com/onu-platform/gearctl/internal/security"
)

// LLDPNeighborsHandler is /v1/execute's counterpart for the
// LLDPNeighborsProvider capability: same request shape minus `commands`,
// but instead of running caller-supplied commands it calls the resolved
// driver's GetLLDPNeighbors() (see drivers.LLDPNeighborsProvider) --
// topology discovery (which local port sees which remote device/port).
// A vendor without an LLDP-capable driver gets a clear "unsupported"
// error rather than a silent fallback to raw output, same convention as
// FactsHandler/InterfacesHandler.
func LLDPNeighborsHandler(w http.ResponseWriter, r *http.Request) {
	var req LLDPNeighborsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeLLDPNeighborsValidationError(w, "invalid JSON body: "+err.Error())
		return
	}

	if msg := req.Validate(); msg != "" {
		writeLLDPNeighborsValidationError(w, msg)
		return
	}

	if !security.IsAllowedOltTarget(req.Host) {
		writeLLDPNeighborsValidationError(w, fmt.Sprintf("Target host %q is not allowed", req.Host))
		return
	}

	startedAt := time.Now()
	driver := drivers.Resolve(req.Vendor)

	lldpProvider, ok := driver.(drivers.LLDPNeighborsProvider)
	if !ok {
		writeJSON(w, http.StatusOK, LLDPNeighborsResponse{
			Status: "failed",
			Error: &ExecuteError{
				Type:    "unsupported",
				Message: fmt.Sprintf("vendor %q has no LLDP-capable driver", req.Vendor),
			},
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	if err := driver.Connect(req.ToConnectionParams()); err != nil {
		writeJSON(w, http.StatusOK, LLDPNeighborsResponse{
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
	neighbors, err := lldpProvider.GetLLDPNeighbors()
	if err != nil {
		writeJSON(w, http.StatusOK, LLDPNeighborsResponse{
			Status:          "failed",
			Error:           toExecuteError(err),
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	writeJSON(w, http.StatusOK, LLDPNeighborsResponse{
		Status:          "success",
		Neighbors:       neighbors,
		ConnectedAt:     &connectedAt,
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	})
}

func writeLLDPNeighborsValidationError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, LLDPNeighborsResponse{
		Status: "failed",
		Error:  &ExecuteError{Type: "validation", Message: msg},
	})
}
