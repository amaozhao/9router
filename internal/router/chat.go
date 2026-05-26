// Package router holds the /v1/* HTTP handlers.
package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/amaozhao/lazirouter/internal/combo"
	"github.com/amaozhao/lazirouter/internal/config"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/httpx"
	"github.com/amaozhao/lazirouter/internal/logger"
	"github.com/amaozhao/lazirouter/internal/picker"
	"github.com/amaozhao/lazirouter/internal/provider"
	"github.com/amaozhao/lazirouter/internal/quota"
	"github.com/amaozhao/lazirouter/internal/tenantctx"
	"github.com/amaozhao/lazirouter/internal/usage"
	"github.com/google/uuid"
)

// Deps is what cmd/router wires once at boot and shares with all handlers.
type Deps struct {
	Cfg       *config.Config
	OpenAI    *provider.OpenAI
	ClaudeSub *provider.ClaudeSub
	CodexSub  *provider.CodexSub
}

func New(d *Deps) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", d.handleChatCompletions)
	mux.HandleFunc("POST /v1/messages", d.handleMessages)
	mux.HandleFunc("POST /v1/responses", d.handleResponses)
	mux.HandleFunc("POST /v1/embeddings", d.handleEmbeddings)
	mux.HandleFunc("GET /v1/models", d.handleModels)
	return mux
}

// truthy is a small helper exported within the package.
func truthy(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

var usageRe = regexp.MustCompile(`"usage"\s*:\s*\{[^}]*"prompt_tokens"\s*:\s*(\d+)[^}]*"completion_tokens"\s*:\s*(\d+)`)

func (d *Deps) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
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
	if _, ok := body["messages"].([]any); !ok {
		httpx.WriteError(w, errs.Validation("Missing field: messages[]"))
		return
	}
	model, _ := body["model"].(string)
	if model == "" {
		model = "auto"
	}

	attempts, err := combo.Resolve(r.Context(), ctx.TenantID, model)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	stream, _ := body["stream"].(bool)

	var errsList []map[string]any
	for _, attempt := range attempts {
		account, err := picker.Pick(r.Context(), d.Cfg.MasterKey, ctx.TenantID, attempt.Provider)
		if err != nil {
			var ae *errs.AppError
			if errors.As(err, &ae) && ae.Code == "no_account_available" {
				errsList = append(errsList, map[string]any{
					"step": attempt.Step, "provider": attempt.Provider, "reason": ae.Message,
				})
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
			errsList = append(errsList, map[string]any{
				"step": attempt.Step, "provider": attempt.Provider, "reason": "connection missing base_url or api_key",
			})
			continue
		}

		upstreamBody := cloneMap(body)
		upstreamBody["model"] = attempt.UpstreamModel

		ueBase := &usage.Event{
			TenantID: ctx.TenantID, APIKeyID: ctx.APIKeyID, ConnectionID: account.ConnectionID,
			Provider: attempt.Provider, Model: model, UpstreamModel: attempt.UpstreamModel,
			RequestID: reqID,
		}

		if stream {
			err = d.doStream(r.Context(), w, baseURL, upstreamKey, upstreamBody, ueBase, started, ctx.TenantID, attempt.Provider, account.ConnectionID)
		} else {
			err = d.doNonStream(r.Context(), w, baseURL, upstreamKey, upstreamBody, ueBase, started, ctx.TenantID, attempt.Provider, account.ConnectionID)
		}
		if err == nil {
			return
		}
		// upstream failure → decide retry
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
		errsList = append(errsList, map[string]any{
			"step": attempt.Step, "provider": attempt.Provider, "status": status, "message": err.Error(),
		})
		// if we've already started writing the response, we can't retry
		if !retriable {
			httpx.WriteError(w, err)
			return
		}
	}

	allNoAccount := len(errsList) > 0
	for _, e := range errsList {
		if e["status"] != nil && e["status"].(int) != 0 {
			allNoAccount = false
		}
	}
	if allNoAccount {
		httpx.WriteError(w, errs.NoAccountAvailable(map[string]any{"attempts": errsList}))
		return
	}
	httpx.WriteError(w, errs.Upstream("All upstream attempts failed",
		map[string]any{"attempts": errsList, "hint": "check provider availability or configure more connections"}))
}

func (d *Deps) doNonStream(ctx context.Context, w http.ResponseWriter, baseURL, apiKey string, body map[string]any, ueBase *usage.Event, started time.Time, tenantID int64, provider string, connID int64) error {
	result, err := d.OpenAI.ChatCompletion(ctx, baseURL, apiKey, body)
	if err != nil {
		_ = usage.Record(ctx, errEvent(ueBase, started, err))
		return err
	}
	prompt, completion := extractUsageFromMap(result)
	pricing := usage.GetPricing(ctx, tenantID, provider, ueBase.UpstreamModel)
	ueBase.Status, ueBase.LatencyMs = "ok", time.Since(started).Milliseconds()
	ueBase.PromptTokens, ueBase.CompletionTokens = prompt, completion
	ueBase.CostMicros = usage.ComputeCost(pricing, prompt, completion)
	_ = usage.Record(ctx, ueBase)

	httpx.WriteJSON(w, http.StatusOK, result)
	_ = connID
	return nil
}

func (d *Deps) doStream(ctx context.Context, w http.ResponseWriter, baseURL, apiKey string, body map[string]any, ueBase *usage.Event, started time.Time, tenantID int64, provider string, connID int64) error {
	frames, errCh := d.OpenAI.StreamChatCompletion(ctx, baseURL, apiKey, body)
	var prompt, completion int64
	headersSent := false

	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				if !headersSent {
					// upstream closed with no frames; check error channel
					select {
					case err := <-errCh:
						_ = usage.Record(ctx, errEvent(ueBase, started, err))
						return err
					default:
					}
				}
				goto done
			}
			if !headersSent {
				w.Header().Set("content-type", "text/event-stream")
				w.Header().Set("cache-control", "no-cache, no-transform")
				w.Header().Set("connection", "keep-alive")
				w.WriteHeader(http.StatusOK)
				headersSent = true
			}
			if _, err := w.Write(frame); err != nil {
				logger.Warn("client write failed", "err", err)
				return nil
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if m := usageRe.FindSubmatch(frame); m != nil {
				p, _ := strconv.ParseInt(string(m[1]), 10, 64)
				c, _ := strconv.ParseInt(string(m[2]), 10, 64)
				prompt, completion = p, c
			}
		case err := <-errCh:
			if !headersSent {
				_ = usage.Record(ctx, errEvent(ueBase, started, err))
				return err
			}
			logger.Warn("upstream stream error after headers sent", "err", err)
			goto done
		case <-ctx.Done():
			return nil
		}
	}
