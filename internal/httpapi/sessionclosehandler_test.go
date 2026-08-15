package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func postSessionClose(t *testing.T, body string) (*httptest.ResponseRecorder, SessionCloseResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/session/close", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	SessionCloseHandler(rec, req)

	var decoded SessionCloseResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode response body %q: %v", rec.Body.String(), err)
	}
	return rec, decoded
}

func TestSessionCloseHandlerRejectsInvalidJSON(t *testing.T) {
	rec, resp := postSessionClose(t, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestSessionCloseHandlerRejectsMissingSessionID(t *testing.T) {
	rec, resp := postSessionClose(t, `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Message != "sessionId is required" {
		t.Errorf("got error %+v, want message \"sessionId is required\"", resp.Error)
	}
}

func TestSessionCloseHandlerReportsNotFoundForUnknownSession(t *testing.T) {
	rec, resp := postSessionClose(t, `{"sessionId": "does-not-exist"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200 (an unknown session is still a well-formed response)", rec.Code)
	}
	if resp.Status != "failed" {
		t.Errorf("got status %q, want \"failed\"", resp.Status)
	}
	if resp.Error == nil || resp.Error.Type != "session_not_found" {
		t.Errorf("got error %+v, want type=session_not_found", resp.Error)
	}
}

// TestSessionCloseHandlerClosesAnOpenSession confirms a real close: the
// driver is disconnected, and the session is genuinely gone afterward
// (a second close on the same id reports session_not_found, not success
// again).
func TestSessionCloseHandlerClosesAnOpenSession(t *testing.T) {
	driver := &fakeSessionDriver{vendor: "cisco"}
	sessionID := openFakeSession(driver, "cisco")

	rec, resp := postSessionClose(t, fmt.Sprintf(`{"sessionId": %q}`, sessionID))
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	if resp.Status != "success" {
		t.Errorf("got status %q, want \"success\" (error: %+v)", resp.Status, resp.Error)
	}
	if driver.disconnectCount != 1 {
		t.Errorf("driver.disconnectCount = %d, want 1", driver.disconnectCount)
	}

	// Closing again must report not-found, not success.
	rec2, resp2 := postSessionClose(t, fmt.Sprintf(`{"sessionId": %q}`, sessionID))
	if rec2.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec2.Code)
	}
	if resp2.Error == nil || resp2.Error.Type != "session_not_found" {
		t.Errorf("second close: got error %+v, want type=session_not_found", resp2.Error)
	}
}
