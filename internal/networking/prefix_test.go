package networking

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
)

func TestValidID(t *testing.T) {
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "simple", value: "network-1", valid: true},
		{name: "maximum length", value: strings.Repeat("a", 128), valid: true},
		{name: "empty"},
		{name: "dot", value: "."},
		{name: "dot dot", value: ".."},
		{name: "too long", value: strings.Repeat("a", 129)},
		{name: "space", value: "node id"},
		{name: "slash", value: "node/id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ValidID(test.value); got != test.valid {
				t.Fatalf("ValidID(%q) = %v, want %v", test.value, got, test.valid)
			}
		})
	}
}

func TestHasOverlappingNetwork(t *testing.T) {
	tests := []struct {
		name     string
		metadata []json.RawMessage
		want     bool
	}{
		{name: "overlap", metadata: []json.RawMessage{json.RawMessage(`{"cidr":"10.20.0.0/24"}`)}, want: true},
		{name: "disjoint", metadata: []json.RawMessage{json.RawMessage(`{"cidr":"10.21.0.0/24"}`)}},
		{name: "invalid metadata ignored", metadata: []json.RawMessage{json.RawMessage(`{}`)}},
	}
	candidate := netip.MustParsePrefix("10.20.0.0/25")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := HasOverlappingNetwork(candidate, test.metadata); got != test.want {
				t.Fatalf("HasOverlappingNetwork() = %v, want %v", got, test.want)
			}
		})
	}
}
