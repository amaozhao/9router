package admin

import (
	"errors"
	"net/http"

	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/httpx"
	"github.com/amaozhao/lazirouter/internal/scenario"
	"github.com/jackc/pgx/v5"
)

func (d *Deps) ListRouting(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	rows, err := db.Pool().Query(r.Context(),
		`SELECT scenario, target, updated_at FROM tenant_routing WHERE tenant_id = $1 ORDER BY scenario`, s.TenantID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var scn, tgt string
		var updated any
		_ = rows.Scan(&scn, &tgt, &updated)
		items = append(items, map[string]any{"scenario": scn, "target": tgt, "updatedAt": updated})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type putRoutingReq struct {
	Target string `json:"target"`
}

func (d *Deps) PutRouting(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	scn := r.PathValue("scenario")
	if !validScenario(scn) {
		httpx.WriteError(w, errs.Validation("unknown scenario: "+scn))
		return
	}
	var req putRoutingReq
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	if req.Target == "" {
		httpx.WriteError(w, errs.Validation("target required"))
		return
	}
	// validate combo target exists if it's a combo:slug
	if len(req.Target) > 6 && req.Target[:6] == "combo:" {
		slug := req.Target[6:]
		var n int
		_ = db.Pool().QueryRow(r.Context(),
			`SELECT 1 FROM combos WHERE tenant_id = $1 AND slug = $2 AND enabled = TRUE`, s.TenantID, slug).Scan(&n)
		if n == 0 {
			httpx.WriteError(w, errs.Validation("combo not found or disabled: "+slug))
			return
		}
	}
	_, err = db.Pool().Exec(r.Context(), `
		INSERT INTO tenant_routing (tenant_id, scenario, target)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, scenario) DO UPDATE SET
		  target = EXCLUDED.target,
		  updated_at = now()
	`, s.TenantID, scn, req.Target)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	_ = scenario.InvalidateRouting(r.Context(), s.TenantID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"scenario": scn, "target": req.Target})
}

func (d *Deps) DeleteRouting(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	scn := r.PathValue("scenario")
	tag, err := db.Pool().Exec(r.Context(),
		`DELETE FROM tenant_routing WHERE tenant_id = $1 AND scenario = $2`, s.TenantID, scn)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, errs.NotFound("scenario binding not found"))
		return
	}
	_ = scenario.InvalidateRouting(r.Context(), s.TenantID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "scenario": scn})
}

func validScenario(s string) bool {
	switch s {
	case scenario.Default, scenario.Think, scenario.LongContext,
		scenario.Vision, scenario.ToolUse, scenario.Web:
		return true
	}
	return false
}

var _ = errors.New
var _ = pgx.ErrNoRows
