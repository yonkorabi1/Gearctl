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

func postMergeConfig(t *testing.T, body string) (*httptest.ResponseRecorder, MergeConfigResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/merge-config", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	MergeConfigHandler(rec, req)

	var decoded MergeConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode response body %q: %v", rec.Body.String(), err)
	}
	return rec, decoded
}

func TestMergeConfigHandlerRejectsInvalidJSON(t *testing.T) {
	rec, resp := postMergeConfig(t, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestMergeConfigHandlerRejectsMissingHost(t *testing.T) {
	rec, resp := postMergeConfig(t, `{
		"port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"candidate": "hostname R1\n", "dryRun": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Message != "host is required" {
		t.Errorf("got error %+v, want message \"host is required\"", resp.Error)
	}
}

func TestMergeConfigHandlerRejectsMissingCandidate(t *testing.T) {
	rec, resp := postMergeConfig(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"dryRun": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Message != "candidate is required" {
		t.Errorf("got error %+v, want message \"candidate is required\"", resp.Error)
	}
}

// TestMergeConfigHandlerRequiresExplicitDryRun confirms omitting dryRun
// entirely is a validation error, not an implicit default -- this is the
// core "no silent default" safety property for a config-push feature.
func TestMergeConfigHandlerRequiresExplicitDryRun(t *testing.T) {
	rec, resp := postMergeConfig(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"candidate": "hostname R1\n"
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Message != "dryRun is required (true or false) -- there is no implicit default for a config-push feature" {
		t.Errorf("got error %+v, want the required-dryRun message", resp.Error)
	}
}

func TestMergeConfigHandlerRequiresExpectedConfigHashWhenApplying(t *testing.T) {
	rec, resp := postMergeConfig(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"candidate": "hostname R1\n", "dryRun": false
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

func TestMergeConfigHandlerBlocksDisallowedTarget(t *testing.T) {
	rec, resp := postMergeConfig(t, `{
		"host": "127.0.0.1", "port": 22, "protocol": "ssh",
		"credentials": {"username": "a", "password": "b"},
		"candidate": "hostname R1\n", "dryRun": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "validation" {
		t.Errorf("got error %+v, want type=validation", resp.Error)
	}
}

// TestMergeConfigHandlerReportsUnsupportedForVendorsWithoutConfigLoader
// exercises Resolve's fallback (RawDriver) -- confirms /v1/merge-config
// fails fast with a distinguishable "unsupported" error instead of
// attempting a connection. No network involved.
func TestMergeConfigHandlerReportsUnsupportedForVendorsWithoutConfigLoader(t *testing.T) {
	rec, resp := postMergeConfig(t, `{
		"host": "10.0.0.1", "port": 22, "protocol": "ssh",
		"vendor": "zte",
		"credentials": {"username": "a", "password": "b"},
		"candidate": "hostname R1\n", "dryRun": true
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

func TestMergeConfigHandlerSessionNotFound(t *testing.T) {
	rec, resp := postMergeConfig(t, `{"sessionId": "does-not-exist", "candidate": "hostname R1\n", "dryRun": true}`)
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "session_not_found" {
		t.Errorf("got error %+v, want type=session_not_found", resp.Error)
	}
}

// TestMergeConfigHandlerSucceedsViaSession confirms the session path
// reaches LoadConfig() on the pooled driver -- config PUSH via a session
// is deliberately supported, unlike POST /v1/batch which excludes it
// (see BatchRequest's doc comment) -- a session call is still exactly
// one call in, one call out, so there's no batch-wide-outcome conflict.
func TestMergeConfigHandlerSucceedsViaSession(t *testing.T) {
	driver := &fakeSessionDriver{vendor: "cisco", diff: "+hostname R1", configHash: "abc123", applied: false}
	sessionID := openFakeSession(driver, "cisco")
	defer func() { _ = sessionStore.Close(sessionID) }()

	rec, resp := postMergeConfig(t, fmt.Sprintf(`{"sessionId": %q, "candidate": "hostname R1\n", "dryRun": true}`, sessionID))
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	if resp.Status != "success" {
		t.Errorf("got status %q, want \"success\" (error: %+v)", resp.Status, resp.Error)
	}
	if resp.Diff == nil || *resp.Diff != "+hostname R1" {
		t.Errorf("got diff %v, want \"+hostname R1\"", resp.Diff)
	}
	if resp.ConfigHash == nil || *resp.ConfigHash != "abc123" {
		t.Errorf("got configHash %v, want \"abc123\"", resp.ConfigHash)
	}
}

func TestMergeConfigHandlerReportsUnsupportedViaSession(t *testing.T) {
	driver := &drivers.RawDriver{}
	sessionID := openFakeSession(driver, "zte")
	defer func() { _ = sessionStore.Close(sessionID) }()

	rec, resp := postMergeConfig(t, fmt.Sprintf(`{"sessionId": %q, "candidate": "hostname R1\n", "dryRun": true}`, sessionID))
	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	if resp.Error == nil || resp.Error.Type != "unsupported" {
		t.Errorf("got error %+v, want type=unsupported", resp.Error)
	}
}

// TestMergeConfigHandlerReturnsFailedForConnectionError exercises the
// real CiscoIOSDriver/transport stack against 192.0.2.1 (TEST-NET-1,
// RFC 5737, guaranteed non-routable) -- same technique as the other
// handler tests.
func TestMergeConfigHandlerReturnsFailedForConnectionError(t *testing.T) {
	rec, resp := postMergeConfig(t, `{
		"host": "192.0.2.1", "port": 22, "protocol": "ssh",
		"vendor": "cisco",
		"credentials": {"username": "a", "password": "b"},
		"candidate": "hostname R1\n", "dryRun": true,
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
