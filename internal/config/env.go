package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/caarlos0/env/v11"
)

// Config contains the service settings loaded from environment variables.
type Config struct {
	APIKey             string `env:"PEERIMETER_API_KEY,required"`
	DataFile           string `env:"PEERIMETER_DATA_FILE" envDefault:"/var/lib/peerimeter/config.json"`
	ListenAddr         string `env:"PEERIMETER_LISTEN_ADDR" envDefault:"127.0.0.1"`
	PublicAPIURL       string `env:"PEERIMETER_PUBLIC_API_URL"`
	RequireHTTPS       bool   `env:"PEERIMETER_REQUIRE_HTTPS" envDefault:"true"`
	CORSAllowedOrigins string `env:"PEERIMETER_CORS_ALLOWED_ORIGINS"`
	Port               string `env:"PORT" envDefault:"8080"`
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
	for _, raw := range strings.Split(cfg.CORSAllowedOrigins, ",") {
		origin := strings.TrimSpace(raw)
		if origin == "" {
			continue
		}
		parsed, err := url.Parse(origin)
		if err != nil {
			return Config{}, fmt.Errorf("PEERIMETER_CORS_ALLOWED_ORIGINS must contain comma-separated HTTP(S) origins without paths, credentials, query, fragment, or wildcards")
		}
		scheme := strings.ToLower(parsed.Scheme)
		if (scheme != "https" && scheme != "http") || parsed.Hostname() == "" || strings.HasSuffix(parsed.Host, ":") || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || origin == "null" || strings.Contains(origin, "*") {
			return Config{}, fmt.Errorf("PEERIMETER_CORS_ALLOWED_ORIGINS must contain comma-separated HTTP(S) origins without paths, credentials, query, fragment, or wildcards")
		}
		if port := parsed.Port(); port != "" {
			value, err := strconv.Atoi(port)
			if err != nil || value < 1 || value > 65535 {
				return Config{}, fmt.Errorf("PEERIMETER_CORS_ALLOWED_ORIGINS contains an invalid port")
			}
		}
	}
	return cfg, nil
}
