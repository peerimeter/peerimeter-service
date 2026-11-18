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

func TestValidateImportIndexAllowsExplicitSidecarOnlySource(t *testing.T) {
	root := t.TempDir()
	index := filepath.Join(root, "config.json")
	if err := os.WriteFile(index, []byte(`{"version":1,"networks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateImportIndex(index, true); err == nil {
		t.Fatal("sidecar-only mode accepted with an index file")
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := validateImportIndex(index, false); err == nil {
		t.Fatal("missing index was accepted without sidecar-only mode")
	}
	if err := os.Mkdir(filepath.Join(root, "nodes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nodes", "legacy.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateImportIndex(index, false); err == nil {
		t.Fatal("sidecar-only source was accepted without explicit mode")
	}
	if err := validateImportIndex(index, true); err != nil {
		t.Fatalf("explicit sidecar-only source rejected: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, "nodes")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(index+".registrations/mesh", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(index+".registrations/mesh", "node-a.yaml"), []byte("public_key: key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateImportIndex(index, true); err != nil {
		t.Fatalf("legacy registration sidecars rejected: %v", err)
	}
}

func TestValidateImportIndexRejectsMissingAlternateWhenCanonicalIndexExists(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nodes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateImportIndex(filepath.Join(root, "confg.json"), true); err == nil {
		t.Fatal("sidecar-only import accepted a missing alternate path despite config.json")
	}
}
