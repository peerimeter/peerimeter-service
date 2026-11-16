package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peerimeter/peerimeter-service/internal/configstore"
)

func newTestAPI(t *testing.T, publicURL string) http.Handler {
	t.Helper()
	store, err := configstore.Open(filepath.Join(t.TempDir(), "config.json"), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return New("test-static-key", publicURL, store, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func TestHealthIsUnauthenticatedAndConfigIsProtected(t *testing.T) {
	handler := newTestAPI(t, "https://service.example")
	for _, tc := range []struct {
		path string
		want int
	}{{"/healthz", http.StatusNoContent}, {"/api/v1/networks/net-a/config", http.StatusUnauthorized}} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != tc.want {
			t.Errorf("GET %s status = %d, want %d", tc.path, res.Code, tc.want)
		}
	}
}

func TestNetworkAndNodeConfigRoundTrip(t *testing.T) {
	handler := newTestAPI(t, "https://service.example")
	for _, tc := range []struct {
		path string
		body string
	}{{"/api/v1/networks/net-a/config", `{"version":1,"name":"mesh"}`}, {"/api/v1/networks/net-a/nodes/node-a/config", `{"version":1,"addresses":["10.0.0.2/32"]}`}} {
		put := httptest.NewRequest(http.MethodPut, tc.path, strings.NewReader(tc.body))
		put.Header.Set("Authorization", "Bearer test-static-key")
		putRes := httptest.NewRecorder()
		handler.ServeHTTP(putRes, put)
		if putRes.Code != http.StatusNoContent {
			t.Fatalf("PUT %s status = %d: %s", tc.path, putRes.Code, putRes.Body)
		}
		get := httptest.NewRequest(http.MethodGet, tc.path, nil)
		get.Header.Set("Authorization", "Bearer test-static-key")
		getRes := httptest.NewRecorder()
		handler.ServeHTTP(getRes, get)
		if getRes.Code != http.StatusOK || getRes.Body.String() != tc.body {
			t.Fatalf("GET %s = %d %s, want %s", tc.path, getRes.Code, getRes.Body, tc.body)
		}
	}
}

func TestJoinBundleCarriesStaticKey(t *testing.T) {
	handler := newTestAPI(t, "https://service.example")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/join-bundle", nil)
	req.Header.Set("Authorization", "Bearer test-static-key")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("join bundle status = %d: %s", res.Code, res.Body)
	}
	if res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("join bundle Cache-Control = %q, want no-store", res.Header().Get("Cache-Control"))
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(response.Token)
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Version int    `json:"version"`
		APIURL  string `json:"api_url"`
		Key     string `json:"enrollment_token"`
	}
	if err := json.Unmarshal(decoded, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Version != 1 || bundle.APIURL != "https://service.example" || bundle.Key != "test-static-key" {
		t.Fatalf("unexpected join bundle: %+v", bundle)
	}
}

func TestRejectInvalidJSONAndNonHTTPSJoinURL(t *testing.T) {
	handler := newTestAPI(t, "http://service.example")
	for _, body := range []string{`[]`, `null`, `not-json`, `{"version":2}`} {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/networks/net-a/config", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-static-key")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest {
			t.Errorf("PUT body %q status = %d, want 400", body, res.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/join-bundle", nil)
	req.Header.Set("Authorization", "Bearer test-static-key")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("join bundle status = %d, want 503", res.Code)
	}
}
