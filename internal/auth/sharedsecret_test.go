package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware("s3cret", inner)

	t.Run("rejects a missing header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/execute", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want 401", rec.Code)
		}
	})

	t.Run("rejects a wrong secret", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/execute", nil)
		req.Header.Set("X-Internal-Auth", "wrong")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want 401", rec.Code)
		}
	})

	t.Run("accepts the correct secret", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/execute", nil)
		req.Header.Set("X-Internal-Auth", "s3cret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200", rec.Code)
		}
	})
}
