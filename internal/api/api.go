package api

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/peerimeter/peerimeter-service/internal/configstore"
)

const maxConfigSize = 1 << 20

const configVersion = 1

type configStore interface {
	GetNetwork(string) (json.RawMessage, error)
	GetNode(string, string) (json.RawMessage, error)
	PutNetwork(ctx context.Context, id string, value json.RawMessage) error
	PutNode(ctx context.Context, networkID, nodeID string, value json.RawMessage) error
}

type API struct {
	key       string
	publicURL string
	store     configStore
	logger    *slog.Logger
}

func New(key, publicURL string, store *configstore.Store, logger *slog.Logger) http.Handler {
	a := &API{key: key, publicURL: strings.TrimRight(publicURL, "/"), store: store, logger: logger}
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	r.Group(func(r chi.Router) {
		r.Use(a.authenticate)
		r.Get("/api/v1/join-bundle", a.joinBundle)
		r.Get("/api/v1/networks/{networkID}/config", a.getNetwork)
		r.Put("/api/v1/networks/{networkID}/config", a.putNetwork)
		r.Get("/api/v1/networks/{networkID}/nodes/{nodeID}/config", a.getNode)
		r.Put("/api/v1/networks/{networkID}/nodes/{nodeID}/config", a.putNode)
	})
	return r
}

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if a.key == "" || provided == r.Header.Get("Authorization") || len(provided) != len(a.key) || subtle.ConstantTimeCompare([]byte(provided), []byte(a.key)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) joinBundle(w http.ResponseWriter, _ *http.Request) {
	u, err := url.Parse(a.publicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		writeError(w, http.StatusServiceUnavailable, "public HTTPS API URL is not configured")
		return
	}
	data, err := json.Marshal(struct {
		Version         int    `json:"version"`
		APIURL          string `json:"api_url"`
		EnrollmentToken string `json:"enrollment_token"`
	}{Version: 1, APIURL: a.publicURL, EnrollmentToken: a.key})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create join bundle")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		Token string `json:"token"`
	}{Token: base64.RawURLEncoding.EncodeToString(data)})
}

func (a *API) getNetwork(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.GetNetwork(chi.URLParam(r, "networkID"))
	a.writeConfig(w, value, err)
}

func (a *API) getNode(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.GetNode(chi.URLParam(r, "networkID"), chi.URLParam(r, "nodeID"))
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
	value, err := readConfig(r)
	if err == nil {
		err = a.store.PutNetwork(r.Context(), chi.URLParam(r, "networkID"), value)
	}
	a.writePutResult(w, err)
}

func (a *API) putNode(w http.ResponseWriter, r *http.Request) {
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
