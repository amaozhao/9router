package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/httpx"
)

// GET /api/usage/summary?hours=24 — hourly buckets for the tenant.
func (d *Deps) UsageSummary(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 168 {
			hours = n
		}
	}
	rows, err := db.Pool().Query(r.Context(), `
		SELECT bucket_hour, provider, model,
		       SUM(request_count)::bigint, SUM(prompt_tokens)::bigint,
		       SUM(completion_tokens)::bigint, SUM(cost_micros)::bigint,
		       SUM(error_count)::bigint
		FROM usage_summaries
		WHERE tenant_id = $1 AND bucket_hour > now() - $2 * interval '1 hour'
		GROUP BY bucket_hour, provider, model
		ORDER BY bucket_hour DESC, provider, model
	`, s.TenantID, hours)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var bucket time.Time
		var provider, model string
		var req, pt, ct, cost, errCount int64
		_ = rows.Scan(&bucket, &provider, &model, &req, &pt, &ct, &cost, &errCount)
		items = append(items, map[string]any{
			"bucketHour":       bucket,
			"provider":         provider,
			"model":            model,
			"requestCount":     req,
			"promptTokens":     pt,
			"completionTokens": ct,
			"costMicros":       cost,
			"errorCount":       errCount,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"hours": hours, "items": items})
}

// GET /api/usage/recent?limit=50 — most recent usage_events for the tenant.
func (d *Deps) UsageRecent(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := db.Pool().Query(r.Context(), `
		SELECT id, ts, provider, model, upstream_model, status,
		       prompt_tokens, completion_tokens, cost_micros, latency_ms,
		       error_code, request_id
		FROM usage_events
		WHERE tenant_id = $1
		ORDER BY id DESC LIMIT $2
	`, s.TenantID, limit)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var ts time.Time
		var prov, model, upstreamModel, status, reqID string
		var errCode *string
		var pt, ct, cost, lat int64
		_ = rows.Scan(&id, &ts, &prov, &model, &upstreamModel, &status, &pt, &ct, &cost, &lat, &errCode, &reqID)
		items = append(items, map[string]any{
			"id":               id,
			"ts":               ts,
			"provider":         prov,
			"model":            model,
			"upstreamModel":    upstreamModel,
			"status":           status,
			"promptTokens":     pt,
			"completionTokens": ct,
			"costMicros":       cost,
			"latencyMs":        lat,
			"errorCode":        errCode,
			"requestId":        reqID,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}
