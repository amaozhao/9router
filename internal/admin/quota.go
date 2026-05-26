package admin

import (
	"net/http"
	"time"

	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/httpx"
	"github.com/amaozhao/lazirouter/internal/quota"
)

// GET /api/me/quota — the tenant's own usage + limits today.
func (d *Deps) MyQuota(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	var plan string
	if err := db.Pool().QueryRow(r.Context(),
		`SELECT plan FROM tenants WHERE id = $1`, s.TenantID).Scan(&plan); err != nil {
		httpx.WriteError(w, err)
		return
	}
	u, err := quota.ReadUsage(r.Context(), s.TenantID, plan)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

// GET /api/admin/tenants — super-admin only.
func (d *Deps) ListTenants(w http.ResponseWriter, r *http.Request) {
	_, err := RequireSuperAdmin(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	rows, err := db.Pool().Query(r.Context(), `
		SELECT t.id, t.name, t.plan, t.status, t.created_at,
		       (SELECT COUNT(*) FROM users u WHERE u.tenant_id = t.id),
		       (SELECT COUNT(*) FROM api_keys ak WHERE ak.tenant_id = t.id AND ak.revoked_at IS NULL),
		       (SELECT COUNT(*) FROM connections c WHERE c.tenant_id = t.id AND c.enabled = TRUE),
		       tq.daily_token_limit, tq.daily_request_limit, tq.note
		FROM tenants t
		LEFT JOIN tenant_quotas tq ON tq.tenant_id = t.id
		ORDER BY t.id DESC
	`)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	defaults := quota.Defaults()
	for rows.Next() {
		var (
			id                                       int64
			name, plan, status                       string
			createdAt                                time.Time
			userCount, activeKeys, activeConnections int
			tokLimit                                 *int64
			reqLimit                                 *int
			note                                     *string
		)
		if err := rows.Scan(&id, &name, &plan, &status, &createdAt,
			&userCount, &activeKeys, &activeConnections,
			&tokLimit, &reqLimit, &note); err != nil {
			httpx.WriteError(w, err)
			return
		}
		usage, _ := quota.ReadUsage(r.Context(), id, plan)
		pd := defaults[plan]
		eff := map[string]any{"tokens": pd.Tokens, "requests": pd.Requests}
		if tokLimit != nil {
			eff["tokens"] = *tokLimit
		}
		if reqLimit != nil {
			eff["requests"] = *reqLimit
		}
		items = append(items, map[string]any{
			"id": id, "name": name, "plan": plan, "status": status,
			"createdAt":         createdAt,
			"userCount":         userCount,
			"activeKeys":        activeKeys,
			"activeConnections": activeConnections,
			"quota": map[string]any{
				"dailyTokenLimit":   tokLimit,
				"dailyRequestLimit": reqLimit,
				"note":              note,
				"defaults":          map[string]any{"tokens": pd.Tokens, "requests": pd.Requests},
				"effective":         eff,
				"usage": map[string]any{
					"tokens":   usage.Tokens.Used,
					"requests": usage.Requests.Used,
					"date":     usage.Date,
				},
			},
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type putQuotaReq struct {
	DailyTokenLimit   *int64  `json:"dailyTokenLimit"`
	DailyRequestLimit *int    `json:"dailyRequestLimit"`
	Note              *string `json:"note"`
}

func (d *Deps) PutTenantQuota(w http.ResponseWriter, r *http.Request) {
	_, err := RequireSuperAdmin(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	id, err := pathInt(r, "id")
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	var req putQuotaReq
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	_, err = db.Pool().Exec(r.Context(), `
		INSERT INTO tenant_quotas (tenant_id, daily_token_limit, daily_request_limit, note)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id) DO UPDATE SET
		  daily_token_limit = EXCLUDED.daily_token_limit,
		  daily_request_limit = EXCLUDED.daily_request_limit,
		  note = EXCLUDED.note,
		  updated_at = now()
	`, id, req.DailyTokenLimit, req.DailyRequestLimit, req.Note)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	_ = quota.InvalidateCache(r.Context(), id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "tenantId": id})
}

// stop unused-import linter complaint when only errs.Validation is referenced via httpx
var _ = errs.Validation
