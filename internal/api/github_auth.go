package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	authCookieName       = "peerimeter_session"
	oauthStateCookieName = "peerimeter_oauth_state"
	authSessionLifetime  = 12 * time.Hour
	authStateLifetime    = 10 * time.Minute
)

type GitHubConfig struct {
	ClientID      string
	ClientSecret  string
	Organization  string
	Team          string
	DashboardURL  string
	SessionSecret string
}

func (c GitHubConfig) Enabled() bool {
	return c.ClientID != ""
}

type GitHubAuth struct {
	config   GitHubConfig
	client   *http.Client
	mu       sync.Mutex
	states   map[string]oauthState
	sessions map[string]time.Time
}

type oauthState struct {
	expires      time.Time
	browserNonce string
}

func NewGitHubAuth(config GitHubConfig) *GitHubAuth {
	return &GitHubAuth{
		config:   config,
		client:   &http.Client{Timeout: 8 * time.Second},
		states:   make(map[string]oauthState),
		sessions: make(map[string]time.Time),
	}
}

func (a *API) authGitHubLogin(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil {
		http.NotFound(w, r)
		return
	}
	state, err := randomToken()
	if err != nil {
		a.logger.ErrorContext(r.Context(), "create GitHub OAuth state", "error", err)
		http.Error(w, "authentication unavailable", http.StatusInternalServerError)
		return
	}
	browserNonce, err := randomToken()
	if err != nil {
		a.logger.ErrorContext(r.Context(), "create GitHub OAuth browser nonce", "error", err)
		http.Error(w, "authentication unavailable", http.StatusInternalServerError)
		return
	}
	a.auth.mu.Lock()
	for pendingState, pending := range a.auth.states {
		if time.Now().After(pending.expires) {
			delete(a.auth.states, pendingState)
		}
	}
	a.auth.states[state] = oauthState{expires: time.Now().Add(authStateLifetime), browserNonce: browserNonce}
	a.auth.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookieName, Value: browserNonce, Path: "/auth/github/callback", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: int(authStateLifetime.Seconds())})
	query := url.Values{"client_id": {a.auth.config.ClientID}, "redirect_uri": {a.publicURL + "/auth/github/callback"}, "scope": {"read:org"}, "state": {state}}
	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+query.Encode(), http.StatusFound)
}

func (a *API) authGitHubCallback(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil {
		http.NotFound(w, r)
		return
	}
	defer clearOAuthStateCookie(w)
	state := r.URL.Query().Get("state")
	cookie, cookieErr := r.Cookie(oauthStateCookieName)
	a.auth.mu.Lock()
	pending, found := a.auth.states[state]
	validBrowser := cookieErr == nil && hmac.Equal([]byte(cookie.Value), []byte(pending.browserNonce))
	if found && validBrowser {
		delete(a.auth.states, state)
	}
	a.auth.mu.Unlock()
	if !found || !validBrowser || time.Now().After(pending.expires) || r.URL.Query().Get("error") != "" {
		http.Error(w, "GitHub sign-in was not authorized", http.StatusUnauthorized)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "GitHub sign-in code is missing", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	githubToken, err := a.auth.exchangeCode(ctx, code)
	if err != nil {
		a.logger.ErrorContext(ctx, "exchange GitHub OAuth code", "error", err)
		http.Error(w, "GitHub sign-in failed", http.StatusBadGateway)
		return
	}
	username, err := a.auth.githubUsername(ctx, githubToken)
	if err != nil {
		a.logger.ErrorContext(ctx, "read GitHub account", "error", err)
		http.Error(w, "GitHub sign-in failed", http.StatusBadGateway)
		return
	}
	member, err := a.auth.isTeamMember(ctx, githubToken, username)
	if err != nil {
		a.logger.ErrorContext(ctx, "check GitHub team membership", "error", err)
		http.Error(w, "could not verify GitHub team membership", http.StatusBadGateway)
		return
	}
	if !member {
		http.Error(w, "your GitHub account is not in the authorized team", http.StatusForbidden)
		return
	}
	sessionID, err := randomToken()
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	a.auth.mu.Lock()
	a.auth.sessions[sessionHash(a.auth.config.SessionSecret, sessionID)] = time.Now().Add(authSessionLifetime)
	a.auth.mu.Unlock()
	secure := strings.HasPrefix(a.publicURL, "https://")
	sameSite := http.SameSiteLaxMode
	if secure {
		sameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, &http.Cookie{Name: authCookieName, Value: sessionID, Path: "/", HttpOnly: true, Secure: secure, SameSite: sameSite, Expires: time.Now().Add(authSessionLifetime), MaxAge: int(authSessionLifetime.Seconds())})
	http.Redirect(w, r, strings.TrimRight(a.auth.config.DashboardURL, "/"), http.StatusFound)
}

func clearOAuthStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookieName, Value: "", Path: "/auth/github/callback", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (a *API) authLogout(w http.ResponseWriter, r *http.Request) {
	if a.auth != nil {
		if a.auth.validSession(r) && !a.validSessionRequestOrigin(r) {
			writeError(w, http.StatusForbidden, "untrusted origin")
			return
		}
		if cookie, err := r.Cookie(authCookieName); err == nil {
			a.auth.mu.Lock()
			delete(a.auth.sessions, sessionHash(a.auth.config.SessionSecret, cookie.Value))
			a.auth.mu.Unlock()
		}
	}
	http.SetCookie(w, &http.Cookie{Name: authCookieName, Value: "", Path: "/", HttpOnly: true, Secure: strings.HasPrefix(a.publicURL, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (a *GitHubAuth) validSession(r *http.Request) bool {
	cookie, err := r.Cookie(authCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	key := sessionHash(a.config.SessionSecret, cookie.Value)
	a.mu.Lock()
	defer a.mu.Unlock()
	expires, ok := a.sessions[key]
	if ok && time.Now().After(expires) {
		delete(a.sessions, key)
		return false
	}
	return ok
}

func (a *GitHubAuth) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{"client_id": {a.config.ClientID}, "client_secret": {a.config.ClientSecret}, "code": {code}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := a.doJSON(req, &result); err != nil {
		return "", err
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("GitHub OAuth token exchange failed: %s", result.Error)
	}
	return result.AccessToken, nil
}

func (a *GitHubAuth) githubUsername(ctx context.Context, token string) (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if err := a.doJSON(req, &user); err != nil {
		return "", err
	}
	if user.Login == "" {
		return "", fmt.Errorf("GitHub account has no login")
	}
	return user.Login, nil
}

func (a *GitHubAuth) isTeamMember(ctx context.Context, token, username string) (bool, error) {
	endpoint := "https://api.github.com/orgs/" + url.PathEscape(a.config.Organization) + "/teams/" + url.PathEscape(a.config.Team) + "/memberships/" + url.PathEscape(username)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := a.client.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("GitHub team membership returned %s", res.Status)
	}
	var membership struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(res.Body).Decode(&membership); err != nil {
		return false, err
	}
	return membership.State == "active", nil
}

func (a *GitHubAuth) doJSON(req *http.Request, destination any) error {
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitHub API returned %s", res.Status)
	}
	return json.NewDecoder(res.Body).Decode(destination)
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func sessionHash(secret, sessionID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(sessionID))
	hash := mac.Sum(nil)
	return hex.EncodeToString(hash[:])
}
