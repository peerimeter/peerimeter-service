package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/peerimeter/peerimeter-service/internal/configstore"
)

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

type nodeRegistration struct {
	Version          int      `json:"version"`
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
	if !validID(networkID) {
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
	if u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Scheme == "https" || (!requireHTTPS && u.Scheme == "http")
}
