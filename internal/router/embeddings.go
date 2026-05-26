package router

import (
	"errors"
	"net/http"
	"time"

	"github.com/amaozhao/lazirouter/internal/combo"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/httpx"
	"github.com/amaozhao/lazirouter/internal/picker"
	"github.com/amaozhao/lazirouter/internal/quota"
	"github.com/amaozhao/lazirouter/internal/tenantctx"
	"github.com/amaozhao/lazirouter/internal/usage"
	"github.com/google/uuid"
)

func (d *Deps) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	reqID := uuid.NewString()

	apiKey := httpx.BearerOrXAPIKey(r)
	if apiKey == "" {
		httpx.WriteError(w, errs.Auth("Missing Authorization header"))
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
	if err := quota.Enforce(r.Context(), ctx); err != nil {
		httpx.WriteError(w, err)
		return
	}

	body, err := httpx.ReadJSONMap(r)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	model, _ := body["model"].(string)
	if model == "" {
		httpx.WriteError(w, errs.Validation("Missing field: model"))
		return
	}
	if _, ok := body["input"]; !ok {
		httpx.WriteError(w, errs.Validation("Missing field: input"))
		return
	}

	attempts, err := combo.Resolve(r.Context(), ctx.TenantID, model)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}

	var errsList []map[string]any
	for _, attempt := range attempts {
		account, err := picker.Pick(r.Context(), d.Cfg.MasterKey, ctx.TenantID, attempt.Provider)
		if err != nil {
			var ae *errs.AppError
			if errors.As(err, &ae) && ae.Code == "no_account_available" {
				errsList = append(errsList, map[string]any{"step": attempt.Step, "reason": ae.Message})
				continue
			}
			httpx.WriteError(w, err)
			return
		}
		baseURL := stringFrom(account.Metadata, "base_url")
		if baseURL == "" {
			baseURL = httpx.DefaultBaseURL(attempt.Provider)
		}
		upstreamKey := stringFrom(account.Credentials, "api_key")
		if baseURL == "" || upstreamKey == "" {
			errsList = append(errsList, map[string]any{"step": attempt.Step, "reason": "connection missing base_url or api_key"})
			continue
		}

		upstreamBody := cloneMap(body)
		upstreamBody["model"] = attempt.UpstreamModel

		result, err := d.OpenAI.Embed(r.Context(), baseURL, upstreamKey, upstreamBody)
		ue := &usage.Event{
			TenantID: ctx.TenantID, APIKeyID: ctx.APIKeyID, ConnectionID: account.ConnectionID,
			Provider: attempt.Provider, Model: model, UpstreamModel: attempt.UpstreamModel,
			RequestID: reqID, Meta: map[string]any{"transport": "/v1/embeddings"},
		}
		if err != nil {
			var ae *errs.AppError
			status := 0
			if errors.As(err, &ae) {
				if sc, ok := ae.Meta["status_code"].(int); ok {
					status = sc
				}
			}
			retriable := status == 429 || (status >= 500 && status < 600) || !errors.As(err, &ae)
			if retriable && status > 0 {
				_, _ = picker.MarkCooldown(r.Context(), ctx.TenantID, attempt.Provider, account.ConnectionID)
			}
			_ = usage.Record(r.Context(), errEvent(ue, started, err))
			errsList = append(errsList, map[string]any{
				"step": attempt.Step, "provider": attempt.Provider, "status": status, "message": err.Error(),
			})
			if !retriable {
				httpx.WriteError(w, err)
				return
			}
			continue
		}
		prompt := toI64(asMap(result["usage"])["prompt_tokens"])
		pricing := usage.GetPricing(r.Context(), ctx.TenantID, attempt.Provider, attempt.UpstreamModel)
		ue.Status, ue.LatencyMs = "ok", time.Since(started).Milliseconds()
		ue.PromptTokens = prompt
		ue.CostMicros = usage.ComputeCost(pricing, prompt, 0)
		_ = usage.Record(r.Context(), ue)

		httpx.WriteJSON(w, http.StatusOK, result)
		return
	}

	httpx.WriteError(w, errs.NoAccountAvailable(map[string]any{"attempts": errsList}))
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}
