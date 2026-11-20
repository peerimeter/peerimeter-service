package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/peerimeter/peerimeter-service/internal/configstore"
	"github.com/peerimeter/peerimeter-service/internal/networking"
)

func (a *API) listPeers(w http.ResponseWriter, r *http.Request) {
	networkID, selfID := chi.URLParam(r, "networkID"), chi.URLParam(r, "nodeID")
	if !networking.ValidID(networkID) || !networking.ValidID(selfID) {
		writeError(w, http.StatusBadRequest, "networkID and nodeID are invalid")
		return
	}
	a.listPeersResponse(w, r, networkID, selfID)
}

func (a *API) listPeersResponse(w http.ResponseWriter, r *http.Request, networkID, excludedNodeID string) {
	nodes, err := a.store.ListRegisteredNodes(r.Context(), networkID)
	if errors.Is(err, configstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "network not found")
		return
	}
	if err != nil {
		a.logger.ErrorContext(r.Context(), "list peers", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list peers")
		return
	}
	peers := make(map[string]json.RawMessage, len(nodes))
	for id, value := range nodes {
		if excludedNodeID == "" || id != excludedNodeID {
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
	if !networking.ValidID(networkID) {
		writeError(w, http.StatusBadRequest, "networkID is invalid")
		return
	}
	a.listPeersResponse(w, r, networkID, "")
}
