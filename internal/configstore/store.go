package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var (
	ErrNotFound  = errors.New("configuration not found")
	ErrInvalidID = errors.New("network and node IDs must not be empty")
)

type operationKind uint8

const (
	operationNetwork operationKind = iota + 1
	operationNode
)

type fileState struct {
	Version  int                `json:"version"`
	Networks map[string]network `json:"networks"`
}

type network struct {
	Config json.RawMessage            `json:"config"`
	Nodes  map[string]json.RawMessage `json:"nodes"`
}

type write struct {
	kind      operationKind
	networkID string
	nodeID    string
	value     json.RawMessage
	result    chan error
}

// Store serializes writes through one bounded in-memory queue. It provides
// process-local ordering and atomic single-file replacement, not multi-process
// coordination.
type Store struct {
	path   string
	queue  chan write
	done   chan struct{}
	closed chan struct{}
	mu     sync.RWMutex
	state  fileState
	close  sync.Once
}

func Open(path string, queueSize int) (*Store, error) {
	if queueSize < 1 {
		return nil, fmt.Errorf("queue size must be positive")
	}
	s := &Store{
		path: path, queue: make(chan write, queueSize), done: make(chan struct{}),
		closed: make(chan struct{}),
		state:  fileState{Version: 1, Networks: make(map[string]network)},
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &s.state); err != nil {
			return nil, fmt.Errorf("decode config file: %w", err)
		}
		if s.state.Version != 1 || s.state.Networks == nil {
			return nil, fmt.Errorf("unsupported or invalid config file")
		}
	}
	go s.run()
	return s, nil
}

func (s *Store) Close() {
	s.close.Do(func() {
		close(s.done)
		<-s.closed
	})
}

func (s *Store) GetNetwork(id string) (json.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.state.Networks[id]
	if !ok || len(n.Config) == 0 {
		return nil, ErrNotFound
	}
	return append(json.RawMessage(nil), n.Config...), nil
}

func (s *Store) GetNode(networkID, nodeID string) (json.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.state.Networks[networkID]
	if !ok {
		return nil, ErrNotFound
	}
	value, ok := n.Nodes[nodeID]
	if !ok {
		return nil, ErrNotFound
	}
	return append(json.RawMessage(nil), value...), nil
}

func (s *Store) PutNetwork(ctx context.Context, id string, value json.RawMessage) error {
	if id == "" {
		return ErrInvalidID
	}
	return s.submit(ctx, write{kind: operationNetwork, networkID: id, value: append(json.RawMessage(nil), value...), result: make(chan error, 1)})
}

func (s *Store) PutNode(ctx context.Context, networkID, nodeID string, value json.RawMessage) error {
	if networkID == "" || nodeID == "" {
		return ErrInvalidID
	}
	return s.submit(ctx, write{kind: operationNode, networkID: networkID, nodeID: nodeID, value: append(json.RawMessage(nil), value...), result: make(chan error, 1)})
}

func (s *Store) submit(ctx context.Context, op write) error {
	select {
	case <-s.done:
		return os.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case s.queue <- op:
	}
	select {
	case err := <-op.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return os.ErrClosed
	}
}

func (s *Store) run() {
	defer close(s.closed)
	for {
		select {
		case <-s.done:
			for {
				select {
				case op := <-s.queue:
					op.result <- s.apply(op)
				default:
					return
				}
			}
		case op := <-s.queue:
			op.result <- s.apply(op)
		}
	}
}

func (s *Store) apply(op write) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := fileState{Version: 1, Networks: make(map[string]network, len(s.state.Networks))}
	for id, n := range s.state.Networks {
		next.Networks[id] = cloneNetwork(n)
	}
	n := next.Networks[op.networkID]
	if op.kind == operationNetwork {
		n.Config = append(json.RawMessage(nil), op.value...)
	} else if op.kind == operationNode {
		if n.Nodes == nil {
			n.Nodes = make(map[string]json.RawMessage)
		}
		n.Nodes[op.nodeID] = append(json.RawMessage(nil), op.value...)
	} else {
		return fmt.Errorf("unknown config write operation")
	}
	next.Networks[op.networkID] = n
	committed, err := s.persist(next)
	if committed {
		s.state = next
	}
	if err != nil {
		return err
	}
	return nil
}

func cloneNetwork(n network) network {
	out := network{Config: append(json.RawMessage(nil), n.Config...), Nodes: make(map[string]json.RawMessage, len(n.Nodes))}
	for id, value := range n.Nodes {
		out.Nodes[id] = append(json.RawMessage(nil), value...)
	}
	return out
}

func (s *Store) persist(state fileState) (bool, error) {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode config file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return false, fmt.Errorf("create config directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".peerimeter-config-*.tmp")
	if err != nil {
		return false, fmt.Errorf("create config temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return false, fmt.Errorf("protect config temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, fmt.Errorf("write config temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, fmt.Errorf("sync config temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close config temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return false, fmt.Errorf("replace config file: %w", err)
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	if err != nil {
		return true, fmt.Errorf("sync config directory: %w", err)
	}
	return true, nil
}
