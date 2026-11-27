package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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
	config GitHubConfig
	client *http.Client
}

type sessionClaims struct {
	Username   string `json:"username"`
	AuthMethod string `json:"auth_method"`
	ExpiresAt  int64  `json:"expires_at"`
}

func NewGitHubAuth(config GitHubConfig) *GitHubAuth {
	return &GitHubAuth{
		config: config,
		client: &http.Client{Timeout: 8 * time.Second},
	}
}

func (a *API) authGitHubLogin(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil {
		http.NotFound(w, r)
		return
	}
	browserNonce, err := randomToken()
	if err != nil {
		a.logger.ErrorContext(r.Context(), "create GitHub OAuth state", "error", err)
		http.Error(w, "authentication unavailable", http.StatusInternalServerError)
		return
	}
	state, err := signPayload(a.sessionSecret, "peerimeter-github-state-v1", oauthStateClaims{Nonce: browserNonce, ExpiresAt: time.Now().Add(authStateLifetime).Unix()})
	if err != nil {
		a.logger.ErrorContext(r.Context(), "sign GitHub OAuth state", "error", err)
		http.Error(w, "authentication unavailable", http.StatusInternalServerError)
		return
	}
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
	var pending oauthStateClaims
	validState := verifyPayload(a.sessionSecret, "peerimeter-github-state-v1", state, &pending) == nil
	validBrowser := cookieErr == nil && hmac.Equal([]byte(cookie.Value), []byte(pending.Nonce))
	if !validState || !validBrowser || time.Now().Unix() >= pending.ExpiresAt || r.URL.Query().Get("error") != "" {
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
	claims := sessionClaims{Username: username, AuthMethod: "github", ExpiresAt: time.Now().Add(authSessionLifetime).Unix()}
	if err := a.setSessionCookie(w, claims); err != nil {
		a.logger.ErrorContext(r.Context(), "sign GitHub session", "error", err)
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, strings.TrimRight(a.auth.config.DashboardURL, "/"), http.StatusFound)
}

func clearOAuthStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookieName, Value: "", Path: "/auth/github/callback", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (a *API) authLogout(w http.ResponseWriter, r *http.Request) {
	if a.sessionClaimsValid(r) {
		if !a.validSessionRequestOrigin(r) {
			writeError(w, http.StatusForbidden, "untrusted origin")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: authCookieName, Value: "", Path: "/", HttpOnly: true, Secure: strings.HasPrefix(a.publicURL, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) sessionClaimsValid(r *http.Request) bool {
	_, ok := a.sessionClaims(r)
	return ok
}

func (a *API) sessionClaims(r *http.Request) (sessionClaims, bool) {
	cookie, err := r.Cookie(authCookieName)
	if err != nil || cookie.Value == "" {
		return sessionClaims{}, false
	}
	var claims sessionClaims
	if err := verifyPayload(a.sessionSecret, "peerimeter-session-v1", cookie.Value, &claims); err != nil || claims.Username == "" || (claims.AuthMethod != "github" && claims.AuthMethod != "api_key") || time.Now().Unix() >= claims.ExpiresAt {
		return sessionClaims{}, false
	}
	return claims, true
}

func (a *API) setSessionCookie(w http.ResponseWriter, claims sessionClaims) error {
	value, err := signPayload(a.sessionSecret, "peerimeter-session-v1", claims)
	if err != nil {
		return err
	}
	secure := strings.HasPrefix(a.publicURL, "https://")
	sameSite := http.SameSiteLaxMode
	if secure {
		sameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, &http.Cookie{Name: authCookieName, Value: value, Path: "/", HttpOnly: true, Secure: secure, SameSite: sameSite, Expires: time.Unix(claims.ExpiresAt, 0), MaxAge: int(time.Until(time.Unix(claims.ExpiresAt, 0)).Seconds())})
	return nil
}

type oauthStateClaims struct {
	Nonce     string `json:"nonce"`
	ExpiresAt int64  `json:"expires_at"`
}

func signPayload(secret, purpose string, payload any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(encoded)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(purpose + "\x00" + body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyPayload(secret, purpose, token string, destination any) error {
	body, signature, ok := strings.Cut(token, ".")
	if !ok {
		return fmt.Errorf("malformed token")
	}
	provided, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(purpose + "\x00" + body))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return fmt.Errorf("invalid token signature")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, destination)
}

func deriveSessionSecret(key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte("peerimeter-session-cookie-v1"))
	return hex.EncodeToString(mac.Sum(nil))
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
