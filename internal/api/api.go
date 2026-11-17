package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/peerimeter/peerimeter-service/internal/configstore"
)

const maxConfigSize = 1 << 20

const configVersion = 1

type configStore interface {
	GetNetwork(string) (json.RawMessage, error)
	GetNetworkMetadata(string) (json.RawMessage, error)
	GetNode(string, string) (json.RawMessage, error)
	ListRegisteredNodes(string) (map[string]json.RawMessage, error)
	ListNetworks() map[string]json.RawMessage
	CreateNetwork(ctx context.Context, id string, value json.RawMessage) error
	RegisterNode(ctx context.Context, networkID, nodeID string, value json.RawMessage) (json.RawMessage, error)
	PutNetwork(ctx context.Context, id string, value json.RawMessage) error
	PutNode(ctx context.Context, networkID, nodeID string, value json.RawMessage) error
}

type API struct {
	key            string
	publicURL      string
	requireHTTPS   bool
	store          configStore
	logger         *slog.Logger
	allowedOrigins map[string]bool
}

func New(key, publicURL string, store *configstore.Store, logger *slog.Logger, requireHTTPS bool, corsAllowedOrigins ...string) http.Handler {
	a := &API{key: key, publicURL: strings.TrimRight(publicURL, "/"), requireHTTPS: requireHTTPS, store: store, logger: logger, allowedOrigins: make(map[string]bool)}
	for _, raw := range corsAllowedOrigins {
		if origin, err := normalizeOrigin(raw); err == nil {
			a.allowedOrigins[origin] = true
		}
	}
	r := chi.NewRouter()
	r.Use(a.cors)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	r.Group(func(r chi.Router) {
		r.Use(a.authenticate)
		r.Get("/api/v1/join-bundle", a.joinBundle)
		r.Get("/api/v1/networks", a.listNetworks)
		r.Post("/api/v1/networks", a.createNetwork)
		r.Get("/api/v1/networks/{networkID}", a.getNetworkMetadata)
		r.Get("/api/v1/networks/{networkID}/config", a.getNetwork)
		r.Get("/api/v1/networks/{networkID}/peers", a.listAllPeers)
		r.Put("/api/v1/networks/{networkID}/config", a.putNetwork)
		r.Get("/api/v1/networks/{networkID}/nodes/{nodeID}/config", a.getNode)
		r.Get("/api/v1/networks/{networkID}/nodes/{nodeID}/peers", a.listPeers)
		r.Put("/api/v1/networks/{networkID}/nodes/{nodeID}/registration", a.registerNode)
		r.Put("/api/v1/networks/{networkID}/nodes/{nodeID}/config", a.putNode)
	})
	return r
}

func (a *API) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if len(a.allowedOrigins) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		canonical, err := normalizeOrigin(origin)
		if err != nil || !a.allowedOrigins[canonical] {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", canonical)
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func normalizeOrigin(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	u, err := url.Parse(value)
	if err != nil {
		return "", errors.New("invalid CORS origin")
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || value == "null" || strings.Contains(value, "*") {
		return "", errors.New("invalid CORS origin")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		parsedPort, parseErr := strconv.Atoi(port)
		if parseErr != nil || parsedPort < 1 || parsedPort > 65535 {
			return "", errors.New("invalid CORS origin port")
		}
		return scheme + "://" + net.JoinHostPort(host, port), nil
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}

type nodeRegistration struct {
	Version          int      `json:"version"`
	PublicKey        string   `json:"public_key"`
	ListenPort       int      `json:"listen_port"`
	PrimaryInterface string   `json:"primary_interface"`
	LocalAddresses   []string `json:"local_addresses"`
	PublicEndpoint   string   `json:"public_endpoint,omitempty"`
}

func (a *API) registerNode(w http.ResponseWriter, r *http.Request) {
	networkID, nodeID := chi.URLParam(r, "networkID"), chi.URLParam(r, "nodeID")
	if !validID(networkID) || !validID(nodeID) {
		writeError(w, http.StatusBadRequest, "networkID and nodeID are invalid")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConfigSize))
	if err != nil {
		a.writePutResult(w, err)
		return
	}
	var registration nodeRegistration
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registration); err != nil || decoder.Decode(new(any)) != io.EOF || !validRegistration(registration) {
		writeError(w, http.StatusBadRequest, "node registration is invalid")
		return
	}
	canonical, _ := json.Marshal(registration)
	value, err := a.store.RegisterNode(r.Context(), networkID, nodeID, canonical)
	if err != nil {
		switch {
		case errors.Is(err, configstore.ErrNotFound):
			writeError(w, http.StatusNotFound, "network not found")
		case errors.Is(err, configstore.ErrNodeConflict):
			writeError(w, http.StatusConflict, "node ID is registered with another public key")
		case errors.Is(err, configstore.ErrDuplicateKey):
			writeError(w, http.StatusConflict, "public key is already registered in this network")
		case errors.Is(err, configstore.ErrNoAddress):
			writeError(w, http.StatusConflict, "network has no available node addresses")
		default:
			a.logger.Error("register node", "error", err)
			writeError(w, http.StatusInternalServerError, "could not register node")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(value)
}

func validRegistration(registration nodeRegistration) bool {
	key, err := base64.StdEncoding.Strict().DecodeString(registration.PublicKey)
	if registration.Version != 1 || err != nil || len(key) != 32 || allZero(key) || registration.PublicKey != base64.StdEncoding.EncodeToString(key) || registration.ListenPort < 1 || registration.ListenPort > 65535 || strings.TrimSpace(registration.PrimaryInterface) == "" || len(registration.PrimaryInterface) > 64 || strings.ContainsAny(registration.PrimaryInterface, "\r\n\x00") || len(registration.LocalAddresses) == 0 || len(registration.LocalAddresses) > 16 {
		return false
	}
	var local []netip.Prefix
	for _, raw := range registration.LocalAddresses {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix.Bits() == 0 || !prefix.Addr().IsGlobalUnicast() || prefix.Addr().IsLinkLocalUnicast() || prefix.Addr().IsLoopback() || prefix.Addr().IsMulticast() || prefix.Addr().IsUnspecified() || prefix.Addr().Is4In6() {
			return false
		}
		for _, previous := range local {
			if previous == prefix {
				return false
			}
		}
		local = append(local, prefix)
	}
	if registration.PublicEndpoint != "" {
		endpoint, err := netip.ParseAddrPort(registration.PublicEndpoint)
		if err != nil || endpoint.Port() == 0 || (!endpoint.Addr().IsGlobalUnicast() && !endpoint.Addr().IsLoopback()) || endpoint.Addr().IsLinkLocalUnicast() || endpoint.Addr().Zone() != "" || endpoint.Addr().Is4In6() {
			return false
		}
	}
	return true
}

func allZero(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}

type networkRequest struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	CIDR    string `json:"cidr"`
}

func (a *API) listNetworks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Version  int                        `json:"version"`
		Networks map[string]json.RawMessage `json:"networks"`
	}{Version: 1, Networks: a.store.ListNetworks()})
}

