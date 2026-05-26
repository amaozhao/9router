package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/amaozhao/lazirouter/internal/cryptox"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/httpx"
	"github.com/amaozhao/lazirouter/internal/picker"
	"github.com/amaozhao/lazirouter/internal/redisx"
)

const stateTTL = 10 * time.Minute

// ListOAuthProviders — GET /api/oauth/providers
// Public: the SPA needs this list to render the "Connect" dropdown before the
// user has even logged in for some flows. We still gate the start/callback
// endpoints by session, so leaking the list isn't sensitive.
func (d *Deps) ListOAuthProviders(w http.ResponseWriter, r *http.Request) {
	names := listProviders()
	items := make([]map[string]any, 0, len(names))
	for _, n := range names {
		_, err := resolveProvider(n)
		items = append(items, map[string]any{"provider": n, "ready": err == nil})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type oauthStartReq struct {
	ConnectionName string `json:"connectionName"`
	RedirectURI    string `json:"redirectUri"`
}

type oauthFlow struct {
	TenantID       int64  `json:"tenantId"`
	UserID         int64  `json:"userId"`
	Provider       string `json:"provider"`
	ConnectionName string `json:"connectionName"`
	Verifier       string `json:"verifier"`
	RedirectURI    string `json:"redirectUri"`
}

// StartOAuth — POST /api/oauth/{provider}/start
// Session-protected. Generates PKCE pair + state, stows in Redis with 10-min TTL,
// returns the authorize URL the SPA opens in a popup window.
func (d *Deps) StartOAuth(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if s.Role != "owner" && s.Role != "admin" {
		httpx.WriteError(w, errs.Forbidden("owner or admin role required"))
		return
	}
	providerName := r.PathValue("provider")
	provider, err := resolveProvider(providerName)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	var req oauthStartReq
	_ = httpx.ReadJSON(r, &req)
	connName := strings.TrimSpace(req.ConnectionName)
	if connName == "" {
		connName = providerName + "-oauth"
	}
	redirectURI := strings.TrimSpace(req.RedirectURI)
	if redirectURI == "" {
		redirectURI = defaultRedirectURI(r)
	}

	verifier, challenge, err := buildPKCE()
	if err != nil {
		httpx.WriteError(w, errs.Internal("pkce: "+err.Error()))
		return
	}
	state, err := randomState()
	if err != nil {
		httpx.WriteError(w, errs.Internal("state: "+err.Error()))
		return
	}
	flowJSON, _ := json.Marshal(oauthFlow{
		TenantID:       s.TenantID,
		UserID:         s.UserID,
		Provider:       providerName,
		ConnectionName: connName,
		Verifier:       verifier,
		RedirectURI:    redirectURI,
	})
	if err := redisx.C().Set(r.Context(), "oauth:state:"+state, flowJSON, stateTTL).Err(); err != nil {
		httpx.WriteError(w, errs.Internal("redis: "+err.Error()))
		return
	}

	authorizeURL, err := buildAuthorizeURL(provider, state, challenge, redirectURI)
	if err != nil {
		httpx.WriteError(w, errs.Internal("authorize url: "+err.Error()))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"authorizeUrl": authorizeURL,
		"state":        state,
		"expiresInSec": int(stateTTL.Seconds()),
	})
}

// OAuthCallback — GET /api/oauth/callback
// The provider redirects the user here with ?code+state (or ?error). We
// recover the flow from Redis, exchange the code, and persist the resulting
// connection. Renders a tiny HTML page so the popup window has something to
// show before it closes.
func (d *Deps) OAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		renderResult(w, http.StatusBadRequest, map[string]any{
			"error":             e,
			"error_description": q.Get("error_description"),
		})
		return
	}
	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		httpx.WriteError(w, errs.Validation("Missing code or state"))
		return
	}
	ctx := r.Context()
	raw, err := redisx.C().Get(ctx, "oauth:state:"+state).Result()
	if err != nil || raw == "" {
		httpx.WriteError(w, errs.NotFound("OAuth state expired or unknown"))
		return
	}
	_ = redisx.C().Del(ctx, "oauth:state:"+state).Err()

	var flow oauthFlow
	if err := json.Unmarshal([]byte(raw), &flow); err != nil {
		httpx.WriteError(w, errs.Internal("flow parse: "+err.Error()))
		return
	}
	provider, err := resolveProvider(flow.Provider)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}

	tokens, err := exchangeCode(ctx, provider, code, flow.Verifier, flow.RedirectURI, state)
	if err != nil {
		renderResult(w, http.StatusBadGateway, map[string]any{
			"error":  "exchange_failed",
			"detail": err.Error(),
		})
		return
	}
	access, _ := tokens["access_token"].(string)
	if access == "" {
		renderResult(w, http.StatusBadRequest, map[string]any{
			"error":          "no_access_token",
			"token_response": tokens,
		})
		return
	}

	creds := map[string]any{
		"access_token":   access,
		"refresh_token":  tokens["refresh_token"],
		"token_endpoint": provider.TokenEndpoint,
		"client_id":      provider.ClientID,
		"obtained_at":    time.Now().UTC().Format(time.RFC3339),
		"scope":          firstNonEmpty(stringFrom(tokens, "scope"), provider.Scope),
	}
	if provider.ClientSecret != "" {
		creds["client_secret"] = provider.ClientSecret
	}
	expiresInSec := int64(3600)
	switch v := tokens["expires_in"].(type) {
	case float64:
		expiresInSec = int64(v)
	case string:
		// best-effort
	}
	expiresAt := time.Now().Add(time.Duration(expiresInSec) * time.Second).UTC()

	blob, err := cryptox.EncryptForTenant(d.Cfg.MasterKey, flow.TenantID, creds)
	if err != nil {
		httpx.WriteError(w, errs.Internal("encrypt: "+err.Error()))
		return
	}
	metaJSON, _ := json.Marshal(map[string]any{"via": "oauth", "flow_state": state})
	_, err = db.Pool().Exec(ctx, `
		INSERT INTO connections
		  (tenant_id, provider, name, auth_type, credentials_encrypted, oauth_expires_at, metadata, enabled, weight)
		VALUES ($1, $2, $3, 'oauth', $4, $5, $6, TRUE, 1)
		ON CONFLICT (tenant_id, provider, name) DO UPDATE SET
		  credentials_encrypted = EXCLUDED.credentials_encrypted,
		  oauth_expires_at      = EXCLUDED.oauth_expires_at,
		  auth_type             = 'oauth',
		  enabled               = TRUE,
		  updated_at            = now()
	`, flow.TenantID, flow.Provider, flow.ConnectionName, blob, expiresAt, metaJSON)
	if err != nil {
		httpx.WriteError(w, errs.Internal("persist connection: "+err.Error()))
		return
	}
	_ = picker.InvalidateCache(ctx, flow.TenantID, flow.Provider)

	renderResult(w, http.StatusOK, map[string]any{
		"ok":         true,
		"provider":   flow.Provider,
		"name":       flow.ConnectionName,
		"expires_at": expiresAt.Format(time.RFC3339),
	})
}

