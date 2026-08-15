package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onu-platform/gearctl/internal/drivers"
)

func postConfig(t *testing.T, body string) (*httptest.ResponseRecorder, ConfigResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/config", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	ConfigHandler(rec, req)

	var decoded ConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode response body %q: %v", rec.Body.String(), err)
	}
	return rec, decoded
}

func TestConfigHandlerRejectsInvalidJSON(t *testing.T) {
	rec, resp := postConfig(t, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestConfigHandlerRejectsMissingHost(t *testing.T) {
	rec, resp := postConfig(t, `{
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

func TestConfigHandlerBlocksDisallowedTarget(t *testing.T) {
	rec, resp := postConfig(t, `{
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

// TestConfigHandlerReportsUnsupportedForVendorsWithoutConfigGetter exercises
// Resolve's fallback (RawDriver, for any vendor without a dedicated
// driver) -- confirms /v1/config fails fast with a distinguishable
// "unsupported" error instead of attempting a connection that could never
// produce a config. No network involved: the type assertion fails before
// Connect is ever called.
func TestConfigHandlerReportsUnsupportedForVendorsWithoutConfigGetter(t *testing.T) {
	rec, resp := postConfig(t, `{
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
	if resp.Config != nil {
		t.Errorf("got config %v, want nil", resp.Config)
	}
}

func TestConfigHandlerSessionNotFound(t *testing.T) {
	rec, resp := postConfig(t, `{"sessionId": "does-not-exist"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "session_not_found" {
		t.Errorf("got error %+v, want type=session_not_found", resp.Error)
	}
}

func TestConfigHandlerSucceedsViaSession(t *testing.T) {
	driver := &fakeSessionDriver{vendor: "cisco", config: "hostname R1\nend\n"}
	sessionID := openFakeSession(driver, "cisco")
	defer func() { _ = sessionStore.Close(sessionID) }()

	rec, resp := postConfig(t, fmt.Sprintf(`{"sessionId": %q}`, sessionID))
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	if resp.Status != "success" {
		t.Errorf("got status %q, want \"success\" (error: %+v)", resp.Status, resp.Error)
	}
	if resp.Config == nil || *resp.Config != "hostname R1\nend\n" {
		t.Errorf("got config %v, want the fake driver's config", resp.Config)
	}
}

func TestConfigHandlerReportsUnsupportedViaSession(t *testing.T) {
	driver := &drivers.RawDriver{}
	sessionID := openFakeSession(driver, "huawei")
	defer func() { _ = sessionStore.Close(sessionID) }()

	rec, resp := postConfig(t, fmt.Sprintf(`{"sessionId": %q}`, sessionID))
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "unsupported" {
		t.Errorf("got error %+v, want type=unsupported", resp.Error)
	}
}

// TestConfigHandlerReturnsFailedForConnectionError exercises the real
// CiscoIOSDriver/transport stack against 192.0.2.1 -- the TEST-NET-1 range
// (RFC 5737), reserved for documentation and guaranteed non-routable, so
// this deterministically times out at dial without touching any real
// network or device (same technique as facts/executehandler_test.go's
// equivalent tests).
func TestConfigHandlerReturnsFailedForConnectionError(t *testing.T) {
	rec, resp := postConfig(t, `{
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
	if resp.Config != nil {
		t.Errorf("got config %v, want nil", resp.Config)
	}
}
