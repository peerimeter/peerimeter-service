package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/peerimeter/peerimeter-service/internal/configstore"
	"github.com/peerimeter/peerimeter-service/internal/networking"
)

func (a *API) registerNode(w http.ResponseWriter, r *http.Request) {
	networkID, nodeID := chi.URLParam(r, "networkID"), chi.URLParam(r, "nodeID")
	if !networking.ValidID(networkID) || !networking.ValidID(nodeID) {
		writeError(w, http.StatusBadRequest, "networkID and nodeID are invalid")
		return
	}
	var registration nodeRegistration
	if !a.decodeJSONOrWriteError(w, r, &registration, "node registration is invalid") {
		return
	}
	if !validRegistration(registration) {
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
	return registration.Version == 1 && validAgentCLIVersion(registration.AgentCLIVersion) &&
		validWireGuardKey(registration.PublicKey) && validListenPort(registration.ListenPort) &&
		validPrimaryInterface(registration.PrimaryInterface) && validLocalPrefixes(registration.LocalAddresses) &&
		validPublicEndpoint(registration.PublicEndpoint)
}

func validAgentCLIVersion(version string) bool {
	return len(version) <= 64 && !strings.ContainsAny(version, "\r\n\x00")
}

func validListenPort(port int) bool {
	return port >= 1 && port <= 65535
}

func validWireGuardKey(raw string) bool {
	key, err := base64.StdEncoding.Strict().DecodeString(raw)
	return err == nil && len(key) == 32 && !allZero(key) && raw == base64.StdEncoding.EncodeToString(key)
}

func validPrimaryInterface(name string) bool {
	return strings.TrimSpace(name) != "" && len(name) <= 64 && !strings.ContainsAny(name, "\r\n\x00")
}

func validLocalPrefixes(addresses []string) bool {
	if len(addresses) == 0 || len(addresses) > 16 {
		return false
	}
	var local []netip.Prefix
	for _, raw := range addresses {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || !validUnicastPrefix(prefix) {
			return false
		}
		for _, previous := range local {
			if previous == prefix {
				return false
			}
		}
		local = append(local, prefix)
	}
	return true
}

func validUnicastPrefix(prefix netip.Prefix) bool {
	address := prefix.Addr()
	return prefix.Bits() != 0 && address.IsGlobalUnicast() && !address.IsLinkLocalUnicast() &&
		!address.IsLoopback() && !address.IsMulticast() && !address.IsUnspecified() && !address.Is4In6()
}

func validPublicEndpoint(raw string) bool {
	if raw == "" {
		return true
	}
	endpoint, err := netip.ParseAddrPort(raw)
	if err != nil || endpoint.Port() == 0 {
		return false
	}
	address := endpoint.Addr()
	return (address.IsGlobalUnicast() || address.IsLoopback()) &&
		!address.IsLinkLocalUnicast() && address.Zone() == "" && !address.Is4In6()
}

func allZero(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}

type nodeRegistration struct {
	Version          int      `json:"version"`
	AgentCLIVersion  string   `json:"agent_cli_version,omitempty"`
	PublicKey        string   `json:"public_key"`
	ListenPort       int      `json:"listen_port"`
	PrimaryInterface string   `json:"primary_interface"`
	LocalAddresses   []string `json:"local_addresses"`
	PublicEndpoint   string   `json:"public_endpoint,omitempty"`
}

func networkToken(key, networkID string) string {
	// Derive a deterministic credential without storing per-network secrets.
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte("peerimeter-enrollment-v1:" + networkID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *API) joinBundle(w http.ResponseWriter, r *http.Request) {
	networkID := r.URL.Query().Get("network_id")
	if !networking.ValidID(networkID) {
		writeError(w, http.StatusBadRequest, "network_id query parameter is required and must be valid")
		return
	}
	if _, err := a.store.GetNetworkMetadata(r.Context(), networkID); err != nil {
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
	if u == nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Scheme == "https" || (!requireHTTPS && u.Scheme == "http")
}
