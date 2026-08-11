// Package auth implements service-to-service authentication: platform-api
// sends the same static secret (OLT_GATEWAY_SHARED_SECRET) in every
// request. This is a private, internal-network-only service (no `ports:`
// mapping in compose), but every request is still authenticated to
// prevent lateral movement if another container on the same network were
// compromised.
package auth

import (
	"crypto/subtle"
	"net/http"
)

const headerName = "X-Internal-Auth"

// Middleware wraps an http.Handler, rejecting any request whose
// X-Internal-Auth header doesn't match sharedSecret via a constant-time
// comparison (to avoid a timing side-channel).
func Middleware(sharedSecret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get(headerName)
		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(sharedSecret)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
