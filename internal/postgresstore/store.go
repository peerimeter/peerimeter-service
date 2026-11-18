package postgresstore

import (
	"context"
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/peerimeter/peerimeter-service/internal/configstore"
	"github.com/pressly/goose/v3"
	"gorm.io/datatypes"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct{ db *gorm.DB }

type networkRow struct {
	ID       string `gorm:"primaryKey"`
	Metadata datatypes.JSON
	Config   datatypes.JSON
}

func (networkRow) TableName() string { return "networks" }

type nodeRow struct {
	NetworkID string `gorm:"primaryKey"`
	NodeID    string `gorm:"primaryKey"`
	Config    datatypes.JSON
}

func (nodeRow) TableName() string { return "nodes" }

type registrationRow struct {
	NetworkID string `gorm:"primaryKey"`
	NodeID    string `gorm:"primaryKey"`
	PublicKey string
	Document  datatypes.JSON
}

func (registrationRow) TableName() string { return "registrations" }

func Open(databaseURL string) (*Store, error) {
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get PostgreSQL connection pool: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	goose.SetBaseFS(migrationFiles)
	if err := goose.SetDialect("postgres"); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("configure PostgreSQL migrations: %w", err)
	}
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("apply PostgreSQL migrations: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

// ImportSnapshot copies the legacy file store into an empty PostgreSQL database.
// It is all-or-nothing and refuses to merge or overwrite an existing database.
func (s *Store) ImportSnapshot(ctx context.Context, snapshot configstore.Snapshot) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(0x70656572696d6574)).Error; err != nil {
			return err
		}
		if err := tx.Exec("LOCK TABLE nodes, registrations, networks IN EXCLUSIVE MODE").Error; err != nil {
			return err
		}
		for _, model := range []any{&networkRow{}, &nodeRow{}, &registrationRow{}} {
			var count int64
			if err := tx.Model(model).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return errors.New("PostgreSQL import requires an empty database")
			}
		}
		for id, network := range snapshot.Networks {
			row := networkRow{ID: id, Metadata: datatypes.JSON(network.Metadata), Config: datatypes.JSON(network.Config)}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("import network %q: %w", id, err)
			}
			for nodeID, value := range network.Nodes {
				if err := tx.Create(&nodeRow{NetworkID: id, NodeID: nodeID, Config: datatypes.JSON(value)}).Error; err != nil {
					return fmt.Errorf("import node %q/%q: %w", id, nodeID, err)
				}
			}
			for nodeID, value := range network.Registrations {
				var document struct {
					PublicKey string `json:"public_key"`
				}
				if err := json.Unmarshal(value, &document); err != nil || strings.TrimSpace(document.PublicKey) == "" {
					return fmt.Errorf("import registration %q/%q: invalid public key", id, nodeID)
				}
				row := registrationRow{NetworkID: id, NodeID: nodeID, PublicKey: document.PublicKey, Document: datatypes.JSON(value)}
				if err := tx.Create(&row).Error; err != nil {
					return fmt.Errorf("import registration %q/%q: %w", id, nodeID, err)
				}
			}
		}
		return nil
	})
}

