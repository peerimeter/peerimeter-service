package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/peerimeter/peerimeter-service/internal/configstore"
)

func newTestAPI(t *testing.T, publicURL string, corsAllowedOrigins ...string) http.Handler {
	return newTestAPIWithHTTPS(t, publicURL, true, corsAllowedOrigins...)
}

func newTestAPIWithHTTPS(t *testing.T, publicURL string, requireHTTPS bool, corsAllowedOrigins ...string) http.Handler {
	t.Helper()
	store, err := configstore.Open(filepath.Join(t.TempDir(), "config.json"), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return New("test-static-key", publicURL, store, slog.New(slog.NewJSONHandler(io.Discard, nil)), requireHTTPS, corsAllowedOrigins...)
}

func TestCORSAllowsConfiguredOriginAndAuthorizationPreflight(t *testing.T) {
	handler := newTestAPI(t, "https://service.example.test", "https://dashboard.example.test")
	preflight := httptest.NewRequest(http.MethodOptions, "/api/v1/networks", nil)
	preflight.Header.Set("Origin", "https://DASHBOARD.example.test:443")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
	preflight.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, preflight)
	if result.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d, want 204: %s", result.Code, result.Body)
	}
	if got := result.Header().Get("Access-Control-Allow-Origin"); got != "https://dashboard.example.test" {
		t.Fatalf("allow-origin = %q", got)
	}
	if got := result.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("allow-credentials = %q, want true", got)
	}
	if got := result.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, DELETE, OPTIONS" {
		t.Fatalf("allow-methods = %q", got)
	}
	if got := result.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type" {
		t.Fatalf("allow-headers = %q", got)
	}

	get := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	get.Header.Set("Origin", "https://dashboard.example.test")
	getResult := httptest.NewRecorder()
	handler.ServeHTTP(getResult, get)
	if getResult.Code != http.StatusOK || getResult.Header().Get("Access-Control-Allow-Origin") != "https://dashboard.example.test" {
		t.Fatalf("allowed-origin GET = %d, headers %v", getResult.Code, getResult.Header())
	}
}

func TestDeletePeerRequiresManagementKeyAndRemovesPeer(t *testing.T) {
	handler := newTestAPI(t, "https://service.example")
	put := httptest.NewRequest(http.MethodPut, "/api/v1/networks/net-a/nodes/node-a/config", strings.NewReader(`{"version":1}`))
	put.Header.Set("Authorization", "Bearer test-static-key")
	putResult := httptest.NewRecorder()
	handler.ServeHTTP(putResult, put)
	if putResult.Code != http.StatusNoContent {
		t.Fatalf("PUT peer config = %d %s", putResult.Code, putResult.Body)
	}
	deletePeer := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/networks/net-a/nodes/node-a", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	if got := deletePeer("").Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated DELETE = %d, want 401", got)
	}
	if got := deletePeer("test-static-key").Code; got != http.StatusNoContent {
		t.Fatalf("DELETE peer = %d", got)
	}
	if got := deletePeer("test-static-key").Code; got != http.StatusNotFound {
		t.Fatalf("DELETE missing peer = %d, want 404", got)
	}
}

func TestCORSVariesResponsesWhenOriginIsAbsent(t *testing.T) {
	handler := newTestAPI(t, "https://service.example.test", "https://dashboard.example.test")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	if got := result.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want Origin", got)
	}
}

func TestCORSDoesNotAllowUnknownOrigin(t *testing.T) {
	handler := newTestAPI(t, "https://service.example.test", "https://dashboard.example.test")
	preflight := httptest.NewRequest(http.MethodOptions, "/api/v1/networks", nil)
	preflight.Header.Set("Origin", "https://untrusted.example.test")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, preflight)
	if result.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("untrusted origin was allowed: %v", result.Header())
	}
}

func TestHealthIsUnauthenticatedAndConfigIsProtected(t *testing.T) {
	handler := newTestAPI(t, "https://service.example")
	for _, tc := range []struct {
		path     string
		want     int
		wantBody string
	}{{"/healthz", http.StatusOK, "{\"success\":true}\n"}, {"/health", http.StatusOK, "{\"success\":true}\n"}, {"/api/v1/networks/net-a/config", http.StatusUnauthorized, ""}} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != tc.want {
			t.Errorf("GET %s status = %d, want %d", tc.path, res.Code, tc.want)
		}
		if tc.wantBody != "" && res.Body.String() != tc.wantBody {
			t.Errorf("GET %s body = %q, want %q", tc.path, res.Body.String(), tc.wantBody)
		}
	}
}

