package postgresstore

import "testing"

func TestValidateTLSMode(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "verified remote", url: "postgres://db.example:5432/peerimeter?sslmode=verify-full"},
		{name: "local compose", url: "postgres://postgres:5432/peerimeter?sslmode=disable"},
		{name: "local loopback", url: "postgres://localhost/peerimeter?sslmode=disable"},
		{name: "unverified remote", url: "postgres://db.example/peerimeter?sslmode=require", want: true},
		{name: "remote default", url: "postgres://db.example/peerimeter", want: true},
		{name: "malformed", url: "not a URL", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateTLSMode(tt.url); (err != nil) != tt.want {
				t.Fatalf("validateTLSMode() error = %v, want error %v", err, tt.want)
			}
		})
	}
}
