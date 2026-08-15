package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func postSessionOpen(t *testing.T, body string) (*httptest.ResponseRecorder, SessionOpenResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/session/open", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	SessionOpenHandler(rec, req)

	var decoded SessionOpenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode response body %q: %v", rec.Body.String(), err)
	}
	return rec, decoded
}

func TestSessionOpenHandlerRejectsInvalidJSON(t *testing.T) {
	rec, resp := postSessionOpen(t, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestSessionOpenHandlerRejectsMissingHost(t *testing.T) {
	rec, resp := postSessionOpen(t, `{
		"port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"}
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Message != "host is required" {
		t.Errorf("got error %+v, want message \"host is required\"", resp.Error)
	}
}

// TestSessionOpenHandlerRejectsSessionIDSet confirms opening a session
// with a sessionId already set (which makes no sense -- there's nothing
// to reuse yet) is a clear validation error, not silently ignored.
func TestSessionOpenHandlerRejectsSessionIDSet(t *testing.T) {
	rec, resp := postSessionOpen(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"sessionId": "some-id"
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Message != "sessionId must not be set when opening a new session" {
		t.Errorf("got error %+v, want the sessionId-must-not-be-set message", resp.Error)
	}
}

func TestSessionOpenHandlerBlocksDisallowedTarget(t *testing.T) {
	rec, resp := postSessionOpen(t, `{
		"host": "127.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"}
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

// TestSessionOpenHandlerReturnsFailedForConnectionError exercises the
// real CiscoIOSDriver/transport stack against 192.0.2.1 (TEST-NET-1, RFC
// 5737, guaranteed non-routable) -- same technique as every other
// handler test in this package. Also confirms a failed open never
// registers a session -- sessionStore.Len() unchanged.
func TestSessionOpenHandlerReturnsFailedForConnectionError(t *testing.T) {
	before := sessionStore.Len()
	rec, resp := postSessionOpen(t, `{
		"host": "192.0.2.1", "port": 22, "protocol": "ssh",
		"vendor": "cisco",
		"credentials": {"username": "a", "password": "b"},
		"connectTimeoutMs": 300,
		"perCommandTimeoutMs": 300
	}`)
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200 (a connection failure is still a well-formed response)", rec.Code)
	}
	if resp.Status != "failed" {
		t.Errorf("got status %q, want \"failed\"", resp.Status)
	}
	if resp.Error == nil || resp.Error.Type != "connection" {
		t.Errorf("got error %+v, want type=connection", resp.Error)
	}
	if resp.SessionID != "" {
		t.Errorf("got sessionId %q, want empty", resp.SessionID)
	}
	if sessionStore.Len() != before {
		t.Errorf("sessionStore.Len() = %d, want unchanged at %d -- a failed open must not register a session", sessionStore.Len(), before)
	}
}