func (s *Store) GetNetwork(id string) (json.RawMessage, error) {
	if !validID(id) {
		return nil, configstore.ErrInvalidID
	}
	var row networkRow
	err := s.db.Where("id = ? AND config IS NOT NULL", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, configstore.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(row.Config), nil
}
func (s *Store) GetNetworkMetadata(id string) (json.RawMessage, error) {
	if !validID(id) {
		return nil, configstore.ErrInvalidID
	}
	var row networkRow
	err := s.db.Where("id = ? AND metadata IS NOT NULL", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, configstore.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(row.Metadata), nil
}
func (s *Store) GetNode(networkID, nodeID string) (json.RawMessage, error) {
	if !validID(networkID) || !validID(nodeID) {
		return nil, configstore.ErrInvalidID
	}
	var row nodeRow
	err := s.db.Where("network_id = ? AND node_id = ?", networkID, nodeID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, configstore.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(row.Config), nil
}
func (s *Store) ListRegisteredNodes(networkID string) (map[string]json.RawMessage, error) {
	if !validID(networkID) {
		return nil, configstore.ErrInvalidID
	}
	var rows []registrationRow
	if err := s.db.Where("network_id = ?", networkID).Find(&rows).Error; err != nil {
		return nil, err
	}
	var exists int64
	if err := s.db.Model(&networkRow{}).Where("id = ?", networkID).Count(&exists).Error; err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, configstore.ErrNotFound
	}
	result := make(map[string]json.RawMessage, len(rows))
	for _, row := range rows {
		result[row.NodeID] = json.RawMessage(row.Document)
	}
	return result, nil
}
func (s *Store) ListNetworks() (map[string]json.RawMessage, error) {
	var rows []networkRow
	if err := s.db.Where("metadata IS NOT NULL").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]json.RawMessage, len(rows))
	for _, row := range rows {
		result[row.ID] = json.RawMessage(row.Metadata)
	}
	return result, nil
}
func (s *Store) CreateNetwork(ctx context.Context, id string, value json.RawMessage) error {
	if !validID(id) {
		return configstore.ErrInvalidID
	}
	prefix, err := networkPrefix(value)
	if err != nil {
		return configstore.ErrInvalidNetwork
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(0x70656572696d6574)).Error; err != nil {
			return err
		}
		var rows []networkRow
		if err := tx.Where("metadata IS NOT NULL").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if row.ID == id && len(row.Metadata) != 0 {
				return configstore.ErrExists
			}
			previous, err := networkPrefix(json.RawMessage(row.Metadata))
			if err == nil && prefix.Overlaps(previous) {
				return configstore.ErrCIDROverlap
			}
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.Assignments(map[string]any{"metadata": datatypes.JSON(value)})}).Create(&networkRow{ID: id, Metadata: datatypes.JSON(value)}).Error
	})
}
func (s *Store) RegisterNode(ctx context.Context, networkID, nodeID string, value json.RawMessage) (json.RawMessage, error) {
	if !validID(networkID) || !validID(nodeID) {
		return nil, configstore.ErrInvalidID
	}
	var output json.RawMessage
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var network networkRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND metadata IS NOT NULL", networkID).First(&network).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return configstore.ErrNotFound
		} else if err != nil {
			return err
		}
		var current map[string]json.RawMessage
		if err := json.Unmarshal(value, &current); err != nil || current == nil {
			return errors.New("invalid node registration")
		}
		var publicKey string
		if err := json.Unmarshal(current["public_key"], &publicKey); err != nil || strings.TrimSpace(publicKey) == "" {
			return errors.New("invalid node registration public key")
		}
		var rows []registrationRow
		if err := tx.Where("network_id = ?", networkID).Find(&rows).Error; err != nil {
			return err
		}
		var existing *registrationRow
		for i := range rows {
			if rows[i].NodeID == nodeID {
				existing = &rows[i]
				continue
			}
			if rows[i].PublicKey == publicKey {
				return configstore.ErrDuplicateKey
			}
		}
		if existing != nil {
			if existing.PublicKey != publicKey {
				return configstore.ErrNodeConflict
			}
			var old map[string]json.RawMessage
			if json.Unmarshal(existing.Document, &old) == nil && len(old["tunnel_address"]) > 0 {
				current["tunnel_address"] = old["tunnel_address"]
			}
		} else {
			if len(rows) >= 1024 {
				return configstore.ErrNoAddress
			}
			prefix, err := networkPrefix(json.RawMessage(network.Metadata))
			if err != nil {
				return errors.New("network has no valid address pool")
			}
			used := make(map[netip.Addr]bool, len(rows))
			for _, row := range rows {
				var old map[string]json.RawMessage
				if json.Unmarshal(row.Document, &old) == nil {
					var ip string
					if json.Unmarshal(old["tunnel_address"], &ip) == nil {
						if address, err := netip.ParseAddr(ip); err == nil {
							used[address] = true
						}
					}
				}
			}
			address, ok := nextAddress(prefix, used)
			if !ok {
				return configstore.ErrNoAddress
			}
			current["tunnel_address"], _ = json.Marshal(address.String())
		}
		current["node_id"], _ = json.Marshal(nodeID)
		encoded, err := json.Marshal(current)
		if err != nil {
			return err
		}
		output = encoded
		row := registrationRow{NetworkID: networkID, NodeID: nodeID, PublicKey: publicKey, Document: datatypes.JSON(encoded)}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "network_id"}, {Name: "node_id"}}, DoUpdates: clause.AssignmentColumns([]string{"public_key", "document"})}).Create(&row).Error
	})
	return output, err
}
func (s *Store) PutNetwork(ctx context.Context, id string, value json.RawMessage) error {
	if !validID(id) {
		return configstore.ErrInvalidID
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.Assignments(map[string]any{"config": datatypes.JSON(value)})}).Create(&networkRow{ID: id, Config: datatypes.JSON(value)}).Error
}
func (s *Store) PutNode(ctx context.Context, networkID, nodeID string, value json.RawMessage) error {
	if !validID(networkID) || !validID(nodeID) {
		return configstore.ErrInvalidID
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "network_id"}, {Name: "node_id"}}, DoUpdates: clause.AssignmentColumns([]string{"config"})}).Create(&nodeRow{NetworkID: networkID, NodeID: nodeID, Config: datatypes.JSON(value)}).Error
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
func networkPrefix(value json.RawMessage) (netip.Prefix, error) {
	var data struct {
		CIDR string `json:"cidr"`
	}
	if err := json.Unmarshal(value, &data); err != nil {
		return netip.Prefix{}, err
	}
	prefix, err := netip.ParsePrefix(data.CIDR)
	if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || prefix.Bits() < 8 || prefix.Bits() > 30 || !privateIPv4Prefix(prefix) {
		return netip.Prefix{}, configstore.ErrInvalidNetwork
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
func nextAddress(prefix netip.Prefix, used map[netip.Addr]bool) (netip.Addr, bool) {
	base := prefix.Masked().Addr().As4()
	first := binary.BigEndian.Uint32(base[:])
	last := first | uint32((1<<(32-prefix.Bits()))-1)
	for n := first + 1; n < last; n++ {
		var raw [4]byte
		binary.BigEndian.PutUint32(raw[:], n)
		ip := netip.AddrFrom4(raw)
		if !used[ip] {
			return ip, true
		}
	}
	return netip.Addr{}, false
}
