package configstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentQueuedWritesPersistAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	store, err := Open(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	const count = 48
	var wg sync.WaitGroup
	errCh := make(chan error, count)
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errCh <- store.PutNode(context.Background(), "network-a", fmt.Sprintf("node-%d", i), []byte(`{"revision":1}`))
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutNetwork(context.Background(), "network-a", []byte(`{"name":"mesh"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetNode("network-a", "node-1"); err != nil {
		t.Fatalf("network write dropped node config in memory: %v", err)
	}
	store.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config file mode = %04o, want 0600", got)
	}
	store, err = Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	if _, err := store.GetNetwork("network-a"); err != nil {
		t.Fatalf("network config missing after restart: %v", err)
	}
	for i := range count {
		if _, err := store.GetNode("network-a", fmt.Sprintf("node-%d", i)); err != nil {
			t.Fatalf("node %d missing after restart: %v", i, err)
		}
	}
	nodeFile := filepath.Join(filepath.Dir(path), "nodes", "network-a", "node-1.yaml")
	nodeYAML, err := os.ReadFile(nodeFile)
	if err != nil || !strings.Contains(string(nodeYAML), `"revision": 1`) {
		t.Fatalf("node YAML = %q, err = %v", nodeYAML, err)
	}
	if info, err := os.Stat(nodeFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("node YAML permissions = %v, err = %v", info, err)
	}
}

func TestGetMissingConfig(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "config.json"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.GetNode("missing", "missing"); err != ErrNotFound {
		t.Fatalf("GetNode error = %v, want ErrNotFound", err)
	}
	if err := store.PutNetwork(context.Background(), "", []byte(`{"version":1}`)); err != ErrInvalidID {
		t.Fatalf("PutNetwork with empty ID error = %v, want ErrInvalidID", err)
	}
	if err := store.PutNode(context.Background(), "network", "", []byte(`{"version":1}`)); err != ErrInvalidID {
		t.Fatalf("PutNode with empty node ID error = %v, want ErrInvalidID", err)
	}
}

func TestStoreRejectsPathLikeIDs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "config.json"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"..", "../outside", "a/b", strings.Repeat("n", 129)} {
		if err := store.PutNode(context.Background(), id, "node", []byte(`{"version":1}`)); err != ErrInvalidID {
			t.Errorf("PutNode network ID %q error = %v, want ErrInvalidID", id, err)
		}
		if err := store.PutNode(context.Background(), "network", id, []byte(`{"version":1}`)); err != ErrInvalidID {
			t.Errorf("PutNode node ID %q error = %v, want ErrInvalidID", id, err)
		}
	}
}

func TestMigratesLegacyNodeDocumentsToSeparateYAMLFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"version":1,"networks":{"net-a":{"config":{"version":1},"nodes":{"node-a":{"version":1,"public_key":"fake"}}}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.GetNode("net-a", "node-a"); err != nil {
		t.Fatalf("legacy node unavailable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "nodes", "net-a", "node-a.yaml")); err != nil {
		t.Fatalf("legacy node YAML was not created: %v", err)
	}
}

func TestLoadsLegacyRegistrationsWhenNewDirectoryExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacyDir := filepath.Join(path+".registrations", "net-a")
	currentDir := filepath.Join(filepath.Dir(path), "registrations", "net-a")
	for dir, files := range map[string]map[string]string{
		legacyDir:  {"node-a.yaml": "version: 1\nsource: legacy\n", "node-b.yaml": "version: 1\nsource: legacy\n"},
		currentDir: {"node-a.yaml": "version: 1\nsource: current\n"},
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for name, contents := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	store, err := Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registrations := store.state.Networks["net-a"].Registrations
	if len(registrations) != 2 {
		t.Fatalf("loaded %d registrations, want 2", len(registrations))
	}
	if !strings.Contains(string(registrations["node-a"]), `"source":"current"`) {
		t.Fatalf("current registration did not take precedence: %s", registrations["node-a"])
	}
	if !strings.Contains(string(registrations["node-b"]), `"source":"legacy"`) {
		t.Fatalf("legacy registration was not loaded: %s", registrations["node-b"])
	}
}

func TestYAMLNodePersistencePreservesLargeJSONIntegers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store, err := Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	value := json.RawMessage(`{"version":1,"revision":9007199254740993}`)
	if err := store.PutNode(context.Background(), "net-a", "node-a", value); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.GetNode("net-a", "node-a")
	if err != nil || !strings.Contains(string(got), `"revision":9007199254740993`) {
		t.Fatalf("reopened node = %s, err = %v", got, err)
	}
}

func TestYAMLNodePersistencePreservesLiteralNEL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store, err := Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	value := json.RawMessage("{\"version\":1,\"text\":\"AB\"}")
	if err := store.PutNode(context.Background(), "net-a", "node-a", value); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.GetNode("net-a", "node-a")
	var decoded map[string]any
	if err == nil {
		err = json.Unmarshal(got, &decoded)
	}
	if err != nil || decoded["text"] != "AB" {
		t.Fatalf("reopened node = %q, err = %v", got, err)
	}
}

func TestNodeWriteUsesJSONLastValueForDuplicateKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store, err := Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutNode(context.Background(), "net-a", "node-a", json.RawMessage(`{"version":1,"name":"old","name":"new"}`)); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.GetNode("net-a", "node-a")
	var decoded map[string]any
	if err == nil {
		err = json.Unmarshal(got, &decoded)
	}
	if err != nil || decoded["name"] != "new" {
		t.Fatalf("reopened duplicate-key node = %s, err = %v", got, err)
	}
}

func TestLegacyMigrationPreservesLargeJSONIntegers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"version":1,"networks":{"net-a":{"config":{"version":1},"nodes":{"node-a":{"version":1,"revision":9007199254740993,"name":"old","name":"new"}}}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.GetNode("net-a", "node-a")
	if err != nil || !strings.Contains(string(got), `"revision":9007199254740993`) || !strings.Contains(string(got), `"name":"new"`) {
		t.Fatalf("migrated node = %s, err = %v", got, err)
	}
}

func TestCreateNetworkPersistsCIDRMetadataAndRejectsDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store, err := Open(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	body := json.RawMessage(`{"version":1,"id":"mesh-a","name":"Mesh A","cidr":"10.90.0.0/24"}`)
	if err := store.CreateNetwork(context.Background(), "mesh-a", body); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateNetwork(context.Background(), "mesh-a", body); err != ErrExists {
		t.Fatalf("duplicate CreateNetwork error = %v, want ErrExists", err)
	}
	if err := store.CreateNetwork(context.Background(), "mesh-overlap", json.RawMessage(`{"version":1,"id":"mesh-overlap","name":"Overlap","cidr":"10.90.0.128/25"}`)); err != ErrCIDROverlap {
		t.Fatalf("overlapping CreateNetwork error = %v, want ErrCIDROverlap", err)
	}
	store.Close()
	store, err = Open(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.GetNetworkMetadata("mesh-a")
	var actual, expected any
	_ = json.Unmarshal(got, &actual)
	_ = json.Unmarshal(body, &expected)
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("network metadata = %s, err = %v", got, err)
	}
}

func TestCreateNetworkRejectsCIDRThatCrossesPrivateBlock(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "config.json"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateNetwork(context.Background(), "mesh-a", json.RawMessage(`{"version":1,"id":"mesh-a","name":"Mesh","cidr":"192.168.0.0/15"}`)); err != ErrInvalidNetwork {
		t.Fatalf("cross-boundary CIDR error = %v, want ErrInvalidNetwork", err)
	}
}

func TestConcurrentRegistrationsAllocateUniqueAddresses(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "config.json"), 64)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	if err := store.CreateNetwork(context.Background(), "mesh-a", json.RawMessage(`{"version":1,"id":"mesh-a","name":"Mesh","cidr":"10.90.0.0/24"}`)); err != nil {
		t.Fatal(err)
	}
	const count = 32
	var wg sync.WaitGroup
	addresses := make(chan string, count)
	errorsFound := make(chan error, count)
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := json.RawMessage(fmt.Sprintf(`{"version":1,"public_key":"key-%d","listen_port":51820,"primary_interface":"eth0","local_addresses":["192.168.1.10/24"]}`, i))
			result, err := store.RegisterNode(context.Background(), "mesh-a", fmt.Sprintf("node-%d", i), body)
			if err != nil {
				errorsFound <- err
				return
			}
			var node struct {
				Address string `json:"tunnel_address"`
			}
			if err := json.Unmarshal(result, &node); err != nil {
				errorsFound <- err
				return
			}
			addresses <- node.Address
		}(i)
	}
	wg.Wait()
	close(addresses)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for address := range addresses {
		if address == "" || seen[address] {
			t.Fatalf("duplicate or empty allocated address %q", address)
		}
		seen[address] = true
	}
	if len(seen) != count {
		t.Fatalf("allocated %d unique addresses, want %d", len(seen), count)
	}
	store.Close()
	store, err = Open(store.path, 64)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := store.ListRegisteredNodes("mesh-a")
	if err != nil || len(registered) != count {
		t.Fatalf("registered nodes after restart = %d, err = %v", len(registered), err)
	}
}