func (a *API) getNetworkMetadata(w http.ResponseWriter, r *http.Request) {
	networkID := chi.URLParam(r, "networkID")
	if !validID(networkID) {
		writeError(w, http.StatusBadRequest, "networkID is invalid")
		return
	}
	value, err := a.store.GetNetworkMetadata(networkID)
	a.writeConfig(w, value, err)
}

func (a *API) createNetwork(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConfigSize))
	if err != nil {
		a.writePutResult(w, err)
		return
	}
	var request networkRequest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "body must contain version, id, name and cidr")
		return
	}
	if request.Version != 1 || !validID(request.ID) || strings.TrimSpace(request.Name) == "" || len(request.Name) > 128 || strings.ContainsAny(request.Name, "\r\n\x00") {
		writeError(w, http.StatusBadRequest, "network version, id or name is invalid")
		return
	}
	prefix, err := netip.ParsePrefix(request.CIDR)
	if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || prefix.Bits() < 8 || prefix.Bits() > 30 || !privateIPv4Prefix(prefix) {
		writeError(w, http.StatusBadRequest, "cidr must be a canonical private IPv4 network between /8 and /30")
		return
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "network config is invalid")
		return
	}
	if err := a.store.CreateNetwork(r.Context(), request.ID, canonical); err != nil {
		if errors.Is(err, configstore.ErrExists) {
			writeError(w, http.StatusConflict, "network already exists")
			return
		}
		if errors.Is(err, configstore.ErrCIDROverlap) {
			writeError(w, http.StatusConflict, "network CIDR overlaps an existing network")
			return
		}
		a.writePutResult(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
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

func (a *API) listPeers(w http.ResponseWriter, r *http.Request) {
	networkID, selfID := chi.URLParam(r, "networkID"), chi.URLParam(r, "nodeID")
	if !validID(networkID) || !validID(selfID) {
		writeError(w, http.StatusBadRequest, "networkID and nodeID are invalid")
		return
	}
	nodes, err := a.store.ListRegisteredNodes(networkID)
	if errors.Is(err, configstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "network not found")
		return
	}
	if err != nil {
		a.logger.Error("list peers", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list peers")
		return
	}
	peers := make(map[string]json.RawMessage, len(nodes))
	for id, value := range nodes {
		if id != selfID {
			peers[id] = value
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Version int                        `json:"version"`
		Peers   map[string]json.RawMessage `json:"peers"`
	}{Version: 1, Peers: peers})
}

func (a *API) listAllPeers(w http.ResponseWriter, r *http.Request) {
	networkID := chi.URLParam(r, "networkID")
	if !validID(networkID) {
		writeError(w, http.StatusBadRequest, "networkID is invalid")
		return
	}
	nodes, err := a.store.ListRegisteredNodes(networkID)
	if errors.Is(err, configstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "network not found")
		return
	}
	if err != nil {
		a.logger.Error("list peers", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list peers")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Version int                        `json:"version"`
		Peers   map[string]json.RawMessage `json:"peers"`
	}{Version: 1, Peers: nodes})
}

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		masterOK := a.key != "" && len(provided) == len(a.key) && subtle.ConstantTimeCompare([]byte(provided), []byte(a.key)) == 1
		networkID := chi.URLParam(r, "networkID")
		if networkID == "" {
			networkID = r.URL.Query().Get("network_id")
		}
		token := networkToken(a.key, networkID)
		networkOK := networkID != "" && len(provided) == len(token) && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
		if a.key == "" || provided == r.Header.Get("Authorization") || (!masterOK && !networkOK) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func networkToken(key, networkID string) string {
	// Derive a deterministic credential without storing per-network secrets.
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte("peerimeter-enrollment-v1:" + networkID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *API) joinBundle(w http.ResponseWriter, r *http.Request) {
	networkID := r.URL.Query().Get("network_id")
	if !validID(networkID) {
		writeError(w, http.StatusBadRequest, "network_id query parameter is required and must be valid")
		return
	}
	if _, err := a.store.GetNetworkMetadata(networkID); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "network not found")
		} else {
			a.logger.Error("check join bundle network", "error", err)
			writeError(w, http.StatusInternalServerError, "could not create join bundle")
		}
		return
	}
	u, err := url.Parse(a.publicURL)
	if err != nil || !validPublicAPIURL(u, a.requireHTTPS) {
		writeError(w, http.StatusServiceUnavailable, "public API URL is not configured or does not satisfy the HTTPS requirement")
		return
	}
	data, err := json.Marshal(struct {
		Version         int    `json:"version"`
		APIURL          string `json:"api_url"`
		NetworkID       string `json:"network_id"`
		EnrollmentToken string `json:"enrollment_token"`
	}{Version: 1, APIURL: a.publicURL, NetworkID: networkID, EnrollmentToken: networkToken(a.key, networkID)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create join bundle")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		Token string `json:"token"`
	}{Token: base64.RawURLEncoding.EncodeToString(data)})
}

