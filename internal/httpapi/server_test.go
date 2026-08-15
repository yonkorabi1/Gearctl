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

func TestServerFactsRequiresAuth(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/facts", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rec.Code)
	}
}

func TestServerFactsWithValidAuthReachesHandler(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/facts", nil)
	req.Header.Set("X-Internal-Auth", "s3cret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 (routed past auth into validation)", rec.Code)
	}
}

func TestServerConfigRequiresAuth(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/config", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rec.Code)
	}
}

func TestServerConfigWithValidAuthReachesHandler(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/config", nil)
	req.Header.Set("X-Internal-Auth", "s3cret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 (routed past auth into validation)", rec.Code)
	}
}

func TestServerInterfacesRequiresAuth(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/interfaces", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rec.Code)
	}
}

func TestServerInterfacesWithValidAuthReachesHandler(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/interfaces", nil)
	req.Header.Set("X-Internal-Auth", "s3cret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 (routed past auth into validation)", rec.Code)
	}
}

func TestServerLLDPNeighborsRequiresAuth(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/lldp-neighbors", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rec.Code)
	}
}

func TestServerLLDPNeighborsWithValidAuthReachesHandler(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/lldp-neighbors", nil)
	req.Header.Set("X-Internal-Auth", "s3cret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 (routed past auth into validation)", rec.Code)
	}
}

func TestServerBatchRequiresAuth(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/batch", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rec.Code)
	}
}

func TestServerBatchWithValidAuthReachesHandler(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/batch", nil)
	req.Header.Set("X-Internal-Auth", "s3cret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 (routed past auth into validation)", rec.Code)
	}
}

func TestServerMergeConfigRequiresAuth(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/merge-config", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rec.Code)
	}
}

func TestServerMergeConfigWithValidAuthReachesHandler(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/merge-config", nil)
	req.Header.Set("X-Internal-Auth", "s3cret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 (routed past auth into validation)", rec.Code)
	}
}

func TestServerMergeCmdRequiresAuth(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/merge-cmd", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rec.Code)
	}
}

func TestServerMergeCmdWithValidAuthReachesHandler(t *testing.T) {
	server := NewServer("s3cret")
	req := httptest.NewRequest(http.MethodPost, "/v1/merge-cmd", nil)
	req.Header.Set("X-Internal-Auth", "s3cret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 (routed past auth into validation)", rec.Code)
	}
}
