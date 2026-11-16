package configstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	defer store.Close()
	if _, err := store.GetNetwork("network-a"); err != nil {
		t.Fatalf("network config missing after restart: %v", err)
	}
	for i := range count {
		if _, err := store.GetNode("network-a", fmt.Sprintf("node-%d", i)); err != nil {
			t.Fatalf("node %d missing after restart: %v", i, err)
		}
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
}
