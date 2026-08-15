// Package config loads this service's environment configuration.
package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Port         int
	SharedSecret string
}

func Load() (Config, error) {
	secret := os.Getenv("GEARCTL_SHARED_SECRET")
	if secret == "" {
		return Config{}, fmt.Errorf("missing required env var GEARCTL_SHARED_SECRET")
	}

	port := 8090
	if raw := os.Getenv("PORT"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			port = parsed
		}
	}

	return Config{Port: port, SharedSecret: secret}, nil
}
