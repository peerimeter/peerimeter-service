package api

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	corsorigin "github.com/peerimeter/peerimeter-service/internal/origin"
)

func (a *API) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if len(a.allowedOrigins) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		canonical, err := corsorigin.Normalize(origin)
		if err != nil || !a.allowedOrigins[canonical] {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", canonical)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.validBearerTokenRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if a.sessionClaimsValid(r) {
			if !a.validSessionRequestOrigin(r) {
				writeError(w, http.StatusForbidden, "untrusted origin")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized")
	})
}

func (a *API) validBearerTokenRequest(r *http.Request) bool {
	provided, ok := bearerToken(r)
	if !ok || a.key == "" {
		return false
	}
	masterOK := a.passwordLogin && constantTimeEqual(provided, a.key)
	networkID := requestNetworkID(r)
	networkOK := networkID != "" && constantTimeEqual(provided, networkToken(a.key, networkID))
	return masterOK || networkOK
}

func (a *API) authenticateManagement(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided, ok := bearerToken(r)
		keyOK := a.passwordLogin && ok && a.key != "" && constantTimeEqual(provided, a.key)
		if keyOK {
			next.ServeHTTP(w, r)
			return
		}
		sessionOK := a.sessionClaimsValid(r)
		if !sessionOK {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !a.validSessionRequestOrigin(r) {
			writeError(w, http.StatusForbidden, "untrusted origin")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) validSessionRequestOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	requestOrigin, err := corsorigin.Normalize(r.Header.Get("Origin"))
	if err != nil {
		return false
	}
	if a.allowedOrigins[requestOrigin] {
		return true
	}
	serviceOrigin, err := corsorigin.Normalize(a.publicURL)
	if err == nil && requestOrigin == serviceOrigin {
		return true
	}
	if a.auth == nil {
		return false
	}
	trustedOrigin, err := corsorigin.Normalize(a.auth.config.DashboardURL)
	return err == nil && requestOrigin == trustedOrigin
}

func bearerToken(r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return token, ok && token != ""
}

func constantTimeEqual(provided, expected string) bool {
	return expected != "" && len(provided) == len(expected) && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func requestNetworkID(r *http.Request) string {
	if networkID := chi.URLParam(r, "networkID"); networkID != "" {
		return networkID
	}
	if r.URL.Path == "/api/v1/join-bundle" {
		return r.URL.Query().Get("network_id")
	}
	return ""
}
