package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/amaozhao/lazirouter/internal/cryptox"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/httpx"
	"github.com/amaozhao/lazirouter/internal/picker"
	"github.com/jackc/pgx/v5"
)

type connDTO struct {
	ID              int64           `json:"id"`
	Provider        string          `json:"provider"`
	Name            string          `json:"name"`
	AuthType        string          `json:"authType"`
	Metadata        json.RawMessage `json:"metadata"`
	Enabled         bool            `json:"enabled"`
	Weight          int             `json:"weight"`
	OauthExpiresAt  *time.Time      `json:"oauthExpiresAt"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

func (d *Deps) ListConnections(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	rows, err := db.Pool().Query(r.Context(), `
		SELECT id, provider, name, auth_type, metadata, enabled, weight, oauth_expires_at, created_at, updated_at
		FROM connections WHERE tenant_id = $1 ORDER BY id
	`, s.TenantID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	defer rows.Close()
	items := []connDTO{}
	for rows.Next() {
		var c connDTO
		if err := rows.Scan(&c.ID, &c.Provider, &c.Name, &c.AuthType, &c.Metadata, &c.Enabled, &c.Weight, &c.OauthExpiresAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			httpx.WriteError(w, err)
			return
		}
		items = append(items, c)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type createConnReq struct {
	Provider    string         `json:"provider"`
	Name        string         `json:"name"`
	AuthType    string         `json:"authType"`
	Credentials map[string]any `json:"credentials"`
	Metadata    map[string]any `json:"metadata"`
	Weight      *int           `json:"weight"`
}

func (d *Deps) CreateConnection(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	var req createConnReq
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	if req.Provider == "" || req.Name == "" || req.AuthType == "" {
		httpx.WriteError(w, errs.Validation("provider, name, authType required"))
		return
	}
	if req.Credentials == nil {
		httpx.WriteError(w, errs.Validation("credentials object is required"))
		return
	}
	if req.AuthType == "api_key" {
		if _, ok := req.Credentials["api_key"]; !ok {
			httpx.WriteError(w, errs.Validation("credentials.api_key is required for authType=api_key"))
			return
		}
	}
	weight := 1
	if req.Weight != nil && *req.Weight >= 1 {
		weight = *req.Weight
	}
	enc, err := cryptox.EncryptForTenant(d.Cfg.MasterKey, s.TenantID, req.Credentials)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	metaJSON := []byte("{}")
	if req.Metadata != nil {
		metaJSON = marshalJSON(req.Metadata)
	}
	var c connDTO
	err = db.Pool().QueryRow(r.Context(), `
		INSERT INTO connections (tenant_id, provider, name, auth_type, credentials_encrypted, metadata, weight)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, provider, name, auth_type, metadata, enabled, weight, oauth_expires_at, created_at, updated_at
	`, s.TenantID, req.Provider, req.Name, req.AuthType, enc, metaJSON, weight).Scan(
		&c.ID, &c.Provider, &c.Name, &c.AuthType, &c.Metadata, &c.Enabled, &c.Weight,
		&c.OauthExpiresAt, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	_ = picker.InvalidateCache(r.Context(), s.TenantID, req.Provider)
	httpx.WriteJSON(w, http.StatusOK, c)
}

type patchConnReq struct {
	Name        *string                 `json:"name"`
	Weight      *int                    `json:"weight"`
	Enabled     *bool                   `json:"enabled"`
	Metadata    *map[string]any         `json:"metadata"`
	Credentials *map[string]any         `json:"credentials"`
}

func (d *Deps) UpdateConnection(w http.ResponseWriter, r *http.Request) {
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
	var req patchConnReq
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	// Pre-check ownership + grab provider for cache invalidation
	var provider string
	err = db.Pool().QueryRow(r.Context(),
		`SELECT provider FROM connections WHERE id = $1 AND tenant_id = $2`, id, s.TenantID).Scan(&provider)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, errs.NotFound("Connection not found"))
		return
	}
	if err != nil {
		httpx.WriteError(w, err)
		return
	}

	// Build dynamic UPDATE
	sets := []string{}
	args := []any{}
	add := func(col string, val any) {
		args = append(args, val)
		sets = append(sets, col+" = $"+itoa(len(args)))
	}
	if req.Name != nil {
		add("name", *req.Name)
	}
	if req.Weight != nil {
		add("weight", *req.Weight)
	}
	if req.Enabled != nil {
		add("enabled", *req.Enabled)
	}
	if req.Metadata != nil {
		add("metadata", marshalJSON(*req.Metadata))
	}
	if req.Credentials != nil {
		enc, err := cryptox.EncryptForTenant(d.Cfg.MasterKey, s.TenantID, *req.Credentials)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		add("credentials_encrypted", enc)
	}
	if len(sets) == 0 {
		httpx.WriteError(w, errs.Validation("no updatable fields"))
		return
	}
	args = append(args, id, s.TenantID)
	q := `UPDATE connections SET ` + join(sets, ", ") + `, updated_at = now()
	      WHERE id = $` + itoa(len(args)-1) + ` AND tenant_id = $` + itoa(len(args)) + `
	      RETURNING id, provider, name, auth_type, metadata, enabled, weight, oauth_expires_at, created_at, updated_at`
	var c connDTO
	err = db.Pool().QueryRow(r.Context(), q, args...).Scan(
		&c.ID, &c.Provider, &c.Name, &c.AuthType, &c.Metadata, &c.Enabled, &c.Weight,
		&c.OauthExpiresAt, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	_ = picker.InvalidateCache(r.Context(), s.TenantID, provider)
	httpx.WriteJSON(w, http.StatusOK, c)
}

func (d *Deps) DeleteConnection(w http.ResponseWriter, r *http.Request) {
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
	var provider string
	err = db.Pool().QueryRow(r.Context(),
		`DELETE FROM connections WHERE id = $1 AND tenant_id = $2 RETURNING provider`, id, s.TenantID).Scan(&provider)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, errs.NotFound("Connection not found"))
		return
	}
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	_ = picker.InvalidateCache(r.Context(), s.TenantID, provider)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// tiny local helpers — avoid adding strconv/strings importance noise to every call site
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return slowItoa(n)
}
func slowItoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		s = string(rune('0'+(n%10))) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}
func join(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}
