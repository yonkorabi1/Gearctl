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

func postMergeCmd(t *testing.T, body string) (*httptest.ResponseRecorder, MergeConfigResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/merge-cmd", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	MergeCmdHandler(rec, req)

	var decoded MergeConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode response body %q: %v", rec.Body.String(), err)
	}
	return rec, decoded
}

func TestMergeCmdHandlerRejectsInvalidJSON(t *testing.T) {
	rec, resp := postMergeCmd(t, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestMergeCmdHandlerRejectsMissingHost(t *testing.T) {
	rec, resp := postMergeCmd(t, `{
		"port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"commands": ["hostname R2"], "dryRun": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Message != "host is required" {
		t.Errorf("got error %+v, want message \"host is required\"", resp.Error)
	}
}

func TestMergeCmdHandlerRejectsMissingCommands(t *testing.T) {
	rec, resp := postMergeCmd(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"dryRun": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Message != "commands must have at least 1 entry" {
		t.Errorf("got error %+v, want message \"commands must have at least 1 entry\"", resp.Error)
	}
}

func TestMergeCmdHandlerRejectsEmptyCommandEntry(t *testing.T) {
	rec, resp := postMergeCmd(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"commands": ["hostname R2", ""], "dryRun": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Message != "commands entries must not be empty" {
		t.Errorf("got error %+v, want message \"commands entries must not be empty\"", resp.Error)
	}
}

// TestMergeCmdHandlerRequiresExplicitDryRun confirms omitting dryRun
// entirely is a validation error, not an implicit default -- same "no
// silent default" safety property as MergeConfigHandler's.
func TestMergeCmdHandlerRequiresExplicitDryRun(t *testing.T) {
	rec, resp := postMergeCmd(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"commands": ["hostname R2"]
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Message != "dryRun is required (true or false) -- there is no implicit default for a config-push feature" {
		t.Errorf("got error %+v, want the required-dryRun message", resp.Error)
	}
}

func TestMergeCmdHandlerRequiresExpectedConfigHashWhenApplying(t *testing.T) {
	rec, resp := postMergeCmd(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"commands": ["hostname R2"], "dryRun": false
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestMergeCmdHandlerBlocksDisallowedTarget(t *testing.T) {
	rec, resp := postMergeCmd(t, `{
		"host": "127.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"commands": ["hostname R2"], "dryRun": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

// TestMergeCmdHandlerReportsUnsupportedForVendorsWithoutConfigLoader
// exercises Resolve's fallback (RawDriver) -- confirms /v1/merge-cmd
// fails fast with a distinguishable "unsupported" error instead of
// attempting a connection. No network involved.
func TestMergeCmdHandlerReportsUnsupportedForVendorsWithoutConfigLoader(t *testing.T) {
	rec, resp := postMergeCmd(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"vendor": "zte",
		"credentials": {"username": "a", "password": "b"},
		"commands": ["hostname R2"], "dryRun": true
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
	if resp.Applied {
		t.Error("applied = true, want false")
	}
}

func TestMergeCmdHandlerSessionNotFound(t *testing.T) {
	rec, resp := postMergeCmd(t, `{"sessionId": "does-not-exist", "commands": ["hostname R2"], "dryRun": true}`)
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "session_not_found" {
		t.Errorf("got error %+v, want type=session_not_found", resp.Error)
	}
}

func TestMergeCmdHandlerSucceedsViaSession(t *testing.T) {
	driver := &fakeSessionDriver{
		vendor: "cisco", diff: "+hostname R2", configHash: "abc123",
		reverseCommands: []string{"hostname R1"},
	}
	sessionID := openFakeSession(driver, "cisco")
	defer func() { _ = sessionStore.Close(sessionID) }()

	rec, resp := postMergeCmd(t, fmt.Sprintf(`{"sessionId": %q, "commands": ["hostname R2"], "dryRun": true}`, sessionID))
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	if resp.Status != "success" {
		t.Errorf("got status %q, want \"success\" (error: %+v)", resp.Status, resp.Error)
	}
	if len(resp.ReverseCommands) != 1 || resp.ReverseCommands[0] != "hostname R1" {
		t.Errorf("got reverseCommands %v, want [\"hostname R1\"]", resp.ReverseCommands)
	}
}

func TestMergeCmdHandlerReportsUnsupportedViaSession(t *testing.T) {
	driver := &drivers.RawDriver{}
	sessionID := openFakeSession(driver, "zte")
	defer func() { _ = sessionStore.Close(sessionID) }()

	rec, resp := postMergeCmd(t, fmt.Sprintf(`{"sessionId": %q, "commands": ["hostname R2"], "dryRun": true}`, sessionID))
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "unsupported" {
		t.Errorf("got error %+v, want type=unsupported", resp.Error)
	}
}

// TestMergeCmdHandlerReturnsFailedForConnectionError exercises the real
// CiscoIOSDriver/transport stack against 192.0.2.1 (TEST-NET-1, RFC 5737,
// guaranteed non-routable) -- same technique as the other handler tests.
func TestMergeCmdHandlerReturnsFailedForConnectionError(t *testing.T) {
	rec, resp := postMergeCmd(t, `{
		"host": "192.0.2.1", "port": 22, "protocol": "ssh",
		"vendor": "cisco",
		"credentials": {"username": "a", "password": "b"},
		"commands": ["hostname R2"], "dryRun": true,
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
	if resp.Applied {
		t.Error("applied = true, want false")
	}
	if resp.Diff != nil {
		t.Errorf("got diff %v, want nil (connect failed before GetConfig could even run)", resp.Diff)
	}
}
