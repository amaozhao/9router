package admin

import (
	"net/http"
	"time"

	"github.com/amaozhao/lazirouter/internal/cryptox"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/httpx"
	"github.com/amaozhao/lazirouter/internal/picker"
)

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
	if provider != "claude" && provider != "codex" {
		httpx.WriteError(w, errs.Validation("provider must be claude or codex"))
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
