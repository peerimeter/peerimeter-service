package config

import "testing"

func TestLoadMigrate(t *testing.T) {
	tests := []struct {
		name      string
		backend   string
		database  string
		allowSSL  string
		wantError bool
		wantAllow bool
	}{
		{name: "valid defaults", backend: "postgres", database: " postgres://localhost/test "},
		{name: "allow required ssl mode", backend: "postgres", database: "postgres://localhost/test", allowSSL: "true", wantAllow: true},
		{name: "invalid boolean", backend: "postgres", database: "postgres://localhost/test", allowSSL: "sometimes", wantError: true},
		{name: "wrong backend", backend: "file", database: "postgres://localhost/test", wantError: true},
		{name: "missing database", backend: "postgres", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PEERIMETER_STORAGE_BACKEND", test.backend)
			t.Setenv("PEERIMETER_DATABASE_URL", test.database)
			t.Setenv("PEERIMETER_DATABASE_ALLOW_SSLMODE_REQUIRE", test.allowSSL)
			t.Setenv("PEERIMETER_DATABASE_CA_CERT", "test-ca")
			got, err := LoadMigrate()
			if (err != nil) != test.wantError {
				t.Fatalf("LoadMigrate() error = %v, wantError %v", err, test.wantError)
			}
			if err != nil {
				return
			}
			if got.AllowSSLModeRequire != test.wantAllow {
				t.Errorf("AllowSSLModeRequire = %v, want %v", got.AllowSSLModeRequire, test.wantAllow)
			}
			if got.DatabaseURL != "postgres://localhost/test" || got.DatabaseCACert != "test-ca" {
				t.Errorf("loaded config = %+v", got)
			}
		})
	}
}
