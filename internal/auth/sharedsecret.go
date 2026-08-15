// Package auth implements service-to-service authentication: a caller
// sends the same static secret (gearctl's own config reads it as
// GEARCTL_SHARED_SECRET -- see internal/config/config.go; platform-api's
// client-side config still calls its copy of the same value
// OLT_GATEWAY_SHARED_SECRET, its own naming, unrelated to gearctl's) in
// every request. When gearctl runs as a private, internal-network-only
// service (no `ports:` mapping in compose), every request is still
// authenticated to prevent lateral movement if another container on the
// same network were compromised.
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
