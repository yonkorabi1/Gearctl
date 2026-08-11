package httpapi

import (
	"net/http"

	"github.com/onu-platform/olt-cli-gateway/internal/auth"
)

// NewServer builds the gateway's HTTP handler: exactly two routes, no
// framework -- Go's stdlib net/http.ServeMux (1.22+) supports method-
// specific patterns natively, which is all this service needs.
func NewServer(sharedSecret string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", HealthHandler)
	mux.Handle("POST /v1/execute", auth.Middleware(sharedSecret, http.HandlerFunc(ExecuteHandler)))
	return mux
}
