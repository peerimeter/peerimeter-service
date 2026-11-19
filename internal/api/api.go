package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	corsorigin "github.com/peerimeter/peerimeter-service/internal/origin"
)

const maxConfigSize = 1 << 20

const configVersion = 1

var errInvalidConfig = errors.New("configuration must be a JSON object with version 1")

type configStore interface {
	GetNetwork(context.Context, string) (json.RawMessage, error)
	GetNetworkMetadata(context.Context, string) (json.RawMessage, error)
	GetNode(context.Context, string, string) (json.RawMessage, error)
	ListRegisteredNodes(context.Context, string) (map[string]json.RawMessage, error)
	ListNetworks(context.Context) (map[string]json.RawMessage, error)
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

func New(key, publicURL string, store configStore, logger *slog.Logger, requireHTTPS bool, corsAllowedOrigins ...string) http.Handler {
	a := &API{key: key, publicURL: strings.TrimRight(publicURL, "/"), requireHTTPS: requireHTTPS, store: store, logger: logger, allowedOrigins: make(map[string]bool)}
	for _, raw := range corsAllowedOrigins {
		if normalized, err := corsorigin.Normalize(raw); err == nil {
			a.allowedOrigins[normalized] = true
		}
	}
	r := chi.NewRouter()
	r.Use(a.cors)
	r.Get("/healthz", a.liveness)
	r.Get("/health", a.readiness)
	r.Group(func(r chi.Router) {
		r.Use(a.authenticate)
		r.Get("/api/v1/join-bundle", a.joinBundle)
		r.Get("/api/v1/networks", a.listNetworks)
		r.Post("/api/v1/networks", a.createNetwork)
		r.Get("/api/v1/networks/{networkID}", a.getNetworkMetadata)
		r.Get("/api/v1/networks/{networkID}/config", a.getNetwork)
		r.Get("/api/v1/networks/{networkID}/peers", a.listAllPeers)
		r.With(a.authenticateManagement).Put("/api/v1/networks/{networkID}/config", a.putNetwork)
		r.Get("/api/v1/networks/{networkID}/nodes/{nodeID}/config", a.getNode)
		r.Get("/api/v1/networks/{networkID}/nodes/{nodeID}/peers", a.listPeers)
		r.Put("/api/v1/networks/{networkID}/nodes/{nodeID}/registration", a.registerNode)
		r.Put("/api/v1/networks/{networkID}/nodes/{nodeID}/config", a.putNode)
	})
	return r
}

func (a *API) liveness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{\"success\":true}\n"))
}

func (a *API) readiness(w http.ResponseWriter, r *http.Request) {
	if checker, ok := a.store.(interface{ Ping(context.Context) error }); ok {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := checker.Ping(ctx); err != nil {
			a.logger.ErrorContext(ctx, "PostgreSQL health check failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"success": false,
				"error":   "database unavailable",
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
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
