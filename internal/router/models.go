package router

import (
	"net/http"
	"time"

	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/httpx"
	"github.com/amaozhao/lazirouter/internal/tenantctx"
)

var wellKnown = map[string][]string{
	"openai":   {"gpt-4o", "gpt-4o-mini", "gpt-4-turbo", "o1-mini", "o3-mini", "text-embedding-3-small", "text-embedding-3-large"},
	"gemini":   {"gemini-2.0-flash", "gemini-1.5-pro", "gemini-1.5-flash", "text-embedding-004"},
	"deepseek": {"deepseek-chat", "deepseek-reasoner"},
	"glm":      {"glm-4", "glm-4-flash"},
	"claude":   {"claude-sonnet-4-5", "claude-opus-4-5", "claude-haiku-4-5"},
	"codex":    {"gpt-5", "gpt-5.5", "gpt-5-codex"},
}

func (d *Deps) handleModels(w http.ResponseWriter, r *http.Request) {
	apiKey := httpx.BearerOrXAPIKey(r)
	if apiKey == "" {
		httpx.WriteError(w, errs.Auth("Missing Authorization or x-api-key header"))
		return
	}
	ctx, err := tenantctx.ResolveAPIKey(r.Context(), apiKey)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if err := tenantctx.EnforceTenantActive(ctx); err != nil {
		httpx.WriteError(w, err)
		return
	}

	// combos
	combosRows, err := db.Pool().Query(r.Context(), `
		SELECT slug FROM combos WHERE tenant_id = $1 AND enabled = TRUE ORDER BY slug
	`, ctx.TenantID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	defer combosRows.Close()
	now := time.Now().Unix()
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	seen := map[string]struct{}{}
	out := []model{}
	push := func(id, owner string) {
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, model{ID: id, Object: "model", Created: now, OwnedBy: owner})
	}
	for combosRows.Next() {
		var slug string
		_ = combosRows.Scan(&slug)
		push("combo:"+slug, "tenant")
	}
	// connections → provider well-knowns
	connRows, err := db.Pool().Query(r.Context(), `
		SELECT DISTINCT provider FROM connections WHERE tenant_id = $1 AND enabled = TRUE
	`, ctx.TenantID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	defer connRows.Close()
	for connRows.Next() {
		var p string
		_ = connRows.Scan(&p)
		for _, m := range wellKnown[p] {
			push(p+":"+m, p)
		}
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   out,
	})
}
