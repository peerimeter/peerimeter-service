package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/caarlos0/env/v11"
	"github.com/peerimeter/peerimeter-service/internal/origin"
)

// MigrateConfig contains the environment settings used by the migrate command.
type MigrateConfig struct {
	StorageBackend      string `env:"PEERIMETER_STORAGE_BACKEND"`
	DatabaseURL         string `env:"PEERIMETER_DATABASE_URL"`
	DatabaseCACert      string `env:"PEERIMETER_DATABASE_CA_CERT"`
	AllowSSLModeRequire bool   `env:"PEERIMETER_DATABASE_ALLOW_SSLMODE_REQUIRE" envDefault:"false"`
}

// LoadMigrate reads and validates the settings required by the migrate command.
func LoadMigrate() (MigrateConfig, error) {
	cfg, err := env.ParseAs[MigrateConfig]()
	if err != nil {
		var parseErr env.ParseError
		if errors.As(err, &parseErr) && parseErr.Name == "AllowSSLModeRequire" {
			return MigrateConfig{}, errors.New("PEERIMETER_DATABASE_ALLOW_SSLMODE_REQUIRE must be a boolean")
		}
		return MigrateConfig{}, fmt.Errorf("parse migrate environment: %w", err)
	}
	if cfg.StorageBackend != "postgres" {
		return MigrateConfig{}, errors.New("PEERIMETER_STORAGE_BACKEND must be postgres for the migrate command")
	}
	cfg.DatabaseURL = strings.TrimSpace(cfg.DatabaseURL)
	if cfg.DatabaseURL == "" {
		return MigrateConfig{}, errors.New("PEERIMETER_DATABASE_URL must be set for the migrate command")
	}
	return cfg, nil
}

// Config contains the service settings loaded from environment variables.
type Config struct {
	APIKey                      string `env:"PEERIMETER_API_KEY,required"`
	StorageBackend              string `env:"PEERIMETER_STORAGE_BACKEND" envDefault:"file"`
	DatabaseURL                 string `env:"PEERIMETER_DATABASE_URL"`
	DatabaseCACert              string `env:"PEERIMETER_DATABASE_CA_CERT"`
	DatabaseAllowSSLModeRequire bool   `env:"PEERIMETER_DATABASE_ALLOW_SSLMODE_REQUIRE" envDefault:"false"`
	AutoMigrate                 bool   `env:"PEERIMETER_AUTO_MIGRATE" envDefault:"true"`
	ImportFile                  string `env:"PEERIMETER_IMPORT_FILE"`
	ImportSidecarOnly           bool   `env:"PEERIMETER_IMPORT_SIDECAR_ONLY"`
	DataFile                    string `env:"PEERIMETER_DATA_FILE" envDefault:"/var/lib/peerimeter/config.json"`
	ListenAddr                  string `env:"PEERIMETER_LISTEN_ADDR" envDefault:"127.0.0.1"`
	PublicAPIURL                string `env:"PEERIMETER_PUBLIC_API_URL"`
	RequireHTTPS                bool   `env:"PEERIMETER_REQUIRE_HTTPS" envDefault:"true"`
	CORSAllowedOrigins          string `env:"PEERIMETER_CORS_ALLOWED_ORIGINS"`
	Port                        string `env:"PORT" envDefault:"8080"`
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
	if cfg.StorageBackend != "file" && cfg.StorageBackend != "postgres" {
		return Config{}, fmt.Errorf("PEERIMETER_STORAGE_BACKEND must be file or postgres")
	}
	if cfg.StorageBackend == "postgres" && strings.TrimSpace(cfg.DatabaseURL) == "" {
		return Config{}, fmt.Errorf("PEERIMETER_DATABASE_URL must be set when PEERIMETER_STORAGE_BACKEND=postgres")
	}
	if cfg.StorageBackend != "postgres" && (cfg.DatabaseCACert != "" || cfg.DatabaseAllowSSLModeRequire) {
		return Config{}, fmt.Errorf("PEERIMETER_DATABASE_CA_CERT and PEERIMETER_DATABASE_ALLOW_SSLMODE_REQUIRE require PEERIMETER_STORAGE_BACKEND=postgres")
	}
	if cfg.ImportSidecarOnly && (cfg.StorageBackend != "postgres" || strings.TrimSpace(cfg.ImportFile) == "") {
		return Config{}, fmt.Errorf("PEERIMETER_IMPORT_SIDECAR_ONLY requires PostgreSQL storage and PEERIMETER_IMPORT_FILE")
	}
	for _, raw := range strings.Split(cfg.CORSAllowedOrigins, ",") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if _, err := origin.Normalize(raw); err != nil {
			return Config{}, fmt.Errorf("PEERIMETER_CORS_ALLOWED_ORIGINS contains an invalid origin: %w", err)
		}
	}
	return cfg, nil
}
