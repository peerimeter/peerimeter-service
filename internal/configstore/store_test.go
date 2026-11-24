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
	if _, err := store.GetNode(context.Background(), "network-a", "node-1"); err != nil {
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
	if _, err := store.GetNetwork(context.Background(), "network-a"); err != nil {
		t.Fatalf("network config missing after restart: %v", err)
	}
	for i := range count {
		if _, err := store.GetNode(context.Background(), "network-a", fmt.Sprintf("node-%d", i)); err != nil {
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
	if _, err := store.GetNode(context.Background(), "missing", "missing"); err != ErrNotFound {
		t.Fatalf("GetNode error = %v, want ErrNotFound", err)
	}
	if err := store.PutNetwork(context.Background(), "", []byte(`{"version":1}`)); err != ErrInvalidID {
		t.Fatalf("PutNetwork with empty ID error = %v, want ErrInvalidID", err)
	}
	if err := store.PutNode(context.Background(), "network", "", []byte(`{"version":1}`)); err != ErrInvalidID {
		t.Fatalf("PutNode with empty node ID error = %v, want ErrInvalidID", err)
	}
}

func TestDeleteNodeRemovesConfigAndRegistrationFiles(t *testing.T) {
	root := t.TempDir()
	store, err := Open(filepath.Join(root, "config.json"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.CreateNetwork(ctx, "net-a", json.RawMessage(`{"id":"net-a","name":"mesh","cidr":"10.90.0.0/24"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutNode(ctx, "net-a", "node-a", json.RawMessage(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterNode(ctx, "net-a", "node-a", json.RawMessage(`{"public_key":"key"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteNode(ctx, "net-a", "node-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetNode(ctx, "net-a", "node-a"); err != ErrNotFound {
		t.Fatalf("GetNode after delete = %v, want ErrNotFound", err)
	}
	if registrations, err := store.ListRegisteredNodes(ctx, "net-a"); err != nil || len(registrations) != 0 {
		t.Fatalf("registrations after delete = %#v, err = %v", registrations, err)
	}
	for _, path := range []string{
		filepath.Join(root, "nodes", "net-a", "node-a.yaml"),
		filepath.Join(root, "registrations", "net-a", "node-a.yaml"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("peer file %s still exists or stat failed: %v", path, err)
		}
	}
}

func TestDeleteNodeRestoresStagedFilesWhenLaterRemovalFails(t *testing.T) {
	root := t.TempDir()
	store, err := Open(filepath.Join(root, "config.json"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.CreateNetwork(ctx, "net-a", json.RawMessage(`{"id":"net-a","name":"mesh","cidr":"10.90.0.0/24"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutNode(ctx, "net-a", "node-a", json.RawMessage(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterNode(ctx, "net-a", "node-a", json.RawMessage(`{"public_key":"key"}`)); err != nil {
		t.Fatal(err)
	}
	registrationDir := filepath.Join(root, "registrations", "net-a")
	if err := os.Chmod(registrationDir, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteNode(ctx, "net-a", "node-a"); err == nil {
		t.Fatal("DeleteNode succeeded when registration directory was not writable")
	}
	if err := os.Chmod(registrationDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetNode(ctx, "net-a", "node-a"); err != nil {
		t.Fatalf("node config was not restored after failed deletion: %v", err)
	}
	if registrations, err := store.ListRegisteredNodes(ctx, "net-a"); err != nil || len(registrations) != 1 {
		t.Fatalf("registrations after failed deletion = %#v, err = %v", registrations, err)
	}
	for path, want := range map[string]string{
		filepath.Join(root, "nodes", "net-a", "node-a.yaml"):         `"version": 1`,
		filepath.Join(root, "registrations", "net-a", "node-a.yaml"): `"public_key": "key"`,
	} {
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), want) {
			t.Errorf("restored peer file %s = %q, err = %v, want to contain %q", path, data, err, want)
		}
	}
	if err := store.DeleteNode(ctx, "net-a", "node-a"); err != nil {
		t.Fatalf("retry DeleteNode: %v", err)
	}
	if registrations, err := store.ListRegisteredNodes(ctx, "net-a"); err != nil || len(registrations) != 0 {
		t.Fatalf("registrations after retry = %#v, err = %v", registrations, err)
	}
}

func TestOpenLoadsSidecarsWithoutIndex(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{filepath.Join(root, "nodes", "mesh"), filepath.Join(root, "registrations", "mesh")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "nodes", "mesh", "node-a.yaml"), []byte("version: 1\nname: legacy-node\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "registrations", "mesh", "node-a.yaml"), []byte("public_key: legacy-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(root, "config.json"), 2)
	if err != nil {
		t.Fatalf("open sidecar-only store: %v", err)
	}
	defer store.Close()
	if _, err := store.GetNode(context.Background(), "mesh", "node-a"); err != nil {
		t.Fatalf("node sidecar not loaded: %v", err)
	}
	if registrations, err := store.ListRegisteredNodes(context.Background(), "mesh"); err != nil || len(registrations) != 1 {
		t.Fatalf("registration sidecar not loaded: %#v, %v", registrations, err)
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
	got, err := store.GetNode(context.Background(), "net-a", "node-a")
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
	got, err := store.GetNode(context.Background(), "net-a", "node-a")
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
	got, err := store.GetNode(context.Background(), "net-a", "node-a")
	var decoded map[string]any
	if err == nil {
		err = json.Unmarshal(got, &decoded)
	}
	if err != nil || decoded["name"] != "new" {
		t.Fatalf("reopened duplicate-key node = %s, err = %v", got, err)
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
	got, err := store.GetNetworkMetadata(context.Background(), "mesh-a")
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
	registered, err := store.ListRegisteredNodes(context.Background(), "mesh-a")
	if err != nil || len(registered) != count {
		t.Fatalf("registered nodes after restart = %d, err = %v", len(registered), err)
	}
}
