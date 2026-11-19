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
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		masterOK := a.key != "" && len(provided) == len(a.key) && subtle.ConstantTimeCompare([]byte(provided), []byte(a.key)) == 1
		networkID := chi.URLParam(r, "networkID")
		if networkID == "" && r.URL.Path == "/api/v1/join-bundle" {
			networkID = r.URL.Query().Get("network_id")
		}
		token := networkToken(a.key, networkID)
		networkOK := networkID != "" && len(provided) == len(token) && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
		if a.key == "" || provided == r.Header.Get("Authorization") || (!masterOK && !networkOK) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}