func defaultRedirectURI(r *http.Request) string {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "http"
	}
	return fmt.Sprintf("%s://%s/api/oauth/callback", proto, host)
}

func renderResult(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	pretty, _ := json.MarshalIndent(payload, "", "  ")
	title := "✗ OAuth failed"
	if status == http.StatusOK {
		title = "✓ Connection added"
	}
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html><head><meta charset="utf-8"><title>lazirouter OAuth</title>
<style>body{font-family:system-ui;background:#0d1117;color:#c9d1d9;padding:48px;text-align:center}
pre{display:inline-block;text-align:left;background:#161b22;padding:16px;border-radius:8px;border:1px solid #30363d}</style>
</head><body>
<h1>%s</h1>
<pre>%s</pre>
<p>You can close this window and return to the dashboard.</p>
</body></html>`, title, htmlEscape(string(pretty)))
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

func stringFrom(m map[string]any, k string) string {
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}


type importTokenReq struct {
	ConnectionName string   `json:"connectionName"`
	AccessToken    string   `json:"accessToken"`
	RefreshToken   *string  `json:"refreshToken"`
	ExpiresAt      *string  `json:"expiresAt"`
	Scopes         []string `json:"scopes"`
}

// ImportOAuth — bring-your-own-token for subscription providers (claude, codex).
// POST /api/oauth/{provider}/import
func (d *Deps) ImportOAuth(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	provider := r.PathValue("provider")
	// Accept any registered provider so the BYOT path stays in sync with the
	// /start flow. Unknown providers still error.
	if _, perr := resolveProvider(provider); perr != nil {
		httpx.WriteError(w, perr)
		return
	}
	var req importTokenReq
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	if req.AccessToken == "" {
		httpx.WriteError(w, errs.Validation("accessToken required"))
		return
	}
	if req.ConnectionName == "" {
		req.ConnectionName = provider + "-sub"
	}
	creds := map[string]any{"access_token": req.AccessToken}
	if req.RefreshToken != nil {
		creds["refresh_token"] = *req.RefreshToken
	}
	if len(req.Scopes) > 0 {
		creds["scopes"] = req.Scopes
	}

	enc, err := cryptox.EncryptForTenant(d.Cfg.MasterKey, s.TenantID, creds)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	var expiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, *req.ExpiresAt); err == nil {
			expiresAt = &t
		}
	}
	metaJSON := []byte(`{"via":"import"}`)
	_, err = db.Pool().Exec(r.Context(), `
		INSERT INTO connections (tenant_id, provider, name, auth_type, credentials_encrypted, metadata, oauth_expires_at)
		VALUES ($1, $2, $3, 'oauth', $4, $5, $6)
		ON CONFLICT (tenant_id, provider, name) DO UPDATE SET
		  credentials_encrypted = EXCLUDED.credentials_encrypted,
		  metadata              = EXCLUDED.metadata,
		  oauth_expires_at      = EXCLUDED.oauth_expires_at,
		  enabled               = TRUE,
		  updated_at            = now()
	`, s.TenantID, provider, req.ConnectionName, enc, metaJSON, expiresAt)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	_ = picker.InvalidateCache(r.Context(), s.TenantID, provider)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"provider":   provider,
		"name":       req.ConnectionName,
		"expires_at": expiresAt,
	})
}
