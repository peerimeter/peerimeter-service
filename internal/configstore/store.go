package configstore

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

var (
	ErrNotFound       = errors.New("configuration not found")
	ErrInvalidID      = errors.New("network or node ID is invalid")
	ErrExists         = errors.New("network already exists")
	ErrNoAddress      = errors.New("network has no available node addresses")
	ErrNodeConflict   = errors.New("node ID is already registered with a different public key")
	ErrDuplicateKey   = errors.New("WireGuard public key is already registered in this network")
	ErrCIDROverlap    = errors.New("network CIDR overlaps another network")
	ErrInvalidNetwork = errors.New("network configuration is invalid")
)

type operationKind uint8

const (
	operationNetwork operationKind = iota + 1
	operationNode
	operationCreateNetwork
	operationRegisterNode
)

type fileState struct {
	Version  int                `json:"version"`
	Networks map[string]network `json:"networks"`
}

type network struct {
	Config        json.RawMessage            `json:"config,omitempty"`
	Metadata      json.RawMessage            `json:"metadata,omitempty"`
	Nodes         map[string]json.RawMessage `json:"nodes,omitempty"`
	Registrations map[string]json.RawMessage `json:"registrations,omitempty"`
}

type write struct {
	kind      operationKind
	networkID string
	nodeID    string
	value     json.RawMessage
	response  chan json.RawMessage
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
	if err := s.migrateLegacyNodes(); err != nil {
		return nil, err
	}
	if err := s.loadNodeFiles(); err != nil {
		return nil, err
	}
	if err := s.loadRegistrationFiles(); err != nil {
		return nil, err
	}
	go s.run()
	return s, nil
}

func (s *Store) loadNodeFiles() error {
	root := s.path + ".nodes"
	networks, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read node config directory: %w", err)
	}
	for _, networkEntry := range networks {
		if !networkEntry.IsDir() {
			continue
		}
		networkID := networkEntry.Name()
		if !validID(networkID) {
			return fmt.Errorf("invalid network ID in node config directory")
		}
		entries, err := os.ReadDir(filepath.Join(root, networkID))
		if err != nil {
			return fmt.Errorf("read node config directory: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
				continue
			}
			nodeID := entry.Name()[:len(entry.Name())-len(".yaml")]
			if !validID(nodeID) {
				return fmt.Errorf("invalid node ID in node config directory")
			}
			data, err := os.ReadFile(filepath.Join(root, networkID, entry.Name()))
			if err != nil {
				return fmt.Errorf("read node config: %w", err)
			}
			var document any
			if err := yaml.Unmarshal(data, &document); err != nil {
				return fmt.Errorf("decode node YAML: %w", err)
			}
			value, err := json.Marshal(document)
			if err != nil {
				return fmt.Errorf("encode node config: %w", err)
			}
			network := s.state.Networks[networkID]
			if network.Nodes == nil {
				network.Nodes = make(map[string]json.RawMessage)
			}
			network.Nodes[nodeID] = value
			s.state.Networks[networkID] = network
		}
	}
	return nil
}

func (s *Store) loadRegistrationFiles() error {
	root := s.path + ".registrations"
	networks, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read node registration directory: %w", err)
	}
	for _, networkEntry := range networks {
		if !networkEntry.IsDir() {
			continue
		}
		networkID := networkEntry.Name()
		if !validID(networkID) {
			return fmt.Errorf("invalid network ID in registration directory")
		}
		entries, err := os.ReadDir(filepath.Join(root, networkID))
		if err != nil {
			return fmt.Errorf("read node registration directory: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
				continue
			}
			nodeID := entry.Name()[:len(entry.Name())-len(".yaml")]
			if !validID(nodeID) {
				return fmt.Errorf("invalid node ID in registration directory")
			}
			data, err := os.ReadFile(filepath.Join(root, networkID, entry.Name()))
			if err != nil {
				return fmt.Errorf("read node registration: %w", err)
			}
			var document any
			if err := yaml.Unmarshal(data, &document); err != nil {
				return fmt.Errorf("decode node registration YAML: %w", err)
			}
			value, err := json.Marshal(document)
			if err != nil {
				return fmt.Errorf("encode node registration: %w", err)
			}
			network := s.state.Networks[networkID]
			if network.Registrations == nil {
				network.Registrations = make(map[string]json.RawMessage)
			}
			network.Registrations[nodeID] = value
			s.state.Networks[networkID] = network
		}
	}
	return nil
}

