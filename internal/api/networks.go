package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/peerimeter/peerimeter-service/internal/configstore"
	"github.com/peerimeter/peerimeter-service/internal/networking"
)

type networkRequest struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	CIDR    string `json:"cidr"`
}

func (a *API) listNetworks(w http.ResponseWriter, r *http.Request) {
	networks, err := a.store.ListNetworks(r.Context())
	if err != nil {
		a.logger.ErrorContext(r.Context(), "list networks", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list networks")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Version  int                        `json:"version"`
		Networks map[string]json.RawMessage `json:"networks"`
	}{Version: 1, Networks: networks})
}

func (a *API) getNetworkMetadata(w http.ResponseWriter, r *http.Request) {
	networkID := chi.URLParam(r, "networkID")
	if !networking.ValidID(networkID) {
		writeError(w, http.StatusBadRequest, "networkID is invalid")
		return
	}
	value, err := a.store.GetNetworkMetadata(r.Context(), networkID)
	a.writeConfig(w, value, err)
}

func (a *API) createNetwork(w http.ResponseWriter, r *http.Request) {
	var request networkRequest
	if !a.decodeJSONOrWriteError(w, r, &request, "body must contain version, id, name and cidr") {
		return
	}
	if !validNetworkRequest(request) {
		writeError(w, http.StatusBadRequest, "network version, id or name is invalid")
		return
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "network config is invalid")
		return
	}
	if _, err := networking.NetworkPrefix(json.RawMessage(canonical)); err != nil {
		writeError(w, http.StatusBadRequest, "cidr must be a canonical private IPv4 network between /8 and /30")
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

func validNetworkRequest(request networkRequest) bool {
	validName := strings.TrimSpace(request.Name) != "" && len(request.Name) <= 128 &&
		!strings.ContainsAny(request.Name, "\r\n\x00")
	return request.Version == 1 && networking.ValidID(request.ID) && validName
}
