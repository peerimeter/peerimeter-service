package postgresstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/peerimeter/peerimeter-service/internal/configstore"
)

func TestPostgresPersistenceAndConcurrentRegistration(t *testing.T) {
	dsn := os.Getenv("PEERIMETER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PEERIMETER_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	networkID := fmt.Sprintf("test-%d", time.Now().UnixNano())
	ctx := context.Background()
	importID := "import-" + networkID
	snapshot := configstore.Snapshot{Networks: map[string]configstore.SnapshotNetwork{
		importID: {
			Metadata:      json.RawMessage(fmt.Sprintf(`{"id":%q,"name":"Imported","cidr":"10.251.0.0/24"}`, importID)),
			Config:        json.RawMessage(`{"version":1,"name":"imported-config"}`),
			Nodes:         map[string]json.RawMessage{"imported-node": json.RawMessage(`{"version":1,"name":"imported-node"}`)},
			Registrations: map[string]json.RawMessage{"imported-node": json.RawMessage(`{"public_key":"imported-key","tunnel_address":"10.251.0.1"}`)},
		},
	}}
	if err := store.ImportSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("import file snapshot: %v", err)
	}
	t.Cleanup(func() { store.db.Exec("DELETE FROM networks WHERE id = ?", importID) })
	if _, err := store.GetNetwork(importID); err != nil {
		t.Fatalf("imported network config missing: %v", err)
	}
	if _, err := store.GetNode(importID, "imported-node"); err != nil {
		t.Fatalf("imported node config missing: %v", err)
	}
	imported, err := store.ListRegisteredNodes(importID)
	if err != nil || len(imported) != 1 {
		t.Fatalf("imported registrations = %d, err = %v", len(imported), err)
	}
	if err := store.db.Exec("DELETE FROM networks WHERE id = ?", importID).Error; err != nil {
		t.Fatal(err)
	}
	metadata := json.RawMessage(fmt.Sprintf(`{"id":%q,"name":"Integration test","cidr":"10.252.0.0/24"}`, networkID))
	if err := store.CreateNetwork(ctx, networkID, metadata); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.db.Exec("DELETE FROM networks WHERE id = ?", networkID) })
	if err := store.PutNetwork(ctx, networkID, json.RawMessage(`{"version":1,"nodes":[]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetNetwork(networkID); err != nil {
		t.Fatalf("network config was not persisted: %v", err)
	}

	const count = 12
	var wg sync.WaitGroup
	errCh := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := json.RawMessage(fmt.Sprintf(`{"public_key":"test-key-%d","listen_port":51820}`, i))
			if _, err := store.RegisterNode(ctx, networkID, fmt.Sprintf("node-%d", i), body); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	registrations, err := store.ListRegisteredNodes(networkID)
	if err != nil {
		t.Fatal(err)
	}
	if len(registrations) != count {
		t.Fatalf("got %d registrations, want %d", len(registrations), count)
	}
	addresses := make(map[string]bool)
	for _, raw := range registrations {
		var row struct {
			TunnelAddress string `json:"tunnel_address"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		if row.TunnelAddress == "" || addresses[row.TunnelAddress] {
			t.Fatalf("missing or duplicate allocated address %q", row.TunnelAddress)
		}
		addresses[row.TunnelAddress] = true
	}
	if err := store.PutNode(ctx, networkID, "node-0", json.RawMessage(`{"version":1,"name":"persisted"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetNode(networkID, "node-0"); err != nil {
		t.Fatalf("node config was not persisted: %v", err)
	}
}
