package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreMayContainData(t *testing.T) {
	root := t.TempDir()
	index := filepath.Join(root, "config.json")
	if fileStoreMayContainData(index) {
		t.Fatal("empty store reported as populated")
	}
	if err := os.WriteFile(index, []byte(`{"version":1,"networks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !fileStoreMayContainData(index) {
		t.Fatal("non-empty index file was not detected")
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "registrations"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "registrations", "state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !fileStoreMayContainData(index) {
		t.Fatal("registration sidecar data was not detected")
	}
}