func TestAuthConfigAndPasswordLoginToggle(t *testing.T) {
	store, err := configstore.Open(filepath.Join(t.TempDir(), "config.json"), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewWithAuth("test-static-key", "https://service.example", store, slog.New(slog.NewJSONHandler(io.Discard, nil)), true, AuthOptions{PasswordLogin: false})
	config := httptest.NewRecorder()
	handler.ServeHTTP(config, httptest.NewRequest(http.MethodGet, "/api/v1/auth/config", nil))
	if config.Code != http.StatusOK || config.Body.String() != "{\"github_sso_enabled\":false,\"password_login_enabled\":false}\n" {
		t.Fatalf("auth config = %d %s", config.Code, config.Body)
	}
	protected := httptest.NewRequest(http.MethodGet, "/api/v1/networks", nil)
	protected.Header.Set("Authorization", "Bearer test-static-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, protected)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("disabled API key status = %d", response.Code)
	}
}

func TestGitHubSessionAuthorizesAPIWithoutAPIKey(t *testing.T) {
	store, err := configstore.Open(filepath.Join(t.TempDir(), "config.json"), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	githubConfig := GitHubConfig{ClientID: "client", SessionSecret: "session-secret"}
	auth := NewGitHubAuth(githubConfig)
	const sessionID = "test-session"
	auth.sessions[sessionHash(githubConfig.SessionSecret, sessionID)] = time.Now().Add(time.Hour)
	handler := NewWithAuth("test-static-key", "https://service.example", store, slog.New(slog.NewJSONHandler(io.Discard, nil)), true, AuthOptions{GitHub: githubConfig, GitHubAuth: auth}, "https://dashboard.example")
	apiRequest := httptest.NewRequest(http.MethodGet, "/api/v1/networks", nil)
	apiRequest.AddCookie(&http.Cookie{Name: authCookieName, Value: sessionID})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, apiRequest)
	if response.Code != http.StatusOK {
		t.Fatalf("GitHub session status = %d: %s", response.Code, response.Body)
	}
}

func TestGitHubSessionRequiresTrustedOriginForWrites(t *testing.T) {
	store, err := configstore.Open(filepath.Join(t.TempDir(), "config.json"), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	githubConfig := GitHubConfig{ClientID: "client", SessionSecret: "session-secret", DashboardURL: "https://dashboard.example"}
	auth := NewGitHubAuth(githubConfig)
	const sessionID = "test-session"
	auth.sessions[sessionHash(githubConfig.SessionSecret, sessionID)] = time.Now().Add(time.Hour)
	handler := NewWithAuth("test-static-key", "https://service.example", store, slog.New(slog.NewJSONHandler(io.Discard, nil)), true, AuthOptions{PasswordLogin: true, GitHub: githubConfig, GitHubAuth: auth}, "https://dashboard.example")
	createNetwork := func(origin string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/networks", strings.NewReader(`{"version":1,"id":"net-a","name":"mesh","cidr":"10.90.0.0/24"}`))
		request.AddCookie(&http.Cookie{Name: authCookieName, Value: sessionID})
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := createNetwork(""); response.Code != http.StatusForbidden {
		t.Fatalf("session write without Origin = %d, want 403", response.Code)
	}
	if response := createNetwork("https://untrusted.example"); response.Code != http.StatusForbidden {
		t.Fatalf("session write from untrusted origin = %d, want 403", response.Code)
	}
	if response := createNetwork("https://dashboard.example"); response.Code != http.StatusCreated {
		t.Fatalf("session write from dashboard origin = %d: %s", response.Code, response.Body)
	}
	requestWithBearer := httptest.NewRequest(http.MethodPut, "/api/v1/networks/net-a/config", strings.NewReader(`{"version":1,"name":"mesh"}`))
	requestWithBearer.Header.Set("Authorization", "Bearer test-static-key")
	requestWithBearer.Header.Set("Origin", "https://untrusted.example")
	requestWithBearer.AddCookie(&http.Cookie{Name: authCookieName, Value: sessionID})
	bearerResponse := httptest.NewRecorder()
	handler.ServeHTTP(bearerResponse, requestWithBearer)
	if bearerResponse.Code != http.StatusNoContent {
		t.Fatalf("bearer-authenticated write with session cookie = %d, want 204: %s", bearerResponse.Code, bearerResponse.Body)
	}
}

func TestOAuthStateIsBoundToStartingBrowser(t *testing.T) {
	store, err := configstore.Open(filepath.Join(t.TempDir(), "config.json"), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	githubConfig := GitHubConfig{ClientID: "client", SessionSecret: "session-secret", DashboardURL: "https://dashboard.example"}
	auth := NewGitHubAuth(githubConfig)
	handler := NewWithAuth("test-static-key", "https://service.example", store, slog.New(slog.NewJSONHandler(io.Discard, nil)), true, AuthOptions{GitHub: githubConfig, GitHubAuth: auth})
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/auth/github/login", nil))
	if login.Code != http.StatusFound {
		t.Fatalf("login status = %d", login.Code)
	}
	state := login.Result().Header.Get("Location")
	parsedLocation, err := url.Parse(state)
	if err != nil {
		t.Fatal(err)
	}
	state = parsedLocation.Query().Get("state")
	stateCookie := login.Result().Cookies()[0]
	if state == "" || stateCookie.Name != oauthStateCookieName {
		t.Fatalf("OAuth state or browser cookie missing: state=%q cookie=%v", state, stateCookie)
	}
	callbackURL := "/auth/github/callback?state=" + url.QueryEscape(state) + "&error=access_denied"
	callbackWithoutCookie := httptest.NewRequest(http.MethodGet, callbackURL, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, callbackWithoutCookie)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("callback without browser cookie = %d, want 401", response.Code)
	}
	if _, exists := auth.states[state]; !exists {
		t.Fatal("state was consumed by callback from the wrong browser")
	}
	callbackFromOriginalBrowser := httptest.NewRequest(http.MethodGet, callbackURL, nil)
	callbackFromOriginalBrowser.AddCookie(stateCookie)
	originalResponse := httptest.NewRecorder()
	handler.ServeHTTP(originalResponse, callbackFromOriginalBrowser)
	if originalResponse.Code != http.StatusUnauthorized {
		t.Fatalf("denied callback status = %d, want 401", originalResponse.Code)
	}
	if _, exists := auth.states[state]; exists {
		t.Fatal("valid browser callback did not consume state")
	}
}

func TestDecodeStrictJSON(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantError bool
	}{
		{name: "valid object", body: `{"name":"mesh"}`},
		{name: "unknown field", body: `{"name":"mesh","other":true}`, wantError: true},
		{name: "trailing value", body: `{"name":"mesh"} {}`, wantError: true},
		{name: "malformed object", body: `{"name":`, wantError: true},
		{name: "body exceeds limit", body: strings.Repeat(" ", maxConfigSize+1), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			response := httptest.NewRecorder()
			var destination struct {
				Name string `json:"name"`
			}
			err := decodeStrictJSON(response, request, &destination)
			if (err != nil) != test.wantError {
				t.Fatalf("decodeStrictJSON() error = %v, wantError %v", err, test.wantError)
			}
		})
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

func TestCreateAndListNetworkWithWireGuardCIDR(t *testing.T) {
	handler := newTestAPI(t, "https://service.example")
	const body = `{"version":1,"id":"net-auto","name":"automesh","cidr":"10.90.0.0/24"}`
	create := httptest.NewRequest(http.MethodPost, "/api/v1/networks", strings.NewReader(body))
	create.Header.Set("Authorization", "Bearer test-static-key")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create network status = %d: %s", created.Code, created.Body)
	}
	duplicate := httptest.NewRequest(http.MethodPost, "/api/v1/networks", strings.NewReader(body))
	duplicate.Header.Set("Authorization", "Bearer test-static-key")
	duplicateRes := httptest.NewRecorder()
	handler.ServeHTTP(duplicateRes, duplicate)
	if duplicateRes.Code != http.StatusConflict {
		t.Fatalf("duplicate network status = %d, want 409", duplicateRes.Code)
	}
	overlap := httptest.NewRequest(http.MethodPost, "/api/v1/networks", strings.NewReader(`{"version":1,"id":"net-overlap","name":"Overlap","cidr":"10.90.0.128/25"}`))
	overlap.Header.Set("Authorization", "Bearer test-static-key")
	overlapRes := httptest.NewRecorder()
	handler.ServeHTTP(overlapRes, overlap)
	if overlapRes.Code != http.StatusConflict {
		t.Fatalf("overlapping network CIDR status = %d, want 409", overlapRes.Code)
	}
	get := httptest.NewRequest(http.MethodGet, "/api/v1/networks/net-auto", nil)
	get.Header.Set("Authorization", "Bearer test-static-key")
	getRes := httptest.NewRecorder()
	handler.ServeHTTP(getRes, get)
	if getRes.Code != http.StatusOK || !strings.Contains(getRes.Body.String(), `"cidr":"10.90.0.0/24"`) {
		t.Fatalf("network config = %d %s", getRes.Code, getRes.Body)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/v1/networks", nil)
	list.Header.Set("Authorization", "Bearer test-static-key")
	listRes := httptest.NewRecorder()
	handler.ServeHTTP(listRes, list)
	if listRes.Code != http.StatusOK || !strings.Contains(listRes.Body.String(), `"net-auto"`) {
		t.Fatalf("network list = %d %s", listRes.Code, listRes.Body)
	}
}

func TestCreateNetworkRejectsInvalidCIDR(t *testing.T) {
	handler := newTestAPI(t, "https://service.example")
	for _, cidr := range []string{"10.90.0.1/24", "8.8.8.0/24", "10.0.0.0/7", "192.168.0.0/15", "10.90.0.0/31", "2001:db8::/64", "garbage"} {
		body := `{"version":1,"id":"net-auto","name":"automesh","cidr":"` + cidr + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/networks", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-static-key")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest {
			t.Errorf("POST cidr %q status = %d, want 400", cidr, res.Code)
		}
	}
}

func TestPutNodeAcceptsJSONDuplicateKeys(t *testing.T) {
	handler := newTestAPI(t, "https://service.example")
	req := httptest.NewRequest(http.MethodPut, "/api/v1/networks/net-auto/nodes/node-a/config", strings.NewReader(`{"version":1,"name":"old","name":"new"}`))
	req.Header.Set("Authorization", "Bearer test-static-key")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("PUT duplicate-key node = %d %s", res.Code, res.Body)
	}
}

func TestNodeRegistrationAllocatesUniqueAddressAndPersistsPeer(t *testing.T) {
	handler := newTestAPI(t, "https://service.example")
	create := httptest.NewRequest(http.MethodPost, "/api/v1/networks", strings.NewReader(`{"version":1,"id":"net-auto","name":"automesh","cidr":"10.90.0.0/24"}`))
	create.Header.Set("Authorization", "Bearer test-static-key")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create network status = %d: %s", created.Code, created.Body)
	}
	key := func(first byte) string {
		value := make([]byte, 32)
		value[0] = first
		return base64.StdEncoding.EncodeToString(value)
	}
	register := func(nodeID string, publicKey string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(nodeRegistration{Version: 1, PublicKey: publicKey, ListenPort: 51820, PrimaryInterface: "eth0", LocalAddresses: []string{"192.168.1.12/24"}})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/networks/net-auto/nodes/"+nodeID+"/registration", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer test-static-key")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	for _, node := range []struct {
		id  string
		key string
	}{{"node-a", key(1)}, {"node-b", key(2)}} {
		res := register(node.id, node.key)
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"tunnel_address":"10.90.0.`) {
			t.Fatalf("register %s = %d %s", node.id, res.Code, res.Body)
		}
	}
	first := register("node-a", key(1))
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"tunnel_address":"10.90.0.1"`) {
		t.Fatalf("re-register did not preserve address: %d %s", first.Code, first.Body)
	}
	configPut := httptest.NewRequest(http.MethodPut, "/api/v1/networks/net-auto/nodes/node-a/config", strings.NewReader(`{"version":1,"tunnel_address":"10.90.0.99"}`))
	configPut.Header.Set("Authorization", "Bearer test-static-key")
	configRes := httptest.NewRecorder()
	handler.ServeHTTP(configRes, configPut)
	if configRes.Code != http.StatusNoContent {
		t.Fatalf("put node config = %d %s", configRes.Code, configRes.Body)
	}
	first = register("node-a", key(1))
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"tunnel_address":"10.90.0.1"`) {
		t.Fatalf("node config overwrote registered address: %d %s", first.Code, first.Body)
	}
	conflict := register("node-a", key(3))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("different key under existing node ID = %d %s", conflict.Code, conflict.Body)
	}
	duplicateKey := register("node-c", key(1))
	if duplicateKey.Code != http.StatusConflict {
		t.Fatalf("duplicate public key = %d %s", duplicateKey.Code, duplicateKey.Body)
	}
	deletePeer := httptest.NewRequest(http.MethodDelete, "/api/v1/networks/net-auto/nodes/node-a", nil)
	deletePeer.Header.Set("Authorization", "Bearer test-static-key")
	deleteResult := httptest.NewRecorder()
	handler.ServeHTTP(deleteResult, deletePeer)
	if deleteResult.Code != http.StatusNoContent {
		t.Fatalf("delete registered peer = %d %s", deleteResult.Code, deleteResult.Body)
	}
	if replacement := register("node-a", key(3)); replacement.Code != http.StatusOK {
		t.Fatalf("register replacement key after delete = %d %s", replacement.Code, replacement.Body)
	}
	peers := httptest.NewRequest(http.MethodGet, "/api/v1/networks/net-auto/nodes/node-a/peers", nil)
	peers.Header.Set("Authorization", "Bearer test-static-key")
	peerRes := httptest.NewRecorder()
	handler.ServeHTTP(peerRes, peers)
	if peerRes.Code != http.StatusOK || !strings.Contains(peerRes.Body.String(), `"node-b"`) || strings.Contains(peerRes.Body.String(), `"node-a"`) {
		t.Fatalf("registered peers = %d %s", peerRes.Code, peerRes.Body)
	}
	allPeers := httptest.NewRequest(http.MethodGet, "/api/v1/networks/net-auto/peers", nil)
	allPeers.Header.Set("Authorization", "Bearer test-static-key")
	allPeersRes := httptest.NewRecorder()
	handler.ServeHTTP(allPeersRes, allPeers)
	if allPeersRes.Code != http.StatusOK || !strings.Contains(allPeersRes.Body.String(), `"node-a"`) || !strings.Contains(allPeersRes.Body.String(), `"node-b"`) {
		t.Fatalf("all registered peers = %d %s", allPeersRes.Code, allPeersRes.Body)
	}
}

