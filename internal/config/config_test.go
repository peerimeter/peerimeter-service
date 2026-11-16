package config

import "testing"

func TestLoadEnvironment(t *testing.T) {
	t.Setenv("PEERIMETER_API_KEY", "test-api-key")
	t.Setenv("PEERIMETER_DATA_FILE", "/tmp/config.json")
	t.Setenv("PEERIMETER_PUBLIC_API_URL", "https://api.example.test")
	t.Setenv("PORT", "9090")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.APIKey != "test-api-key" || cfg.DataFile != "/tmp/config.json" || cfg.PublicAPIURL != "https://api.example.test" || cfg.Port != "9090" {
		t.Fatalf("Load() = %#v, unexpected configuration", cfg)
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("PEERIMETER_API_KEY", "test-api-key")
	t.Setenv("PEERIMETER_DATA_FILE", "")
	t.Setenv("PEERIMETER_PUBLIC_API_URL", "")
	t.Setenv("PORT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DataFile != "/var/lib/peerimeter/config.json" || cfg.Port != "8080" {
		t.Fatalf("Load() defaults = %#v, unexpected values", cfg)
	}
}

func TestLoadRequiresAPIKey(t *testing.T) {
	t.Setenv("PEERIMETER_API_KEY", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want required API key error")
	}
}
