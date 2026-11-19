package noderegistration

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDocumentPreservesAddressOnRepeatRegistration(t *testing.T) {
	registrations := map[string]json.RawMessage{
		"node-a": json.RawMessage(`{"public_key":"key-a","tunnel_address":"10.90.0.7"}`),
	}
	got, err := Document("node-a", json.RawMessage(`{"public_key":"key-a"}`), json.RawMessage(`{"cidr":"10.90.0.0/24"}`), registrations)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Address string `json:"tunnel_address"`
	}
	if err := json.Unmarshal(got, &document); err != nil {
		t.Fatal(err)
	}
	if document.Address != "10.90.0.7" {
		t.Fatalf("tunnel address = %q, want preserved address", document.Address)
	}
}

func TestDocumentRejectsKeyAndNodeConflicts(t *testing.T) {
	registrations := map[string]json.RawMessage{
		"node-a": json.RawMessage(`{"public_key":"key-a","tunnel_address":"10.90.0.2"}`),
	}
	tests := []struct {
		name    string
		nodeID  string
		value   string
		wantErr error
	}{
		{name: "duplicate key", nodeID: "node-b", value: `{"public_key":"key-a"}`, wantErr: ErrDuplicateKey},
		{name: "node key changed", nodeID: "node-a", value: `{"public_key":"key-b"}`, wantErr: ErrNodeConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Document(test.nodeID, json.RawMessage(test.value), json.RawMessage(`{"cidr":"10.90.0.0/24"}`), registrations)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}
