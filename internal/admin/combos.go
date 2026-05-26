package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/httpx"
	"github.com/jackc/pgx/v5"
)

type comboDTO struct {
	ID        int64           `json:"id"`
	Slug      string          `json:"slug"`
	Name      string          `json:"name"`
	Nodes     json.RawMessage `json:"nodes"`
	Enabled   bool            `json:"enabled"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

func (d *Deps) ListCombos(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	rows, err := db.Pool().Query(r.Context(),
		`SELECT id, slug, name, nodes, enabled, created_at, updated_at FROM combos WHERE tenant_id = $1 ORDER BY id`, s.TenantID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	defer rows.Close()
	items := []comboDTO{}
	for rows.Next() {
		var c comboDTO
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.Nodes, &c.Enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
			httpx.WriteError(w, err)
			return
		}
		items = append(items, c)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type createComboReq struct {
	Slug  string           `json:"slug"`
	Name  string           `json:"name"`
	Nodes []map[string]any `json:"nodes"`
}

func (d *Deps) CreateCombo(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	var req createComboReq
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	if req.Slug == "" || len(req.Nodes) == 0 {
		httpx.WriteError(w, errs.Validation("slug + nodes[] required"))
		return
	}
	if req.Name == "" {
		req.Name = req.Slug
	}
	var c comboDTO
	err = db.Pool().QueryRow(r.Context(), `
		INSERT INTO combos (tenant_id, slug, name, nodes)
		VALUES ($1, $2, $3, $4)
		RETURNING id, slug, name, nodes, enabled, created_at, updated_at
	`, s.TenantID, req.Slug, req.Name, marshalJSON(req.Nodes)).Scan(
		&c.ID, &c.Slug, &c.Name, &c.Nodes, &c.Enabled, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

type updateComboReq struct {
	Name    *string          `json:"name"`
	Nodes   []map[string]any `json:"nodes"`
	Enabled *bool            `json:"enabled"`
}

func (d *Deps) UpdateCombo(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	slug := r.PathValue("slug")
	if slug == "" {
		httpx.WriteError(w, errs.Validation("slug path param required"))
		return
	}
	var req updateComboReq
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	sets := []string{}
	args := []any{}
	if req.Name != nil {
		args = append(args, *req.Name)
		sets = append(sets, "name = $"+itoa(len(args)))
	}
	if req.Nodes != nil {
		args = append(args, marshalJSON(req.Nodes))
		sets = append(sets, "nodes = $"+itoa(len(args)))
	}
	if req.Enabled != nil {
		args = append(args, *req.Enabled)
		sets = append(sets, "enabled = $"+itoa(len(args)))
	}
	if len(sets) == 0 {
		httpx.WriteError(w, errs.Validation("no updatable fields"))
		return
	}
	args = append(args, slug, s.TenantID)
	q := `UPDATE combos SET ` + join(sets, ", ") + `, updated_at = now()
	      WHERE slug = $` + itoa(len(args)-1) + ` AND tenant_id = $` + itoa(len(args)) + `
	      RETURNING id, slug, name, nodes, enabled, created_at, updated_at`
	var c comboDTO
	err = db.Pool().QueryRow(r.Context(), q, args...).Scan(
		&c.ID, &c.Slug, &c.Name, &c.Nodes, &c.Enabled, &c.CreatedAt, &c.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, errs.NotFound("Combo not found"))
		return
	}
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

func (d *Deps) DeleteCombo(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	slug := r.PathValue("slug")
	if slug == "" {
		httpx.WriteError(w, errs.Validation("slug path param required"))
		return
	}
	tag, err := db.Pool().Exec(r.Context(),
		`DELETE FROM combos WHERE slug = $1 AND tenant_id = $2`, slug, s.TenantID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, errs.NotFound("Combo not found"))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "slug": slug})
}
