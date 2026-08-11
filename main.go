// Command olt-cli-gateway is a stateless SSH/Telnet CLI executor for OLT
// devices -- see README.md. Holds no state; platform-api is the sole
// orchestrator (credentials, run history, audit).
package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/onu-platform/olt-cli-gateway/internal/config"
	"github.com/onu-platform/olt-cli-gateway/internal/httpapi"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	handler := httpapi.NewServer(cfg.SharedSecret)
	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("olt-cli-gateway listening on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
