package noderegistration

import (
	"encoding/json"
	"errors"
	"net/netip"
	"strings"

	"github.com/peerimeter/peerimeter-service/internal/networking"
)

var (
	ErrInvalidRegistration = errors.New("invalid node registration")
	ErrDuplicateKey        = errors.New("WireGuard public key is already registered in this network")
	ErrNodeConflict        = errors.New("node ID is already registered with a different public key")
	ErrNoAddress           = errors.New("network has no available node addresses")
	ErrInvalidAddressPool  = errors.New("network has no valid address pool")
)

const maximumRegistrations = 1024

// Document applies the registration rules shared by the persistence backends.
func Document(nodeID string, value, metadata json.RawMessage, registrations map[string]json.RawMessage) (json.RawMessage, error) {
	var current map[string]json.RawMessage
	if err := json.Unmarshal(value, &current); err != nil || current == nil {
		return nil, ErrInvalidRegistration
	}
	var publicKey string
	if err := json.Unmarshal(current["public_key"], &publicKey); err != nil || strings.TrimSpace(publicKey) == "" {
		return nil, ErrInvalidRegistration
	}

	for registeredNodeID, previous := range registrations {
		if registeredNodeID == nodeID {
			continue
		}
		var old map[string]json.RawMessage
		if json.Unmarshal(previous, &old) == nil && string(old["public_key"]) == string(current["public_key"]) {
			return nil, ErrDuplicateKey
		}
	}

	if previous, exists := registrations[nodeID]; exists {
		if !hasMatchingPublicKey(current, previous) {
			return nil, ErrNodeConflict
		}
		preserveExistingAddress(current, previous)
	} else {
		address, err := nextAvailableAddress(metadata, registrations)
		if err != nil {
			return nil, err
		}
		encodedAddress, _ := json.Marshal(address.String())
		current["tunnel_address"] = encodedAddress
	}
	encodedNodeID, _ := json.Marshal(nodeID)
	current["node_id"] = encodedNodeID
	return json.Marshal(current)
}

func hasMatchingPublicKey(current map[string]json.RawMessage, previous json.RawMessage) bool {
	var old map[string]json.RawMessage
	if err := json.Unmarshal(previous, &old); err != nil {
		return true
	}
	return string(old["public_key"]) == string(current["public_key"])
}

func preserveExistingAddress(current map[string]json.RawMessage, previous json.RawMessage) {
	var old map[string]json.RawMessage
	if err := json.Unmarshal(previous, &old); err == nil && len(old["tunnel_address"]) > 0 {
		current["tunnel_address"] = old["tunnel_address"]
	}
}

func nextAvailableAddress(metadata json.RawMessage, registrations map[string]json.RawMessage) (netip.Addr, error) {
	if len(registrations) >= maximumRegistrations {
		return netip.Addr{}, ErrNoAddress
	}
	prefix, err := networking.NetworkPrefix(metadata)
	if err != nil {
		return netip.Addr{}, ErrInvalidAddressPool
	}
	used := make(map[netip.Addr]bool, len(registrations))
	for _, registration := range registrations {
		var document map[string]json.RawMessage
		if json.Unmarshal(registration, &document) != nil {
			continue
		}
		var address string
		if json.Unmarshal(document["tunnel_address"], &address) != nil {
			continue
		}
		parsedAddress, err := netip.ParseAddr(address)
		if err == nil {
			used[parsedAddress] = true
		}
	}
	address, ok := networking.NextAddress(prefix, used)
	if !ok {
		return netip.Addr{}, ErrNoAddress
	}
	return address, nil
}