func (s *Store) migrateLegacyNodes() error {
	for networkID, network := range s.state.Networks {
		for nodeID, value := range network.Nodes {
			if !validID(networkID) || !validID(nodeID) {
				return fmt.Errorf("invalid network or node ID in legacy configuration")
			}
			path := filepath.Join(s.path+".nodes", networkID, nodeID+".yaml")
			if _, err := os.Stat(path); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect legacy node config: %w", err)
			}
			if _, err := s.persistNode(networkID, nodeID, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) Close() {
	s.close.Do(func() {
		close(s.done)
		<-s.closed
	})
}

func (s *Store) GetNetwork(id string) (json.RawMessage, error) {
	if !validID(id) {
		return nil, ErrInvalidID
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.state.Networks[id]
	if !ok || len(n.Config) == 0 {
		return nil, ErrNotFound
	}
	return append(json.RawMessage(nil), n.Config...), nil
}

func (s *Store) GetNetworkMetadata(id string) (json.RawMessage, error) {
	if !validID(id) {
		return nil, ErrInvalidID
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.state.Networks[id]
	if !ok || len(n.Metadata) == 0 {
		return nil, ErrNotFound
	}
	return append(json.RawMessage(nil), n.Metadata...), nil
}

func (s *Store) GetNode(networkID, nodeID string) (json.RawMessage, error) {
	if !validID(networkID) || !validID(nodeID) {
		return nil, ErrInvalidID
	}
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

func (s *Store) ListRegisteredNodes(networkID string) (map[string]json.RawMessage, error) {
	if !validID(networkID) {
		return nil, ErrInvalidID
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.state.Networks[networkID]
	if !ok {
		return nil, ErrNotFound
	}
	result := make(map[string]json.RawMessage, len(n.Registrations))
	for id, value := range n.Registrations {
		result[id] = append(json.RawMessage(nil), value...)
	}
	return result, nil
}

func (s *Store) ListNetworks() map[string]json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]json.RawMessage, len(s.state.Networks))
	for id, network := range s.state.Networks {
		if len(network.Metadata) != 0 {
			result[id] = append(json.RawMessage(nil), network.Metadata...)
		}
	}
	return result
}

func (s *Store) CreateNetwork(ctx context.Context, id string, value json.RawMessage) error {
	if !validID(id) {
		return ErrInvalidID
	}
	return s.submit(ctx, write{kind: operationCreateNetwork, networkID: id, value: append(json.RawMessage(nil), value...), result: make(chan error, 1)})
}

// RegisterNode atomically allocates a tunnel address and persists the node
// document so concurrent registrations cannot receive the same address.
func (s *Store) RegisterNode(ctx context.Context, networkID, nodeID string, value json.RawMessage) (json.RawMessage, error) {
	if !validID(networkID) || !validID(nodeID) {
		return nil, ErrInvalidID
	}
	response := make(chan json.RawMessage, 1)
	err := s.submit(ctx, write{kind: operationRegisterNode, networkID: networkID, nodeID: nodeID, value: append(json.RawMessage(nil), value...), response: response, result: make(chan error, 1)})
	if err != nil {
		return nil, err
	}
	return <-response, nil
}

func (s *Store) PutNetwork(ctx context.Context, id string, value json.RawMessage) error {
	if !validID(id) {
		return ErrInvalidID
	}
	return s.submit(ctx, write{kind: operationNetwork, networkID: id, value: append(json.RawMessage(nil), value...), result: make(chan error, 1)})
}

func (s *Store) PutNode(ctx context.Context, networkID, nodeID string, value json.RawMessage) error {
	if !validID(networkID) || !validID(nodeID) {
		return ErrInvalidID
	}
	return s.submit(ctx, write{kind: operationNode, networkID: networkID, nodeID: nodeID, value: append(json.RawMessage(nil), value...), result: make(chan error, 1)})
}

func validID(value string) bool {
	if len(value) == 0 || len(value) > 128 || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return false
		}
	}
	return true
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
	if op.kind == operationCreateNetwork {
		if len(n.Metadata) != 0 {
			return ErrExists
		}
		prefix, err := networkPrefix(op.value)
		if err != nil {
			return ErrInvalidNetwork
		}
		for id, network := range s.state.Networks {
			if id == op.networkID || len(network.Metadata) == 0 {
				continue
			}
			previous, err := networkPrefix(network.Metadata)
			if err == nil && prefix.Overlaps(previous) {
				return ErrCIDROverlap
			}
		}
	}
	if op.kind == operationNetwork {
		n.Config = append(json.RawMessage(nil), op.value...)
	} else if op.kind == operationCreateNetwork {
		n.Metadata = append(json.RawMessage(nil), op.value...)
	} else if op.kind == operationRegisterNode {
		if len(n.Metadata) == 0 {
			return ErrNotFound
		}
		if n.Registrations == nil {
			n.Registrations = make(map[string]json.RawMessage)
		}
		registered, err := registerNodeDocument(n, op.value, op.nodeID)
		if err != nil {
			return err
		}
		n.Registrations[op.nodeID] = registered
		op.response <- append(json.RawMessage(nil), registered...)
	} else if op.kind == operationNode {
		if n.Nodes == nil {
			n.Nodes = make(map[string]json.RawMessage)
		}
		n.Nodes[op.nodeID] = append(json.RawMessage(nil), op.value...)
	} else {
		return fmt.Errorf("unknown config write operation")
	}
	next.Networks[op.networkID] = n
	var committed bool
	var err error
	if op.kind == operationNode {
		committed, err = s.persistNode(op.networkID, op.nodeID, op.value)
	} else if op.kind == operationRegisterNode {
		committed, err = s.persistRegistration(op.networkID, op.nodeID, n.Registrations[op.nodeID])
	} else {
		committed, err = s.persist(next)
	}
	if committed {
		s.state = next
	}
	if err != nil {
		return err
	}
	return nil
}

func networkPrefix(value json.RawMessage) (netip.Prefix, error) {
	var metadata struct {
		CIDR string `json:"cidr"`
	}
	if err := json.Unmarshal(value, &metadata); err != nil {
		return netip.Prefix{}, err
	}
	prefix, err := netip.ParsePrefix(metadata.CIDR)
	if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || prefix.Bits() < 8 || prefix.Bits() > 30 || !privateIPv4Prefix(prefix) {
		return netip.Prefix{}, ErrInvalidNetwork
	}
	return prefix, nil
}

func privateIPv4Prefix(prefix netip.Prefix) bool {
	for _, raw := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		block, _ := netip.ParsePrefix(raw)
		if block.Contains(prefix.Addr()) && prefix.Bits() >= block.Bits() {
			return true
		}
	}
	return false
}

