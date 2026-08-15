package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func postInterfaces(t *testing.T, body string) (*httptest.ResponseRecorder, InterfacesResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/interfaces", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	InterfacesHandler(rec, req)

	var decoded InterfacesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode response body %q: %v", rec.Body.String(), err)
	}
	return rec, decoded
}

func TestInterfacesHandlerRejectsInvalidJSON(t *testing.T) {
	rec, resp := postInterfaces(t, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestInterfacesHandlerRejectsMissingHost(t *testing.T) {
	rec, resp := postInterfaces(t, `{
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

func TestInterfacesHandlerBlocksDisallowedTarget(t *testing.T) {
	rec, resp := postInterfaces(t, `{
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

// TestInterfacesHandlerReportsUnsupportedForVendorsWithoutInterfaceProvider
// exercises Resolve's fallback (RawDriver) -- confirms /v1/interfaces
// fails fast with a distinguishable "unsupported" error instead of
// attempting a connection. No network involved.
func TestInterfacesHandlerReportsUnsupportedForVendorsWithoutInterfaceProvider(t *testing.T) {
	rec, resp := postInterfaces(t, `{
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
	if resp.Interfaces != nil {
		t.Errorf("got interfaces %v, want nil", resp.Interfaces)
	}
}

// TestInterfacesHandlerReturnsFailedForConnectionError exercises the real
// CiscoIOSDriver/transport stack against 192.0.2.1 (TEST-NET-1, RFC 5737,
// guaranteed non-routable) -- same technique as the other handler tests.
func TestInterfacesHandlerReturnsFailedForConnectionError(t *testing.T) {
	rec, resp := postInterfaces(t, `{
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
	if resp.Interfaces != nil {
		t.Errorf("got interfaces %v, want nil", resp.Interfaces)
	}
}
