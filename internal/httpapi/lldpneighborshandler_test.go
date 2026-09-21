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

func postLLDPNeighbors(t *testing.T, body string) (*httptest.ResponseRecorder, LLDPNeighborsResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/lldp-neighbors", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	LLDPNeighborsHandler(rec, req)

	var decoded LLDPNeighborsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode response body %q: %v", rec.Body.String(), err)
	}
	return rec, decoded
}

func TestLLDPNeighborsHandlerRejectsInvalidJSON(t *testing.T) {
	rec, resp := postLLDPNeighbors(t, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestLLDPNeighborsHandlerRejectsMissingHost(t *testing.T) {
	rec, resp := postLLDPNeighbors(t, `{
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

func TestLLDPNeighborsHandlerBlocksDisallowedTarget(t *testing.T) {
	rec, resp := postLLDPNeighbors(t, `{
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

// TestLLDPNeighborsHandlerReportsUnsupportedForVendorsWithoutLLDPProvider
// exercises Resolve's fallback (RawDriver) -- confirms /v1/lldp-neighbors
// fails fast with a distinguishable "unsupported" error instead of
// attempting a connection. No network involved.
func TestLLDPNeighborsHandlerReportsUnsupportedForVendorsWithoutLLDPProvider(t *testing.T) {
	rec, resp := postLLDPNeighbors(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"vendor": "zte",
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
	if resp.Neighbors != nil {
		t.Errorf("got neighbors %v, want nil", resp.Neighbors)
	}
}

func TestLLDPNeighborsHandlerSessionNotFound(t *testing.T) {
	rec, resp := postLLDPNeighbors(t, `{"sessionId": "does-not-exist"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "session_not_found" {
		t.Errorf("got error %+v, want type=session_not_found", resp.Error)
	}
}

func TestLLDPNeighborsHandlerSucceedsViaSession(t *testing.T) {
	driver := &fakeSessionDriver{vendor: "cisco", neighbors: map[string][]drivers.LLDPNeighbor{
		"Gi1": {{RemoteSystemName: "switch1", RemotePortID: "Gi0/1"}},
	}}
	sessionID := openFakeSession(driver, "cisco")
	defer func() { _ = sessionStore.Close(sessionID) }()

	rec, resp := postLLDPNeighbors(t, fmt.Sprintf(`{"sessionId": %q}`, sessionID))
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	if resp.Status != "success" {
		t.Errorf("got status %q, want \"success\" (error: %+v)", resp.Status, resp.Error)
	}
	if len(resp.Neighbors["Gi1"]) != 1 || resp.Neighbors["Gi1"][0].RemoteSystemName != "switch1" {
		t.Errorf("got neighbors %+v, want Gi1 -> switch1", resp.Neighbors)
	}
}

func TestLLDPNeighborsHandlerReportsUnsupportedViaSession(t *testing.T) {
	driver := &drivers.RawDriver{}
	sessionID := openFakeSession(driver, "zte")
	defer func() { _ = sessionStore.Close(sessionID) }()

	rec, resp := postLLDPNeighbors(t, fmt.Sprintf(`{"sessionId": %q}`, sessionID))
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "unsupported" {
		t.Errorf("got error %+v, want type=unsupported", resp.Error)
	}
}

// TestLLDPNeighborsHandlerReturnsFailedForConnectionError exercises the
// real CiscoIOSDriver/transport stack against 192.0.2.1 (TEST-NET-1, RFC
// 5737, guaranteed non-routable) -- same technique as the other handler
// tests.
func TestLLDPNeighborsHandlerReturnsFailedForConnectionError(t *testing.T) {
	rec, resp := postLLDPNeighbors(t, `{
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
	if resp.Neighbors != nil {
		t.Errorf("got neighbors %v, want nil", resp.Neighbors)
	}
}