func cloneNetwork(n network) network {
	out := network{Config: append(json.RawMessage(nil), n.Config...), Metadata: append(json.RawMessage(nil), n.Metadata...), Nodes: make(map[string]json.RawMessage, len(n.Nodes)), Registrations: make(map[string]json.RawMessage, len(n.Registrations))}
	for id, value := range n.Nodes {
		out.Nodes[id] = append(json.RawMessage(nil), value...)
	}
	for id, value := range n.Registrations {
		out.Registrations[id] = append(json.RawMessage(nil), value...)
	}
	return out
}

func (s *Store) persist(state fileState) (bool, error) {
	// Node documents are stored separately so an operator can inspect or back
	// them up independently. The root file remains the network-config index.
	diskState := fileState{Version: 1, Networks: make(map[string]network, len(state.Networks))}
	for id, n := range state.Networks {
		n = cloneNetwork(n)
		for nodeID := range n.Nodes {
			delete(n.Nodes, nodeID)
		}
		for nodeID := range n.Registrations {
			delete(n.Registrations, nodeID)
		}
		diskState.Networks[id] = n
	}
	data, err := json.MarshalIndent(diskState, "", "  ")
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

func (s *Store) persistNode(networkID, nodeID string, value json.RawMessage) (bool, error) {
	return s.persistYAML(s.path+".nodes", networkID, nodeID, value)
}

func (s *Store) persistRegistration(networkID, nodeID string, value json.RawMessage) (bool, error) {
	return s.persistYAML(s.path+".registrations", networkID, nodeID, value)
}

func (s *Store) persistYAML(root, networkID, nodeID string, value json.RawMessage) (bool, error) {
	var document any
	if err := yaml.Unmarshal(value, &document); err != nil {
		return false, fmt.Errorf("decode node config: %w", err)
	}
	data, err := yaml.Marshal(document)
	if err != nil {
		return false, fmt.Errorf("encode node YAML: %w", err)
	}
	dir := filepath.Join(root, networkID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return false, fmt.Errorf("create node config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".peerimeter-node-*.tmp")
	if err != nil {
		return false, fmt.Errorf("create node config temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return false, fmt.Errorf("protect node config temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, fmt.Errorf("write node config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, fmt.Errorf("sync node config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close node config: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, nodeID+".yaml")); err != nil {
		return false, fmt.Errorf("replace node config: %w", err)
	}
	directory, err := os.Open(dir)
	if err == nil {
		err = directory.Sync()
		_ = directory.Close()
	}
	if err != nil {
		return true, fmt.Errorf("sync node config directory: %w", err)
	}
	return true, nil
}

func registerNodeDocument(n network, registration json.RawMessage, nodeID string) (json.RawMessage, error) {
	var current map[string]json.RawMessage
	if err := json.Unmarshal(registration, &current); err != nil || current == nil {
		return nil, errors.New("invalid node registration")
	}
	if n.Registrations == nil {
		n.Registrations = make(map[string]json.RawMessage)
	}
	for id, previous := range n.Registrations {
		if id == nodeID {
			continue
		}
		var old map[string]json.RawMessage
		if json.Unmarshal(previous, &old) == nil && string(old["public_key"]) == string(current["public_key"]) {
			return nil, ErrDuplicateKey
		}
	}
	if previous, ok := n.Registrations[nodeID]; ok {
		var old map[string]json.RawMessage
		if err := json.Unmarshal(previous, &old); err == nil {
			if string(old["public_key"]) != string(current["public_key"]) {
				return nil, ErrNodeConflict
			}
			if len(old["tunnel_address"]) != 0 {
				current["tunnel_address"] = old["tunnel_address"]
			}
		}
	} else {
		if len(n.Registrations) >= 1024 {
			return nil, ErrNoAddress
		}
		var metadata struct {
			CIDR string `json:"cidr"`
		}
		if err := json.Unmarshal(n.Metadata, &metadata); err != nil {
			return nil, errors.New("network has no valid address pool")
		}
		prefix, err := netip.ParsePrefix(metadata.CIDR)
		if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || prefix.Bits() < 8 || prefix.Bits() > 30 {
			return nil, errors.New("network has no valid address pool")
		}
		used := make(map[netip.Addr]bool, len(n.Registrations))
		for _, raw := range n.Registrations {
			var entry struct {
				Address string `json:"tunnel_address"`
			}
			if json.Unmarshal(raw, &entry) == nil {
				if address, err := netip.ParseAddr(entry.Address); err == nil {
					used[address] = true
				}
			}
		}
		base := prefix.Masked().Addr()
		var address netip.Addr
		baseBytes := base.As4()
		baseNumber := binary.BigEndian.Uint32(baseBytes[:])
		broadcast := baseNumber | uint32((1<<(32-prefix.Bits()))-1)
		for number := baseNumber + 1; number < broadcast; number++ {
			var bytes [4]byte
			binary.BigEndian.PutUint32(bytes[:], number)
			candidate := netip.AddrFrom4(bytes)
			if !used[candidate] {
				address = candidate
				break
			}
		}
		if !address.IsValid() {
			return nil, ErrNoAddress
		}
		value, _ := json.Marshal(address.String())
		current["tunnel_address"] = value
	}
	current["node_id"], _ = json.Marshal(nodeID)
	return json.Marshal(current)
}
