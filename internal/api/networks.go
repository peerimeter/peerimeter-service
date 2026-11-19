package api

import (
	"encoding/json"
	"errors"
	"io"
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
	if !validID(networkID) {
		writeError(w, http.StatusBadRequest, "networkID is invalid")
		return
	}
	value, err := a.store.GetNetworkMetadata(r.Context(), networkID)
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
	if _, err := networking.NetworkPrefix(json.RawMessage(data)); err != nil {
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
