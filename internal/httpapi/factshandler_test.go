package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func postFacts(t *testing.T, body string) (*httptest.ResponseRecorder, FactsResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/facts", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	FactsHandler(rec, req)

	var decoded FactsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode response body %q: %v", rec.Body.String(), err)
	}
	return rec, decoded
}

func TestFactsHandlerRejectsInvalidJSON(t *testing.T) {
	rec, resp := postFacts(t, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestFactsHandlerRejectsMissingHost(t *testing.T) {
	rec, resp := postFacts(t, `{
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

func TestFactsHandlerBlocksDisallowedTarget(t *testing.T) {
	rec, resp := postFacts(t, `{
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

// TestFactsHandlerReportsUnsupportedForVendorsWithoutFactsProvider exercises
// Resolve's fallback (RawDriver, for any vendor without a dedicated
// driver) -- confirms /v1/facts fails fast with a distinguishable
// "unsupported" error instead of attempting a connection that could never
// produce facts. No network involved: the type assertion fails before
// Connect is ever called.
func TestFactsHandlerReportsUnsupportedForVendorsWithoutFactsProvider(t *testing.T) {
	rec, resp := postFacts(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"vendor": "huawei",
		"credentials": {"username": "a", "password": "b"}
	}`)
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200 (an unsupported vendor is still a well-formed response)", rec.Code)
	}
	if resp.Status != "failed" {
		t.Errorf("got status %q, want \"failed\"", resp.Status)
	}
	if resp.Error == nil || resp.Error.Type != "unsupported" {
		t.Errorf("got error %+v, want type=unsupported", resp.Error)
	}
	if resp.Facts != nil {
		t.Errorf("got facts %v, want nil", resp.Facts)
	}
}

// TestFactsHandlerReturnsFailedForConnectionError exercises the real
// CiscoIOSDriver/transport stack against 192.0.2.1 -- the TEST-NET-1 range
// (RFC 5737), reserved for documentation and guaranteed non-routable, so
// this deterministically times out at dial without touching any real
// network or device (same technique as executehandler_test.go's
// equivalent test).
func TestFactsHandlerReturnsFailedForConnectionError(t *testing.T) {
	rec, resp := postFacts(t, `{
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
	if resp.ConnectedAt != nil {
		t.Errorf("got connectedAt %v, want nil", resp.ConnectedAt)
	}
	if resp.Facts != nil {
		t.Errorf("got facts %v, want nil", resp.Facts)
	}
}