func validPublicAPIURL(u *url.URL, requireHTTPS bool) bool {
	if u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Scheme == "https" || (!requireHTTPS && u.Scheme == "http")
}

func (a *API) getNetwork(w http.ResponseWriter, r *http.Request) {
	networkID := chi.URLParam(r, "networkID")
	if !validID(networkID) {
		writeError(w, http.StatusBadRequest, "networkID is invalid")
		return
	}
	value, err := a.store.GetNetwork(networkID)
	a.writeConfig(w, value, err)
}

func (a *API) getNode(w http.ResponseWriter, r *http.Request) {
	networkID, nodeID := chi.URLParam(r, "networkID"), chi.URLParam(r, "nodeID")
	if !validID(networkID) || !validID(nodeID) {
		writeError(w, http.StatusBadRequest, "networkID and nodeID are invalid")
		return
	}
	value, err := a.store.GetNode(networkID, nodeID)
	a.writeConfig(w, value, err)
}

func (a *API) writeConfig(w http.ResponseWriter, value json.RawMessage, err error) {
	if errors.Is(err, configstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "configuration not found")
		return
	}
	if err != nil {
		a.logger.Error("read config", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read configuration")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(value)
}

func (a *API) putNetwork(w http.ResponseWriter, r *http.Request) {
	if !validID(chi.URLParam(r, "networkID")) {
		writeError(w, http.StatusBadRequest, "networkID is invalid")
		return
	}
	value, err := readConfig(r)
	if err == nil {
		err = a.store.PutNetwork(r.Context(), chi.URLParam(r, "networkID"), value)
	}
	a.writePutResult(w, err)
}

func (a *API) putNode(w http.ResponseWriter, r *http.Request) {
	if !validID(chi.URLParam(r, "networkID")) || !validID(chi.URLParam(r, "nodeID")) {
		writeError(w, http.StatusBadRequest, "networkID and nodeID are invalid")
		return
	}
	value, err := readConfig(r)
	if err == nil {
		err = a.store.PutNode(r.Context(), chi.URLParam(r, "networkID"), chi.URLParam(r, "nodeID"), value)
	}
	a.writePutResult(w, err)
}

func validID(value string) bool {
	if value == "" || len(value) > 128 || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

func readConfig(r *http.Request) (json.RawMessage, error) {
	data, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxConfigSize))
	if err != nil {
		return nil, err
	}
	var value map[string]json.RawMessage
	if !json.Valid(data) || json.Unmarshal(data, &value) != nil || value == nil {
		return nil, errors.New("body must be a JSON object with version 1")
	}
	var version int
	if err := json.Unmarshal(value["version"], &version); err != nil || version != configVersion {
		return nil, errors.New("body must be a JSON object with version 1")
	}
	return json.RawMessage(data), nil
}

func (a *API) writePutResult(w http.ResponseWriter, err error) {
	if err != nil {
		if errors.Is(err, configstore.ErrInvalidID) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "configuration exceeds 1 MiB")
			return
		}
		if strings.Contains(err.Error(), "JSON object") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		a.logger.Error("write config", "error", err)
		writeError(w, http.StatusInternalServerError, "could not write configuration")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
