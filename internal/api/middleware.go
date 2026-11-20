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
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
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
		provided, ok := bearerToken(r)
		if !ok || a.key == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		masterOK := constantTimeEqual(provided, a.key)
		networkID := requestNetworkID(r)
		token := networkToken(a.key, networkID)
		networkOK := networkID != "" && constantTimeEqual(provided, token)
		if !masterOK && !networkOK {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) authenticateManagement(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided, ok := bearerToken(r)
		if !ok || a.key == "" || !constantTimeEqual(provided, a.key) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
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
