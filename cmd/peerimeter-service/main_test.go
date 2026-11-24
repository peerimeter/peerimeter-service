package main

import "testing"

func TestMigrateFromEnvironmentRequiresPostgresBackendAndURL(t *testing.T) {
	t.Setenv("PEERIMETER_STORAGE_BACKEND", "file")
	t.Setenv("PEERIMETER_DATABASE_URL", "postgres://localhost/test")
	if err := migrateFromEnvironment(); err == nil {
		t.Fatal("migrate accepted file storage backend")
	}
	t.Setenv("PEERIMETER_STORAGE_BACKEND", "postgres")
	t.Setenv("PEERIMETER_DATABASE_URL", "")
	if err := migrateFromEnvironment(); err == nil {
		t.Fatal("migrate accepted missing database URL")
	}
}
