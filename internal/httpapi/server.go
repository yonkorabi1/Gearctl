package httpapi

import (
	"net/http"

	"github.com/onu-platform/gearctl/internal/auth"
)

// NewServer builds the gateway's HTTP handler -- no framework, Go's
// stdlib net/http.ServeMux (1.22+) supports method-specific patterns
// natively, which is all this service needs.
func NewServer(sharedSecret string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", HealthHandler)
	mux.Handle("POST /v1/execute", auth.Middleware(sharedSecret, http.HandlerFunc(ExecuteHandler)))
	mux.Handle("POST /v1/facts", auth.Middleware(sharedSecret, http.HandlerFunc(FactsHandler)))
	mux.Handle("POST /v1/interfaces", auth.Middleware(sharedSecret, http.HandlerFunc(InterfacesHandler)))
	mux.Handle("POST /v1/lldp-neighbors", auth.Middleware(sharedSecret, http.HandlerFunc(LLDPNeighborsHandler)))
	mux.Handle("POST /v1/batch", auth.Middleware(sharedSecret, http.HandlerFunc(BatchHandler)))
	mux.Handle("POST /v1/config", auth.Middleware(sharedSecret, http.HandlerFunc(ConfigHandler)))
	mux.Handle("POST /v1/merge-config", auth.Middleware(sharedSecret, http.HandlerFunc(MergeConfigHandler)))
	mux.Handle("POST /v1/merge-cmd", auth.Middleware(sharedSecret, http.HandlerFunc(MergeCmdHandler)))
	return mux
}
