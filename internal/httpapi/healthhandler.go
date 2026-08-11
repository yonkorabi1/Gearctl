package httpapi

import "net/http"

// HealthHandler is unauthenticated -- internal network only anyway, and
// this is what the docker compose healthcheck polls.
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
