package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServerHealthIsUnauthenticated(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
	if rec.Body.String() != `{"status":"ok"}`+"\n" {
		t.Fatalf("got body %q", rec.Body.String())
	}
}

func TestServerExecuteRequiresAuth(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rec.Code)
	}
}

func TestServerExecuteWithValidAuthReachesHandler(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", nil)
	req.Header.Set("X-Internal-Auth", "s3cret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	// An empty body reaches ExecuteHandler and fails JSON decoding (EOF),
	// which is a 400 -- proof the request passed auth and got routed,
	// as opposed to the 401 it'd get without the header.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 (routed past auth into validation)", rec.Code)
	}
}
