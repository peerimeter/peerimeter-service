package config

import (
	"fmt"
	"strings"

	"github.com/caarlos0/env/v11"
)

// Config contains the service settings loaded from environment variables.
type Config struct {
	APIKey       string `env:"PEERIMETER_API_KEY,required"`
	DataFile     string `env:"PEERIMETER_DATA_FILE" envDefault:"/var/lib/peerimeter/config.json"`
	PublicAPIURL string `env:"PEERIMETER_PUBLIC_API_URL"`
	Port         string `env:"PORT" envDefault:"8080"`
}

// Load reads service settings from the process environment.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return Config{}, fmt.Errorf("PEERIMETER_API_KEY must be set")
	}
	return cfg, nil
}
