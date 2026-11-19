package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/peerimeter/peerimeter-service/internal/configstore"
)

func (a *API) getNetwork(w http.ResponseWriter, r *http.Request) {
	networkID := chi.URLParam(r, "networkID")
	if !validID(networkID) {
		writeError(w, http.StatusBadRequest, "networkID is invalid")
		return
	}
	value, err := a.store.GetNetwork(r.Context(), networkID)
	a.writeConfig(w, value, err)
}

func (a *API) getNode(w http.ResponseWriter, r *http.Request) {
	networkID, nodeID := chi.URLParam(r, "networkID"), chi.URLParam(r, "nodeID")
	if !validID(networkID) || !validID(nodeID) {
		writeError(w, http.StatusBadRequest, "networkID and nodeID are invalid")
		return
	}
	value, err := a.store.GetNode(r.Context(), networkID, nodeID)
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

func readConfig(r *http.Request) (json.RawMessage, error) {
	data, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxConfigSize))
	if err != nil {
		return nil, err
	}
	var value map[string]json.RawMessage
	if !json.Valid(data) || json.Unmarshal(data, &value) != nil || value == nil {
		return nil, errInvalidConfig
	}
	var version int
	if err := json.Unmarshal(value["version"], &version); err != nil || version != configVersion {
		return nil, errInvalidConfig
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
		if errors.Is(err, errInvalidConfig) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		a.logger.Error("write config", "error", err)
		writeError(w, http.StatusInternalServerError, "could not write configuration")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
