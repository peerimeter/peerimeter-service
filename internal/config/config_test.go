package config

import "testing"

func TestLoadEnvironment(t *testing.T) {
	t.Setenv("PEERIMETER_API_KEY", "test-api-key")
	t.Setenv("PEERIMETER_DATA_FILE", "/tmp/config.json")
	t.Setenv("PEERIMETER_LISTEN_ADDR", "0.0.0.0")
	t.Setenv("PEERIMETER_PUBLIC_API_URL", "https://api.example.test")
	t.Setenv("PEERIMETER_REQUIRE_HTTPS", "true")
	t.Setenv("PEERIMETER_CORS_ALLOWED_ORIGINS", "https://dashboard.example.test, https://admin.example.test")
	t.Setenv("PORT", "9090")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.APIKey != "test-api-key" || cfg.DataFile != "/tmp/config.json" || cfg.ListenAddr != "0.0.0.0" || cfg.PublicAPIURL != "https://api.example.test" || !cfg.RequireHTTPS || cfg.CORSAllowedOrigins != "https://dashboard.example.test, https://admin.example.test" || cfg.Port != "9090" {
		t.Fatalf("Load() = %#v, unexpected configuration", cfg)
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("PEERIMETER_API_KEY", "test-api-key")
	t.Setenv("PEERIMETER_DATA_FILE", "")
	t.Setenv("PEERIMETER_LISTEN_ADDR", "")
	t.Setenv("PEERIMETER_PUBLIC_API_URL", "")
	t.Setenv("PEERIMETER_REQUIRE_HTTPS", "")
	t.Setenv("PEERIMETER_CORS_ALLOWED_ORIGINS", "")
	t.Setenv("PORT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DataFile != "/var/lib/peerimeter/config.json" || cfg.ListenAddr != "127.0.0.1" || !cfg.RequireHTTPS || cfg.Port != "8080" {
		t.Fatalf("Load() defaults = %#v, unexpected values", cfg)
	}
}

func TestLoadCanDisableHTTPSRequirement(t *testing.T) {
	t.Setenv("PEERIMETER_API_KEY", "test-api-key")
	t.Setenv("PEERIMETER_REQUIRE_HTTPS", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.RequireHTTPS {
		t.Fatal("RequireHTTPS = true, want false")
	}
}

func TestLoadRejectsInvalidCORSOrigins(t *testing.T) {
	t.Setenv("PEERIMETER_API_KEY", "test-api-key")
	for _, value := range []string{"*", "https://dashboard.example.test/path", "https://user:pass@example.test", "https://example.test?query=1", "https://example.test/#fragment", "https://example.test:", "https://example.test:0", "https://example.test:65536"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("PEERIMETER_CORS_ALLOWED_ORIGINS", value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() with CORS origins %q succeeded, want validation error", value)
			}
		})
	}
}

func TestLoadRequiresAPIKey(t *testing.T) {
	t.Setenv("PEERIMETER_API_KEY", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want required API key error")
	}
}

func TestLoadStorageBackendSelection(t *testing.T) {
	t.Setenv("PEERIMETER_API_KEY", "test-api-key")
	t.Setenv("PEERIMETER_STORAGE_BACKEND", "postgres")
	t.Setenv("PEERIMETER_DATABASE_URL", "postgres://localhost/test")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorageBackend != "postgres" || cfg.DatabaseURL != "postgres://localhost/test" {
		t.Fatalf("storage config = %#v", cfg)
	}
	t.Setenv("PEERIMETER_STORAGE_BACKEND", "unknown")
	if _, err := Load(); err == nil {
		t.Fatal("unknown storage backend accepted")
	}
	t.Setenv("PEERIMETER_STORAGE_BACKEND", "postgres")
	t.Setenv("PEERIMETER_DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("PostgreSQL backend without database URL accepted")
	}
}

func TestLoadSidecarOnlyImportRequiresPostgresAndImportPath(t *testing.T) {
	t.Setenv("PEERIMETER_API_KEY", "test-api-key")
	t.Setenv("PEERIMETER_IMPORT_SIDECAR_ONLY", "true")
	t.Setenv("PEERIMETER_STORAGE_BACKEND", "file")
	t.Setenv("PEERIMETER_IMPORT_FILE", "")
	if _, err := Load(); err == nil {
		t.Fatal("sidecar-only flag accepted without PostgreSQL import settings")
	}
	t.Setenv("PEERIMETER_STORAGE_BACKEND", "postgres")
	t.Setenv("PEERIMETER_DATABASE_URL", "postgres://localhost/test")
	if _, err := Load(); err == nil {
		t.Fatal("sidecar-only flag accepted without import path")
	}
	t.Setenv("PEERIMETER_IMPORT_FILE", "/tmp/config.json")
	if _, err := Load(); err != nil {
		t.Fatalf("valid sidecar-only import config rejected: %v", err)
	}
}
