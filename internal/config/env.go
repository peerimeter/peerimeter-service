package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
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
	PasswordLogin               string `env:"PEERIMETER_PASSWORD_LOGIN_ENABLED"`
	GitHubClientID              string `env:"PEERIMETER_GITHUB_CLIENT_ID"`
	GitHubClientSecret          string `env:"PEERIMETER_GITHUB_CLIENT_SECRET"`
	GitHubOrganization          string `env:"PEERIMETER_GITHUB_ORG"`
	GitHubTeam                  string `env:"PEERIMETER_GITHUB_TEAM"`
	GitHubDashboardURL          string `env:"PEERIMETER_GITHUB_DASHBOARD_URL"`
	AuthSessionSecret           string `env:"PEERIMETER_AUTH_SESSION_SECRET"`
	StorageBackend              string `env:"PEERIMETER_STORAGE_BACKEND" envDefault:"file"`
	DatabaseURL                 string `env:"PEERIMETER_DATABASE_URL"`
	DatabaseCACert              string `env:"PEERIMETER_DATABASE_CA_CERT"`
	DatabaseAllowSSLModeRequire bool   `env:"PEERIMETER_DATABASE_ALLOW_SSLMODE_REQUIRE" envDefault:"false"`
	AutoMigrate                 bool   `env:"PEERIMETER_AUTO_MIGRATE" envDefault:"true"`
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
	if cfg.GitHubSSOEnabled() {
		if cfg.GitHubClientID == "" || cfg.GitHubClientSecret == "" || cfg.GitHubOrganization == "" || cfg.GitHubTeam == "" || cfg.GitHubDashboardURL == "" || len(cfg.AuthSessionSecret) < 32 {
			return Config{}, fmt.Errorf("GitHub SSO requires client ID, client secret, organization, team, dashboard URL, and a session secret of at least 32 characters")
		}
		if cfg.PublicAPIURL == "" {
			return Config{}, fmt.Errorf("PEERIMETER_PUBLIC_API_URL must be set when GitHub SSO is enabled")
		}
		serviceURL, err := url.Parse(cfg.PublicAPIURL)
		if err != nil || serviceURL.Scheme != "https" || serviceURL.Host == "" || serviceURL.Path != "" || serviceURL.RawQuery != "" || serviceURL.Fragment != "" {
			return Config{}, fmt.Errorf("PEERIMETER_PUBLIC_API_URL must be an HTTPS origin without a path when GitHub SSO is enabled")
		}
		if !githubSlug.MatchString(cfg.GitHubOrganization) || !githubSlug.MatchString(cfg.GitHubTeam) {
			return Config{}, fmt.Errorf("GitHub organization and team must be slugs containing only letters, numbers, and hyphens")
		}
		dashboardURL, err := url.Parse(cfg.GitHubDashboardURL)
		if err != nil || dashboardURL.Scheme != "https" || dashboardURL.Host == "" || dashboardURL.Path != "" || dashboardURL.RawQuery != "" || dashboardURL.Fragment != "" {
			return Config{}, fmt.Errorf("PEERIMETER_GITHUB_DASHBOARD_URL must be an HTTPS origin without a path")
		}
	}
	if cfg.AuthSessionSecret != "" && len(cfg.AuthSessionSecret) < 32 {
		return Config{}, fmt.Errorf("PEERIMETER_AUTH_SESSION_SECRET must contain at least 32 characters")
	}
	if cfg.PasswordLogin != "" && cfg.PasswordLogin != "true" && cfg.PasswordLogin != "false" {
		return Config{}, fmt.Errorf("PEERIMETER_PASSWORD_LOGIN_ENABLED must be true or false")
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

var githubSlug = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

func (cfg Config) GitHubSSOEnabled() bool {
	return cfg.GitHubClientID != "" || cfg.GitHubClientSecret != "" || cfg.GitHubOrganization != "" || cfg.GitHubTeam != ""
}

func (cfg Config) IsPasswordLoginEnabled() bool {
	if cfg.PasswordLogin != "" {
		return cfg.PasswordLogin == "true"
	}
	return !cfg.GitHubSSOEnabled()
}
