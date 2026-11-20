package configstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/peerimeter/peerimeter-service/internal/networking"
	"github.com/peerimeter/peerimeter-service/internal/noderegistration"
	"gopkg.in/yaml.v3"
)

var (
	ErrNotFound       = errors.New("configuration not found")
	ErrInvalidID      = errors.New("network or node ID is invalid")
	ErrExists         = errors.New("network already exists")
	ErrNoAddress      = noderegistration.ErrNoAddress
	ErrNodeConflict   = noderegistration.ErrNodeConflict
	ErrDuplicateKey   = noderegistration.ErrDuplicateKey
	ErrCIDROverlap    = errors.New("network CIDR overlaps another network")
	ErrInvalidNetwork = errors.New("network configuration is invalid")
)

type operationKind uint8

const (
	operationNetwork operationKind = iota + 1
	operationNode
	operationCreateNetwork
	operationRegisterNode
	operationDeleteNode
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

// Snapshot is a deep copy of every document persisted by the file backend.
type Snapshot struct {
	Networks map[string]SnapshotNetwork
}

type SnapshotNetwork struct {
	Config        json.RawMessage
	Metadata      json.RawMessage
	Nodes         map[string]json.RawMessage
	Registrations map[string]json.RawMessage
}

// Empty reports whether the snapshot contains any persisted network, node, or registration data.
func (s Snapshot) Empty() bool {
	for _, network := range s.Networks {
		if len(network.Metadata) != 0 || len(network.Config) != 0 || len(network.Nodes) != 0 || len(network.Registrations) != 0 {
			return false
		}
	}
	return true
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
	path       string
	queue      chan write
	done       chan struct{}
	closed     chan struct{}
	mu         sync.RWMutex
	state      fileState
	close      sync.Once
	submitGate chan struct{}
}

func Open(path string, queueSize int) (*Store, error) {
	if queueSize < 1 {
		return nil, fmt.Errorf("queue size must be positive")
	}
	s := &Store{
		path: path, queue: make(chan write, queueSize), done: make(chan struct{}),
		closed: make(chan struct{}), submitGate: make(chan struct{}, 1),
		state: fileState{Version: 1, Networks: make(map[string]network)},
	}
	s.submitGate <- struct{}{}
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
	return loadYAMLDocuments(s.nodeDir(), "node config", func(networkID, nodeID string, value json.RawMessage) error {
		network := s.state.Networks[networkID]
		if network.Nodes == nil {
			network.Nodes = make(map[string]json.RawMessage)
		}
		network.Nodes[nodeID] = value
		s.state.Networks[networkID] = network
		return nil
	})
}

func (s *Store) loadRegistrationFiles() error {
	// Load old files first, then overlay the current directory so new files win
	// even if a previous write created only part of the current directory.
	for _, root := range []string{s.path + ".registrations", s.registrationDir()} {
		if err := loadYAMLDocuments(root, "node registration", func(networkID, nodeID string, value json.RawMessage) error {
			network := s.state.Networks[networkID]
			if network.Registrations == nil {
				network.Registrations = make(map[string]json.RawMessage)
			}
			network.Registrations[nodeID] = value
			s.state.Networks[networkID] = network
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func loadYAMLDocuments(root, label string, visit func(networkID, nodeID string, document json.RawMessage) error) error {
	networks, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s directory: %w", label, err)
	}
	for _, networkEntry := range networks {
		if !networkEntry.IsDir() {
			continue
		}
		networkID := networkEntry.Name()
		if !networking.ValidID(networkID) {
			return fmt.Errorf("invalid network ID in %s directory", label)
		}
		entries, err := os.ReadDir(filepath.Join(root, networkID))
		if err != nil {
			return fmt.Errorf("read %s directory: %w", label, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
				continue
			}
			nodeID := entry.Name()[:len(entry.Name())-len(".yaml")]
			if !networking.ValidID(nodeID) {
				return fmt.Errorf("invalid node ID in %s directory", label)
			}
			path := filepath.Join(root, networkID, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", label, err)
			}
			document, err := yamlDocumentToJSON(data)
			if err != nil {
				return fmt.Errorf("encode %s: %w", label, err)
			}
			if err := visit(networkID, nodeID, document); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) migrateLegacyNodes() error {
	for networkID, network := range s.state.Networks {
		for nodeID, value := range network.Nodes {
			if !networking.ValidID(networkID) || !networking.ValidID(nodeID) {
				return fmt.Errorf("invalid network or node ID in legacy configuration")
			}
			path := filepath.Join(s.nodeDir(), networkID, nodeID+".yaml")
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
		<-s.submitGate
		close(s.done)
		s.submitGate <- struct{}{}
		<-s.closed
	})
}

func (s *Store) GetNetwork(_ context.Context, id string) (json.RawMessage, error) {
	if !networking.ValidID(id) {
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

func (s *Store) GetNetworkMetadata(_ context.Context, id string) (json.RawMessage, error) {
	if !networking.ValidID(id) {
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

func (s *Store) GetNode(_ context.Context, networkID, nodeID string) (json.RawMessage, error) {
	if !networking.ValidID(networkID) || !networking.ValidID(nodeID) {
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

func (s *Store) ListRegisteredNodes(_ context.Context, networkID string) (map[string]json.RawMessage, error) {
	if !networking.ValidID(networkID) {
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

func (s *Store) ListNetworks(_ context.Context) (map[string]json.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]json.RawMessage, len(s.state.Networks))
	for id, network := range s.state.Networks {
		if len(network.Metadata) != 0 {
			result[id] = append(json.RawMessage(nil), network.Metadata...)
		}
	}
	return result, nil
}

// Snapshot returns all file-backed network, node, and registration documents.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := Snapshot{Networks: make(map[string]SnapshotNetwork, len(s.state.Networks))}
	for id, value := range s.state.Networks {
		copy := cloneNetwork(value)
		result.Networks[id] = SnapshotNetwork{Config: copy.Config, Metadata: copy.Metadata, Nodes: copy.Nodes, Registrations: copy.Registrations}
	}
	return result
}

func (s *Store) CreateNetwork(ctx context.Context, id string, value json.RawMessage) error {
	if !networking.ValidID(id) {
		return ErrInvalidID
	}
	return s.submit(ctx, write{kind: operationCreateNetwork, networkID: id, value: append(json.RawMessage(nil), value...), result: make(chan error, 1)})
}

// RegisterNode atomically allocates a tunnel address and persists the node
// document so concurrent registrations cannot receive the same address.
func (s *Store) RegisterNode(ctx context.Context, networkID, nodeID string, value json.RawMessage) (json.RawMessage, error) {
	if !networking.ValidID(networkID) || !networking.ValidID(nodeID) {
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
	if !networking.ValidID(id) {
		return ErrInvalidID
	}
	return s.submit(ctx, write{kind: operationNetwork, networkID: id, value: append(json.RawMessage(nil), value...), result: make(chan error, 1)})
}

func (s *Store) PutNode(ctx context.Context, networkID, nodeID string, value json.RawMessage) error {
	if !networking.ValidID(networkID) || !networking.ValidID(nodeID) {
		return ErrInvalidID
	}
	return s.submit(ctx, write{kind: operationNode, networkID: networkID, nodeID: nodeID, value: append(json.RawMessage(nil), value...), result: make(chan error, 1)})
}

func (s *Store) DeleteNode(ctx context.Context, networkID, nodeID string) error {
	if !networking.ValidID(networkID) || !networking.ValidID(nodeID) {
		return ErrInvalidID
	}
	return s.submit(ctx, write{kind: operationDeleteNode, networkID: networkID, nodeID: nodeID, result: make(chan error, 1)})
}

func (s *Store) submit(ctx context.Context, op write) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.submitGate:
	}
	defer func() { s.submitGate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-s.done:
		return os.ErrClosed
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.queue <- op:
	}
	return <-op.result
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
	switch op.kind {
	case operationNetwork:
		n.Config = append(json.RawMessage(nil), op.value...)
	case operationCreateNetwork:
		var err error
		n, err = s.applyCreateNetwork(op, n)
		if err != nil {
			return err
		}
	case operationRegisterNode:
		var err error
		n, err = applyRegisterNode(n, op)
		if err != nil {
			return err
		}
	case operationNode:
		if n.Nodes == nil {
			n.Nodes = make(map[string]json.RawMessage)
		}
		n.Nodes[op.nodeID] = append(json.RawMessage(nil), op.value...)
	case operationDeleteNode:
		if len(n.Registrations[op.nodeID]) == 0 && len(n.Nodes[op.nodeID]) == 0 {
			return ErrNotFound
		}
		delete(n.Nodes, op.nodeID)
		delete(n.Registrations, op.nodeID)
	default:
		return fmt.Errorf("unknown config write operation")
	}
	next.Networks[op.networkID] = n
	return s.persistOperation(op, next, n)
}

func (s *Store) applyCreateNetwork(op write, current network) (network, error) {
	if len(current.Metadata) != 0 {
		return network{}, ErrExists
	}
	prefix, err := networking.NetworkPrefix(op.value)
	if err != nil {
		return network{}, ErrInvalidNetwork
	}
	metadata := make([]json.RawMessage, 0, len(s.state.Networks))
	for id, existing := range s.state.Networks {
		if id != op.networkID && len(existing.Metadata) != 0 {
			metadata = append(metadata, existing.Metadata)
		}
	}
	if networking.HasOverlappingNetwork(prefix, metadata) {
		return network{}, ErrCIDROverlap
	}
	current.Metadata = append(json.RawMessage(nil), op.value...)
	return current, nil
}

func applyRegisterNode(current network, op write) (network, error) {
	if len(current.Metadata) == 0 {
		return network{}, ErrNotFound
	}
	if current.Registrations == nil {
		current.Registrations = make(map[string]json.RawMessage)
	}
	registered, err := registerNodeDocument(current, op.value, op.nodeID)
	if err != nil {
		return network{}, err
	}
	current.Registrations[op.nodeID] = registered
	op.response <- append(json.RawMessage(nil), registered...)
	return current, nil
}

func (s *Store) persistOperation(op write, next fileState, current network) error {
	var committed bool
	var err error
	switch op.kind {
	case operationNode:
		committed, err = s.persistNode(op.networkID, op.nodeID, op.value)
	case operationRegisterNode:
		committed, err = s.persistRegistration(op.networkID, op.nodeID, current.Registrations[op.nodeID])
	case operationDeleteNode:
		committed, err = s.deleteNodeFiles(op.networkID, op.nodeID, next)
	default:
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

func (s *Store) deleteNodeFiles(networkID, nodeID string, next fileState) (bool, error) {
	paths := []string{
		filepath.Join(s.nodeDir(), networkID, nodeID+".yaml"),
		filepath.Join(s.registrationDir(), networkID, nodeID+".yaml"),
		filepath.Join(s.path+".registrations", networkID, nodeID+".yaml"),
	}
	staged := make([]stagedRemoval, 0, len(paths))
	for _, path := range paths {
		removal, err := stageRemoval(path)
		if err != nil {
			return false, errors.Join(fmt.Errorf("stage peer document removal: %w", err), restoreStagedRemovals(staged))
		}
		if removal.staged {
			staged = append(staged, removal)
		}
	}
	for remainingNetworkID, network := range next.Networks {
		for remainingNodeID, registration := range network.Registrations {
			if _, err := s.persistRegistration(remainingNetworkID, remainingNodeID, registration); err != nil {
				return false, errors.Join(fmt.Errorf("persist remaining peer registration: %w", err), restoreStagedRemovals(staged))
			}
		}
	}
	committed, err := s.persist(next)
	if err != nil {
		if committed {
			for _, removal := range staged {
				_ = os.Remove(removal.backupPath)
			}
			return true, err
		}
		return committed, errors.Join(err, restoreStagedRemovals(staged))
	}
	for _, removal := range staged {
		_ = os.Remove(removal.backupPath)
	}
	return true, nil
}

type stagedRemoval struct {
	originalPath string
	backupPath   string
	staged       bool
}

func stageRemoval(path string) (stagedRemoval, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return stagedRemoval{}, nil
	} else if err != nil {
		return stagedRemoval{}, err
	}
	placeholder, err := os.CreateTemp(filepath.Dir(path), ".peerimeter-delete-*.tmp")
	if err != nil {
		return stagedRemoval{}, err
	}
	backupPath := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		_ = os.Remove(backupPath)
		return stagedRemoval{}, err
	}
	if err := os.Remove(backupPath); err != nil {
		return stagedRemoval{}, err
	}
	if err := os.Rename(path, backupPath); err != nil {
		return stagedRemoval{}, err
	}
	return stagedRemoval{originalPath: path, backupPath: backupPath, staged: true}, nil
}

func restoreStagedRemovals(staged []stagedRemoval) error {
	var rollbackErrors []error
	for i := len(staged) - 1; i >= 0; i-- {
		removal := staged[i]
		if err := os.Rename(removal.backupPath, removal.originalPath); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore peer document: %w", err))
		}
	}
	return errors.Join(rollbackErrors...)
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
	return writeFileAtomically(s.path, ".peerimeter-config-*.tmp", atomicWriteLabels{
		directory: "config", tempFile: "config temp file", operation: "config temp file",
	}, data)
}

func (s *Store) persistNode(networkID, nodeID string, value json.RawMessage) (bool, error) {
	return s.persistYAML(s.nodeDir(), networkID, nodeID, value)
}

func (s *Store) nodeDir() string {
	return filepath.Join(filepath.Dir(s.path), "nodes")
}

func (s *Store) persistRegistration(networkID, nodeID string, value json.RawMessage) (bool, error) {
	return s.persistYAML(s.registrationDir(), networkID, nodeID, value)
}

func (s *Store) registrationDir() string {
	return filepath.Join(filepath.Dir(s.path), "registrations")
}

func (s *Store) persistYAML(root, networkID, nodeID string, value json.RawMessage) (bool, error) {
	document, err := jsonDocumentToYAML(value)
	if err != nil {
		return false, fmt.Errorf("decode node config: %w", err)
	}
	data, err := yaml.Marshal(document)
	if err != nil {
		return false, fmt.Errorf("encode node YAML: %w", err)
	}
	targetPath := filepath.Join(root, networkID, nodeID+".yaml")
	return writeFileAtomically(targetPath, ".peerimeter-node-*.tmp", atomicWriteLabels{
		directory: "node config", tempFile: "node config temp file", operation: "node config",
	}, data)
}

type atomicWriteLabels struct {
	directory string
	tempFile  string
	operation string
}

func writeFileAtomically(targetPath, tempPattern string, labels atomicWriteLabels, data []byte) (bool, error) {
	directory := filepath.Dir(targetPath)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return false, fmt.Errorf("create %s directory: %w", labels.directory, err)
	}
	tmp, err := os.CreateTemp(directory, tempPattern)
	if err != nil {
		return false, fmt.Errorf("create %s: %w", labels.tempFile, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("protect %s: %w", labels.tempFile, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("write %s: %w", labels.operation, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("sync %s: %w", labels.operation, err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close %s: %w", labels.operation, err)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return false, fmt.Errorf("replace %s: %w", labels.operation, err)
	}
	dir, err := os.Open(directory)
	if err == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	if err != nil {
		return true, fmt.Errorf("sync %s directory: %w", labels.directory, err)
	}
	return true, nil
}

func jsonDocumentToYAML(value []byte) (*yaml.Node, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	content, err := jsonValueNode(decoded)
	if err != nil {
		return nil, err
	}
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{content}}, nil
}

func jsonValueNode(value any) (*yaml.Node, error) {
	scalar := func(tag, value string, style yaml.Style) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value, Style: style}
	}
	switch value := value.(type) {
	case nil:
		return scalar("!!null", "null", 0), nil
	case bool:
		return scalar("!!bool", strconv.FormatBool(value), 0), nil
	case string:
		return scalar("!!str", value, yaml.DoubleQuotedStyle), nil
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(string(value), ".eE") {
			tag = "!!float"
		}
		return scalar(tag, string(value), 0), nil
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range value {
			child, err := jsonValueNode(item)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, child)
		}
		return node, nil
	case map[string]any:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			item, err := jsonValueNode(value[key])
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, scalar("!!str", key, yaml.DoubleQuotedStyle), item)
		}
		return node, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value %T", value)
	}
}

func yamlDocumentToJSON(data []byte) (json.RawMessage, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	if len(document.Content) != 1 {
		return nil, errors.New("YAML document must have one root value")
	}
	value, err := yamlValueJSON(document.Content[0])
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	return json.RawMessage(encoded), err
}

func yamlValueJSON(node *yaml.Node) (any, error) {
	switch node.Kind {
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return nil, errors.New("invalid YAML mapping")
		}
		value := make(map[string]any, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return nil, errors.New("YAML mapping keys must be strings")
			}
			item, err := yamlValueJSON(node.Content[i+1])
			if err != nil {
				return nil, err
			}
			value[key.Value] = item
		}
		return value, nil
	case yaml.SequenceNode:
		value := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			item, err := yamlValueJSON(child)
			if err != nil {
				return nil, err
			}
			value = append(value, item)
		}
		return value, nil
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!null":
			return nil, nil
		case "!!bool":
			return strconv.ParseBool(node.Value)
		case "!!int", "!!float":
			if !json.Valid([]byte(node.Value)) || strings.ContainsAny(node.Value, " \t\r\n") {
				return nil, errors.New("YAML numeric value is not a JSON number")
			}
			return json.Number(node.Value), nil
		case "!!str", "!":
			return node.Value, nil
		default:
			return nil, fmt.Errorf("unsupported YAML scalar tag %q", node.Tag)
		}
	default:
		return nil, fmt.Errorf("unsupported YAML node kind %d", node.Kind)
	}
}

func registerNodeDocument(n network, registration json.RawMessage, nodeID string) (json.RawMessage, error) {
	if n.Registrations == nil {
		n.Registrations = make(map[string]json.RawMessage)
	}
	return noderegistration.Document(nodeID, registration, n.Metadata, n.Registrations)
}
