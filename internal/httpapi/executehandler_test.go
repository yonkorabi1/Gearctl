package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func postExecute(t *testing.T, body string) (*httptest.ResponseRecorder, ExecuteResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	ExecuteHandler(rec, req)

	var decoded ExecuteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode response body %q: %v", rec.Body.String(), err)
	}
	return rec, decoded
}

func TestExecuteHandlerRejectsInvalidJSON(t *testing.T) {
	rec, resp := postExecute(t, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestExecuteHandlerRejectsMissingHost(t *testing.T) {
	rec, resp := postExecute(t, `{
		"port": 23, "protocol": "telnet",
		"credentials": {"username": "a", "password": "b"},
		"commands": ["show version"]
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Message != "host is required" {
		t.Errorf("got error %+v, want message \"host is required\"", resp.Error)
	}
}

func TestExecuteHandlerRejectsBadProtocol(t *testing.T) {
	_, resp := postExecute(t, `{
		"host": "10.0.0.1", "port": 23, "protocol": "ftp",
		"credentials": {"username": "a", "password": "b"},
		"commands": ["show version"]
	}`)
	if resp.Error == nil || resp.Error.Message != `protocol must be "ssh" or "telnet"` {
		t.Errorf("got error %+v", resp.Error)
	}
}

func TestExecuteHandlerRejectsEmptyCommands(t *testing.T) {
	_, resp := postExecute(t, `{
		"host": "10.0.0.1", "port": 23, "protocol": "telnet",
		"credentials": {"username": "a", "password": "b"},
		"commands": []
	}`)
	if resp.Error == nil || resp.Error.Message != "commands must have at least 1 entry" {
		t.Errorf("got error %+v", resp.Error)
	}
}

func TestExecuteHandlerRejectsTooManyCommands(t *testing.T) {
	commands := make([]string, 51)
	for i := range commands {
		commands[i] = "\"cmd\""
	}
	body := `{
		"host": "10.0.0.1", "port": 23, "protocol": "telnet",
		"credentials": {"username": "a", "password": "b"},
		"commands": [` + joinStrings(commands, ",") + `]
	}`
	_, resp := postExecute(t, body)
	if resp.Error == nil || resp.Error.Message != "commands must have at most 50 entries" {
		t.Errorf("got error %+v", resp.Error)
	}
}

func TestExecuteHandlerRejectsOutOfRangeTimeouts(t *testing.T) {
	_, resp := postExecute(t, `{
		"host": "10.0.0.1", "port": 23, "protocol": "telnet",
		"credentials": {"username": "a", "password": "b"},
		"commands": ["show version"],
		"connectTimeoutMs": 999999
	}`)
	if resp.Error == nil || resp.Error.Message != "connectTimeoutMs must be between 1 and 60000" {
		t.Errorf("got error %+v", resp.Error)
	}
}

func TestExecuteHandlerBlocksDisallowedTarget(t *testing.T) {
	rec, resp := postExecute(t, `{
		"host": "127.0.0.1", "port": 23, "protocol": "telnet",
		"credentials": {"username": "a", "password": "b"},
		"commands": ["show version"]
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

// TestExecuteHandlerReturnsFailedForConnectionError exercises the real
// RawDriver/transport stack against 192.0.2.1 -- the TEST-NET-1 range
// (RFC 5737), reserved for documentation and guaranteed non-routable, so
// this deterministically times out at dial without touching any real
// network or device. A short connect timeout keeps the test fast.
func TestExecuteHandlerReturnsFailedForConnectionError(t *testing.T) {
	rec, resp := postExecute(t, `{
		"host": "192.0.2.1", "port": 9, "protocol": "telnet",
		"credentials": {"username": "a", "password": "b"},
		"commands": ["show version"],
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
	if len(resp.Results) != 0 {
		t.Errorf("got %d results, want 0", len(resp.Results))
	}
}

func joinStrings(items []string, sep string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
