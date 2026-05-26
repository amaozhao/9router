package admin

import (
	"net/http"
	"time"

	"github.com/amaozhao/lazirouter/internal/cryptox"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/httpx"
	"github.com/amaozhao/lazirouter/internal/tenantctx"
)

func (d *Deps) ListKeys(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	rows, err := db.Pool().Query(r.Context(), `
		SELECT id, key_prefix, name, scopes, rate_limit_rpm, last_used_at, created_at
		FROM api_keys WHERE tenant_id = $1 AND revoked_at IS NULL ORDER BY id
	`, s.TenantID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var prefix string
		var name *string
		var scopes []byte
		var rpm *int
		var lastUsed, created *time.Time
		if err := rows.Scan(&id, &prefix, &name, &scopes, &rpm, &lastUsed, &created); err != nil {
			httpx.WriteError(w, err)
			return
		}
		items = append(items, map[string]any{
			"id": id, "keyPrefix": prefix, "name": name,
			"scopes": rawJSON(scopes), "rateLimitRpm": rpm,
			"lastUsedAt": lastUsed, "createdAt": created,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type createKeyReq struct {
	Name         *string        `json:"name"`
	Scopes       map[string]any `json:"scopes"`
	RateLimitRPM *int           `json:"rateLimitRpm"`
}

func (d *Deps) CreateKey(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	var req createKeyReq
	_ = httpx.ReadJSON(r, &req)
	plain, hash, prefix, err := cryptox.GenerateAPIKey()
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	scopesJSON := []byte("{}")
	if req.Scopes != nil {
		scopesJSON = marshalJSON(req.Scopes)
	}
	var id int64
	if err := db.Pool().QueryRow(r.Context(), `
		INSERT INTO api_keys (tenant_id, created_by, key_prefix, key_hash, name, scopes, rate_limit_rpm)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id
	`, s.TenantID, s.UserID, prefix, hash, req.Name, scopesJSON, req.RateLimitRPM).Scan(&id); err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": id, "keyPrefix": prefix, "key": plain,
		"name": req.Name, "scopes": req.Scopes, "rateLimitRpm": req.RateLimitRPM,
	})
}

func (d *Deps) RevokeKey(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	id, err := pathInt(r, "id")
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	var hash string
	err = db.Pool().QueryRow(r.Context(), `
		UPDATE api_keys SET revoked_at = now()
		WHERE id = $1 AND tenant_id = $2 AND revoked_at IS NULL
		RETURNING key_hash
	`, id, s.TenantID).Scan(&hash)
	if err != nil {
		httpx.WriteError(w, errs.NotFound("API key not found or already revoked"))
		return
	}
	_ = tenantctx.InvalidateByHash(r.Context(), hash)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}