done:
	pricing := usage.GetPricing(ctx, tenantID, provider, ueBase.UpstreamModel)
	ueBase.Status, ueBase.LatencyMs = "ok", time.Since(started).Milliseconds()
	ueBase.PromptTokens, ueBase.CompletionTokens = prompt, completion
	ueBase.CostMicros = usage.ComputeCost(pricing, prompt, completion)
	_ = usage.Record(ctx, ueBase)
	_ = connID
	return nil
}

func errEvent(base *usage.Event, started time.Time, err error) *usage.Event {
	out := *base
	out.Status = "error"
	out.LatencyMs = time.Since(started).Milliseconds()
	var ae *errs.AppError
	if errors.As(err, &ae) {
		if sc, ok := ae.Meta["status_code"].(int); ok {
			out.ErrorCode = "upstream_" + strconv.Itoa(sc)
		} else {
			out.ErrorCode = ae.Code
		}
	} else {
		out.ErrorCode = "unknown"
	}
	out.Meta = map[string]any{"error": err.Error()}
	return &out
}

func extractUsageFromMap(m map[string]any) (int64, int64) {
	u, ok := m["usage"].(map[string]any)
	if !ok {
		return 0, 0
	}
	return toI64(u["prompt_tokens"]), toI64(u["completion_tokens"])
}

func toI64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}

func stringFrom(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