func TestValidRegistration(t *testing.T) {
	keyBytes := make([]byte, 32)
	keyBytes[0] = 1
	valid := nodeRegistration{
		Version:          1,
		PublicKey:        base64.StdEncoding.EncodeToString(keyBytes),
		ListenPort:       51820,
		PrimaryInterface: "eth0",
		LocalAddresses:   []string{"192.168.1.12/24"},
		PublicEndpoint:   "203.0.113.1:51820",
	}
	tests := []struct {
		name         string
		registration nodeRegistration
		want         bool
	}{
		{name: "valid", registration: valid, want: true},
		{name: "invalid version", registration: func() nodeRegistration { r := valid; r.Version = 2; return r }()},
		{name: "empty key", registration: func() nodeRegistration {
			r := valid
			r.PublicKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
			return r
		}()},
		{name: "invalid interface", registration: func() nodeRegistration { r := valid; r.PrimaryInterface = "eth0\n"; return r }()},
		{name: "duplicate local prefix", registration: func() nodeRegistration {
			r := valid
			r.LocalAddresses = []string{"192.168.1.12/24", "192.168.1.12/24"}
			return r
		}()},
		{name: "invalid endpoint", registration: func() nodeRegistration { r := valid; r.PublicEndpoint = "10.0.0.1:0"; return r }()},
		{name: "invalid listen port", registration: func() nodeRegistration { r := valid; r.ListenPort = 0; return r }()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validRegistration(test.registration); got != test.want {
				t.Fatalf("validRegistration() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestEmptyNodeIDCannotOverwriteNetworkConfig(t *testing.T) {
	handler := newTestAPI(t, "https://service.example")
	put := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-static-key")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}

	const networkBody = `{"version":1,"name":"network"}`
	if res := put("/api/v1/networks/net-a/config", networkBody); res.Code != http.StatusNoContent {
		t.Fatalf("PUT network status = %d: %s", res.Code, res.Body)
	}
	if res := put("/api/v1/networks/net-a/nodes//config", `{"version":1,"name":"node"}`); res.Code != http.StatusBadRequest {
		t.Fatalf("PUT empty node ID status = %d, want 400: %s", res.Code, res.Body)
	}
	get := httptest.NewRequest(http.MethodGet, "/api/v1/networks/net-a/config", nil)
	get.Header.Set("Authorization", "Bearer test-static-key")
	getRes := httptest.NewRecorder()
	handler.ServeHTTP(getRes, get)
	if getRes.Code != http.StatusOK || getRes.Body.String() != networkBody {
		t.Fatalf("network config after empty node ID PUT = %d %s, want %s", getRes.Code, getRes.Body, networkBody)
	}
}

func TestRejectsPathLikeNetworkAndNodeIDs(t *testing.T) {
	handler := newTestAPI(t, "https://service.example")
	for _, path := range []string{
		"/api/v1/networks/../config",
		"/api/v1/networks/net-a/nodes/%2e%2e/config",
		"/api/v1/networks/net-a/nodes/node-a/peers",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test-static-key")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if path == "/api/v1/networks/net-a/nodes/node-a/peers" {
			if res.Code != http.StatusNotFound {
				t.Errorf("GET empty network peer list = %d, want 404", res.Code)
			}
		} else if res.Code != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want 400", path, res.Code)
		}
	}
}

func TestJoinBundleCarriesNetworkScopedToken(t *testing.T) {
	handler := newTestAPI(t, "https://service.example")
	create := httptest.NewRequest(http.MethodPost, "/api/v1/networks", strings.NewReader(`{"version":1,"id":"net-a","name":"mesh","cidr":"10.90.0.0/24"}`))
	create.Header.Set("Authorization", "Bearer test-static-key")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create network status = %d: %s", created.Code, created.Body)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/join-bundle?network_id=net-a", nil)
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
		Version   int    `json:"version"`
		APIURL    string `json:"api_url"`
		NetworkID string `json:"network_id"`
		Key       string `json:"enrollment_token"`
	}
	if err := json.Unmarshal(decoded, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Version != 1 || bundle.APIURL != "https://service.example" || bundle.NetworkID != "net-a" || bundle.Key != networkToken("test-static-key", "net-a") || bundle.Key == "test-static-key" {
		t.Fatalf("unexpected join bundle: %+v", bundle)
	}
}

func TestJoinBundleRequiresExistingNetwork(t *testing.T) {
	handler := newTestAPI(t, "https://service.example")
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/v1/join-bundle", http.StatusBadRequest},
		{"/api/v1/join-bundle?network_id=missing", http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer test-static-key")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != tc.want {
			t.Errorf("GET %s status = %d, want %d", tc.path, res.Code, tc.want)
		}
	}
}

func TestJoinBundleAllowsHTTPWhenHTTPSRequirementIsDisabled(t *testing.T) {
	handler := newTestAPIWithHTTPS(t, "http://service.example", false)
	create := httptest.NewRequest(http.MethodPost, "/api/v1/networks", strings.NewReader(`{"version":1,"id":"net-a","name":"mesh","cidr":"10.90.0.0/24"}`))
	create.Header.Set("Authorization", "Bearer test-static-key")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create network status = %d: %s", created.Code, created.Body)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/join-bundle?network_id=net-a", nil)
	req.Header.Set("Authorization", "Bearer test-static-key")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("loopback join bundle status = %d: %s", res.Code, res.Body)
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
	create := httptest.NewRequest(http.MethodPost, "/api/v1/networks", strings.NewReader(`{"version":1,"id":"net-a","name":"mesh","cidr":"10.90.0.0/24"}`))
	create.Header.Set("Authorization", "Bearer test-static-key")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/join-bundle?network_id=net-a", nil)
	req.Header.Set("Authorization", "Bearer test-static-key")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("join bundle status = %d, want 503", res.Code)
	}
}
