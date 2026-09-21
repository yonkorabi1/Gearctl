package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func postBatch(t *testing.T, body string) (*httptest.ResponseRecorder, BatchResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/batch", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	BatchHandler(rec, req)

	var decoded BatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode response body %q: %v", rec.Body.String(), err)
	}
	return rec, decoded
}

func TestBatchHandlerRejectsInvalidJSON(t *testing.T) {
	rec, resp := postBatch(t, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestBatchHandlerRejectsMissingHost(t *testing.T) {
	rec, resp := postBatch(t, `{
		"port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"operations": [{"type": "facts"}]
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Message != "host is required" {
		t.Errorf("got error %+v, want message \"host is required\"", resp.Error)
	}
}

func TestBatchHandlerBlocksDisallowedTarget(t *testing.T) {
	rec, resp := postBatch(t, `{
		"host": "127.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"operations": [{"type": "facts"}]
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestBatchHandlerRejectsEmptyOperations(t *testing.T) {
	rec, resp := postBatch(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"operations": []
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Message != "operations must have at least 1 entry" {
		t.Errorf("got error %+v, want message about operations requiring at least 1 entry", resp.Error)
	}
}

func TestBatchHandlerRejectsUnknownOperationType(t *testing.T) {
	rec, resp := postBatch(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"operations": [{"type": "merge-cmd"}]
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestBatchHandlerRejectsExecuteWithoutCommands(t *testing.T) {
	rec, resp := postBatch(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"operations": [{"type": "execute"}]
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

// TestBatchHandlerReportsUnsupportedPerOperationNotWholeBatch confirms
// the core batch-specific behavior: a vendor without a driver capable of
// one requested operation gets ONLY that operation marked
// "unsupported" -- it doesn't refuse the whole request the way a
// single-purpose handler would. No network involved (RawDriver
// implements none of facts/interfaces/lldp-neighbors/config, so every
// operation here is unsupported and none needs a connection attempt).
func TestBatchHandlerReportsUnsupportedPerOperationNotWholeBatch(t *testing.T) {
	rec, resp := postBatch(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"vendor": "zte",
		"credentials": {"username": "a", "password": "b"},
		"operations": [{"type": "facts"}, {"type": "interfaces"}]
	}`)
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200 (unsupported operations are still a well-formed response)", rec.Code)
	}
	if resp.Status != "failed" {
		t.Errorf("got status %q, want \"failed\" (every operation unsupported)", resp.Status)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(resp.Results))
	}
	for i, want := range []string{"facts", "interfaces"} {
		if resp.Results[i].Type != want {
			t.Errorf("results[%d].Type = %q, want %q", i, resp.Results[i].Type, want)
		}
		if resp.Results[i].Error == nil || resp.Results[i].Error.Type != "unsupported" {
			t.Errorf("results[%d].Error = %+v, want type=unsupported", i, resp.Results[i].Error)
		}
	}
}

// TestBatchHandlerReturnsFailedForConnectionError exercises the real
// CiscoIOSDriver/transport stack against 192.0.2.1 (TEST-NET-1, RFC
// 5737, guaranteed non-routable) -- same technique as the other handler
// tests. cisco implements every batchable capability, so this actually
// reaches the connect step and confirms a connection failure marks every
// (supported) operation "failed" with the same connection error.
func TestBatchHandlerReturnsFailedForConnectionError(t *testing.T) {
	rec, resp := postBatch(t, `{
		"host": "192.0.2.1", "port": 22, "protocol": "ssh",
		"vendor": "cisco",
		"credentials": {"username": "a", "password": "b"},
		"connectTimeoutMs": 300,
		"perCommandTimeoutMs": 300,
		"operations": [{"type": "facts"}, {"type": "config"}]
	}`)
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200 (a connection failure is still a well-formed response)", rec.Code)
	}
	if resp.Status != "failed" {
		t.Errorf("got status %q, want \"failed\"", resp.Status)
	}
	if resp.ConnectedAt != nil {
		t.Errorf("got connectedAt %v, want nil", resp.ConnectedAt)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(resp.Results))
	}
	for i, want := range []string{"facts", "config"} {
		if resp.Results[i].Type != want {
			t.Errorf("results[%d].Type = %q, want %q", i, resp.Results[i].Type, want)
		}
		if resp.Results[i].Error == nil || resp.Results[i].Error.Type != "connection" {
			t.Errorf("results[%d].Error = %+v, want type=connection", i, resp.Results[i].Error)
		}
	}
}
