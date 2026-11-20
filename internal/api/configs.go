package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/peerimeter/peerimeter-service/internal/configstore"
	"github.com/peerimeter/peerimeter-service/internal/networking"
)

func (a *API) getNetwork(w http.ResponseWriter, r *http.Request) {
	networkID := chi.URLParam(r, "networkID")
	if !networking.ValidID(networkID) {
		writeError(w, http.StatusBadRequest, "networkID is invalid")
		return
	}
	value, err := a.store.GetNetwork(r.Context(), networkID)
	a.writeConfig(w, value, err)
}

func (a *API) getNode(w http.ResponseWriter, r *http.Request) {
	networkID, nodeID := chi.URLParam(r, "networkID"), chi.URLParam(r, "nodeID")
	if !networking.ValidID(networkID) || !networking.ValidID(nodeID) {
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
	if !networking.ValidID(chi.URLParam(r, "networkID")) {
		writeError(w, http.StatusBadRequest, "networkID is invalid")
		return
	}
	value, err := readConfig(w, r)
	if err == nil {
		err = a.store.PutNetwork(r.Context(), chi.URLParam(r, "networkID"), value)
	}
	a.writePutResult(w, err)
}

func (a *API) putNode(w http.ResponseWriter, r *http.Request) {
	if !networking.ValidID(chi.URLParam(r, "networkID")) || !networking.ValidID(chi.URLParam(r, "nodeID")) {
		writeError(w, http.StatusBadRequest, "networkID and nodeID are invalid")
		return
	}
	value, err := readConfig(w, r)
	if err == nil {
		err = a.store.PutNode(r.Context(), chi.URLParam(r, "networkID"), chi.URLParam(r, "nodeID"), value)
	}
	a.writePutResult(w, err)
}

func (a *API) deleteNode(w http.ResponseWriter, r *http.Request) {
	networkID, nodeID := chi.URLParam(r, "networkID"), chi.URLParam(r, "nodeID")
	if !networking.ValidID(networkID) || !networking.ValidID(nodeID) {
		writeError(w, http.StatusBadRequest, "networkID and nodeID are invalid")
		return
	}
	if err := a.store.DeleteNode(r.Context(), networkID, nodeID); err != nil {
		if errors.Is(err, configstore.ErrInvalidID) {
			writeError(w, http.StatusBadRequest, err.Error())
		} else if errors.Is(err, configstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "peer not found")
		} else {
			a.logger.Error("delete peer", "error", err)
			writeError(w, http.StatusInternalServerError, "could not delete peer")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func readConfig(w http.ResponseWriter, r *http.Request) (json.RawMessage, error) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConfigSize))
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

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConfigSize))
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
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
