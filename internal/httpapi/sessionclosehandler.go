package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/onu-platform/gearctl/internal/sessionpool"
)

// SessionCloseHandler disconnects and removes a pooled session
// immediately, rather than waiting for it to idle out. Closing an
// unknown/already-closed session is reported as session_not_found, not
// silently treated as success -- see SessionCloseRequest's doc comment.
func SessionCloseHandler(w http.ResponseWriter, r *http.Request) {
	var req SessionCloseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSessionCloseValidationError(w, "invalid JSON body: "+err.Error())
		return
	}

	if msg := req.Validate(); msg != "" {
		writeSessionCloseValidationError(w, msg)
		return
	}

	startedAt := time.Now()
	if err := sessionStore.Close(req.SessionID); err != nil {
		errType := "protocol"
		msg := err.Error()
		if errors.Is(err, sessionpool.ErrNotFound) {
			errType = "session_not_found"
			msg = "no open session with that id -- it may already be closed or have expired"
		}
		writeJSON(w, http.StatusOK, SessionCloseResponse{
			Status:          "failed",
			Error:           &ExecuteError{Type: errType, Message: msg},
			TotalDurationMs: time.Since(startedAt).Milliseconds(),
		})
		return
	}

	writeJSON(w, http.StatusOK, SessionCloseResponse{
		Status:          "success",
		TotalDurationMs: time.Since(startedAt).Milliseconds(),
	})
}

func writeSessionCloseValidationError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, SessionCloseResponse{
		Status: "failed",
		Error:  &ExecuteError{Type: "validation", Message: msg},
	})
}
