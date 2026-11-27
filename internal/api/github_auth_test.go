package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestGitHubTeamMembershipRequiresActiveMember(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantMember bool
		wantError  bool
	}{
		{name: "active member", statusCode: http.StatusOK, body: `{"state":"active"}`, wantMember: true},
		{name: "pending member", statusCode: http.StatusOK, body: `{"state":"pending"}`},
		{name: "not a member", statusCode: http.StatusNotFound},
		{name: "GitHub unavailable", statusCode: http.StatusForbidden, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auth := NewGitHubAuth(GitHubConfig{Organization: "peerimeter", Team: "home"})
			auth.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != "https://api.github.com/orgs/peerimeter/teams/home/memberships/octocat" {
					t.Errorf("membership URL = %q", request.URL)
				}
				if request.Header.Get("Authorization") != "Bearer test-access-token" {
					t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
				}
				return &http.Response{
					StatusCode: test.statusCode,
					Status:     http.StatusText(test.statusCode),
					Body:       io.NopCloser(strings.NewReader(test.body)),
					Header:     make(http.Header),
				}, nil
			})}

			member, err := auth.isTeamMember(context.Background(), "test-access-token", "octocat")
			if (err != nil) != test.wantError {
				t.Fatalf("isTeamMember() error = %v, wantError %v", err, test.wantError)
			}
			if member != test.wantMember {
				t.Fatalf("isTeamMember() = %v, want %v", member, test.wantMember)
			}
		})
	}
}
