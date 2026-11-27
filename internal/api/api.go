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

var serviceVersion = "dev"

var errInvalidConfig = errors.New("configuration must be a JSON object with version 1")

type ConfigStore interface {
	GetNetwork(context.Context, string) (json.RawMessage, error)
	GetNetworkMetadata(context.Context, string) (json.RawMessage, error)
	GetNode(context.Context, string, string) (json.RawMessage, error)
	ListRegisteredNodes(context.Context, string) (map[string]json.RawMessage, error)
	ListNetworks(context.Context) (map[string]json.RawMessage, error)
	CreateNetwork(ctx context.Context, id string, value json.RawMessage) error
	RegisterNode(ctx context.Context, networkID, nodeID string, value json.RawMessage) (json.RawMessage, error)
	DeleteNode(context.Context, string, string) error
	PutNetwork(ctx context.Context, id string, value json.RawMessage) error
	PutNode(ctx context.Context, networkID, nodeID string, value json.RawMessage) error
}

type API struct {
	key            string
	publicURL      string
	requireHTTPS   bool
	store          ConfigStore
	logger         *slog.Logger
	allowedOrigins map[string]bool
	auth           *GitHubAuth
	passwordLogin  bool
	sessionSecret  string
}

type AuthOptions struct {
	PasswordLogin bool
	SessionSecret string
	GitHub        GitHubConfig
	GitHubAuth    *GitHubAuth
}

func New(key, publicURL string, store ConfigStore, logger *slog.Logger, requireHTTPS bool, corsAllowedOrigins ...string) http.Handler {
	return NewWithAuth(key, publicURL, store, logger, requireHTTPS, AuthOptions{PasswordLogin: true}, corsAllowedOrigins...)
}

func NewWithAuth(key, publicURL string, store ConfigStore, logger *slog.Logger, requireHTTPS bool, authOptions AuthOptions, corsAllowedOrigins ...string) http.Handler {
	sessionSecret := authOptions.SessionSecret
	if sessionSecret == "" {
		sessionSecret = authOptions.GitHub.SessionSecret
	}
	if sessionSecret == "" {
		sessionSecret = deriveSessionSecret(key)
	}
	a := &API{key: key, publicURL: strings.TrimRight(publicURL, "/"), requireHTTPS: requireHTTPS, store: store, logger: logger, allowedOrigins: make(map[string]bool), passwordLogin: authOptions.PasswordLogin, sessionSecret: sessionSecret}
	if authOptions.GitHub.Enabled() {
		a.auth = authOptions.GitHubAuth
		if a.auth == nil {
			a.auth = NewGitHubAuth(authOptions.GitHub)
		}
	}
	for _, raw := range corsAllowedOrigins {
		if normalized, err := corsorigin.Normalize(raw); err == nil {
			a.allowedOrigins[normalized] = true
		}
	}
	r := chi.NewRouter()
	r.Use(a.cors)
	r.Get("/healthz", a.liveness)
	r.Get("/health", a.readiness)
	r.Get("/api/v1/auth/config", a.getAuthConfig)
	r.Post("/api/v1/auth/login", a.authAPIKeyLogin)
	r.Get("/api/v1/auth/session", a.getAuthSession)
	r.Get("/auth/github/login", a.authGitHubLogin)
	r.Get("/auth/github/callback", a.authGitHubCallback)
	r.Post("/api/v1/auth/logout", a.authLogout)
	r.Group(func(r chi.Router) {
		r.Use(a.authenticate)
		r.Get("/api/v1/version", a.getVersion)
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
		r.With(a.authenticateManagement).Delete("/api/v1/networks/{networkID}/nodes/{nodeID}", a.deleteNode)
	})
	return r
}

func (a *API) getAuthConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"password_login_enabled": a.passwordLogin, "github_sso_enabled": a.auth != nil})
}

func (a *API) authAPIKeyLogin(w http.ResponseWriter, r *http.Request) {
	if !a.passwordLogin {
		writeError(w, http.StatusForbidden, "password login is disabled")
		return
	}
	var request struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil || !constantTimeEqual(request.APIKey, a.key) {
		writeError(w, http.StatusUnauthorized, "invalid API key")
		return
	}
	claims := sessionClaims{Username: "superadmin", AuthMethod: "api_key", ExpiresAt: time.Now().Add(authSessionLifetime).Unix()}
	if err := a.setSessionCookie(w, claims); err != nil {
		a.logger.ErrorContext(r.Context(), "create API key session", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	writeJSON(w, http.StatusOK, claims)
}

func (a *API) getAuthSession(w http.ResponseWriter, r *http.Request) {
	claims, ok := a.sessionClaims(r)
	if ok {
		writeJSON(w, http.StatusOK, claims)
		return
	}
	if a.validBearerTokenRequest(r) && a.passwordLogin {
		writeJSON(w, http.StatusOK, sessionClaims{Username: "superadmin", AuthMethod: "api_key"})
		return
	}
	writeError(w, http.StatusUnauthorized, "unauthorized")
}

func (a *API) getVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Version string `json:"version"`
	}{Version: serviceVersion})
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
