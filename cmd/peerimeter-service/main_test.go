package main

import (
	"os"
	"path/filepath"
	"strings"
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
	nodes := filepath.Join(root, "nodes")
	if err := os.Mkdir(nodes, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodes, "node-a.yaml"), []byte("valid sidecar\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alternateIndex := filepath.Join(root, "confg.json")
	if err := validateImportIndex(alternateIndex, true); err == nil || !strings.Contains(err.Error(), "config.json exists") {
		t.Fatalf("sidecar-only import error = %v, want canonical config.json error", err)
	}
	if err := os.Remove(filepath.Join(root, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := validateImportIndex(alternateIndex, true); err != nil {
		t.Fatalf("sidecar-only source without canonical index rejected: %v", err)
	}
}

func TestValidateImportIndexRejectsSymlinkParentTraversal(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "archive")
	if err := os.MkdirAll(filepath.Join(archive, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "config.json"), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	nodes := filepath.Join(root, "nodes")
	if err := os.Mkdir(nodes, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodes, "node-a.yaml"), []byte("valid sidecar\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(archive, "sub"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	indexPath := root + string(os.PathSeparator) + "link" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "typo.json"
	if err := validateImportIndex(indexPath, true); err == nil || !strings.Contains(err.Error(), "cannot contain '..'") {
		t.Fatalf("sidecar-only import error = %v, want ambiguous path error", err)
	}
}
