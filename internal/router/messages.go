// /v1/messages — Anthropic-shape entry point.
// Provider routing:
//   provider == "claude"  → ClaudeSub (Anthropic-native, no body translation)
//   otherwise             → OpenAI-compat upstream + translator (Anthropic↔OpenAI)
package router

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/amaozhao/lazirouter/internal/combo"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/httpx"
	"github.com/amaozhao/lazirouter/internal/logger"
	"github.com/amaozhao/lazirouter/internal/picker"
	"github.com/amaozhao/lazirouter/internal/quota"
	"github.com/amaozhao/lazirouter/internal/scenario"
	"github.com/amaozhao/lazirouter/internal/tenantctx"
	"github.com/amaozhao/lazirouter/internal/translator"
	"github.com/amaozhao/lazirouter/internal/usage"
	"github.com/google/uuid"
)

func (d *Deps) handleMessages(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	reqID := uuid.NewString()

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
	requestedModel, _ := body["model"].(string)
	model := requestedModel
	if model == "" {
		model = "auto"
	}
	stream := truthy(body["stream"])

	effectiveModel := model
	routedScenario := ""
	if model == "auto" {
		routedScenario = scenario.Classify(body)
		target, err := scenario.ResolveTarget(r.Context(), ctx.TenantID, routedScenario)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		effectiveModel = target
		logger.Info("auto-routing", "tenantId", ctx.TenantID, "scenario", routedScenario, "effectiveModel", effectiveModel)
	}

	attempts, err := combo.Resolve(r.Context(), ctx.TenantID, effectiveModel)
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
				errsList = append(errsList, map[string]any{"step": attempt.Step, "provider": attempt.Provider, "reason": ae.Message})
				continue
			}
			httpx.WriteError(w, err)
			return
		}

		ueBase := &usage.Event{
			TenantID: ctx.TenantID, APIKeyID: ctx.APIKeyID, ConnectionID: account.ConnectionID,
			Provider: attempt.Provider, Model: model, UpstreamModel: attempt.UpstreamModel,
			RequestID: reqID, Meta: map[string]any{"transport": "/v1/messages"},
		}
		if routedScenario != "" {
			ueBase.RoutedModel = &effectiveModel
		}

		var callErr error
		switch {
		case attempt.Provider == "claude":
			// Anthropic-native pass-through via Claude subscription executor.
			body["model"] = attempt.UpstreamModel
			if stream {
				callErr = d.streamClaude(r.Context(), w, account, body, ueBase, started, ctx.TenantID, account.ConnectionID)
			} else {
				callErr = d.nonStreamClaude(r.Context(), w, account, body, ueBase, started, ctx.TenantID, requestedModel)
			}
		case stringFrom(account.Metadata, "protocol") == "anthropic_passthrough":
			// Third-party Anthropic-compat endpoint (e.g. MiniMax Token Plan).
			// No translation; forward Anthropic body verbatim, auth via x-api-key.
			baseURL := stringFrom(account.Metadata, "base_url")
			upstreamKey := stringFrom(account.Credentials, "api_key")
			if baseURL == "" || upstreamKey == "" {
				errsList = append(errsList, map[string]any{"step": attempt.Step, "reason": "anthropic_passthrough requires base_url + api_key"})
				continue
			}
			body["model"] = attempt.UpstreamModel
			if stream {
				callErr = d.streamMessagesViaAnthropicCompat(r.Context(), w, baseURL, upstreamKey, account.Metadata, body, ueBase, started)
			} else {
				callErr = d.nonStreamMessagesViaAnthropicCompat(r.Context(), w, baseURL, upstreamKey, account.Metadata, body, ueBase, started)
			}
		default:
			// OpenAI-compatible upstream via translator.
			baseURL := stringFrom(account.Metadata, "base_url")
			if baseURL == "" {
				baseURL = httpx.DefaultBaseURL(attempt.Provider)
			}
			upstreamKey := stringFrom(account.Credentials, "api_key")
			if baseURL == "" || upstreamKey == "" {
				errsList = append(errsList, map[string]any{"step": attempt.Step, "reason": "connection missing base_url or api_key"})
				continue
			}
			openaiBody := translator.AnthropicToOpenAIRequest(body)
			openaiBody["model"] = attempt.UpstreamModel
			if stream {
				callErr = d.streamMessagesViaOpenAI(r.Context(), w, baseURL, upstreamKey, openaiBody, requestedModel, ueBase, started)
			} else {
				callErr = d.nonStreamMessagesViaOpenAI(r.Context(), w, baseURL, upstreamKey, openaiBody, requestedModel, ueBase, started)
			}
		}

		if callErr == nil {
			return
		}

		var ae *errs.AppError
		status := 0
		if errors.As(callErr, &ae) {
			if sc, ok := ae.Meta["status_code"].(int); ok {
				status = sc
			}
		}
		retriable := status == 429 || (status >= 500 && status < 600) || !errors.As(callErr, &ae)
		if retriable && status > 0 {
			_, _ = picker.MarkCooldown(r.Context(), ctx.TenantID, attempt.Provider, account.ConnectionID)
		}
		errsList = append(errsList, map[string]any{"step": attempt.Step, "provider": attempt.Provider, "status": status, "message": callErr.Error()})
		if !retriable {
			httpx.WriteError(w, callErr)
			return
		}
	}

	httpx.WriteError(w, errs.Upstream("All upstream attempts failed", map[string]any{"attempts": errsList}))
}

// --- claude subscription paths ---

func (d *Deps) nonStreamClaude(ctx context.Context, w http.ResponseWriter, account *picker.Account, body map[string]any, ue *usage.Event, started time.Time, tenantID int64, requestedModel string) error {
	result, err := d.ClaudeSub.ChatMessages(ctx, account.Credentials, account.Metadata, body)
	if err != nil {
		_ = usage.Record(ctx, errEvent(ue, started, err))
		return err
	}
	u, _ := result["usage"].(map[string]any)
	prompt := toI64Or(u, "input_tokens")
	completion := toI64Or(u, "output_tokens")
	pricing := usage.GetPricing(ctx, tenantID, "claude", ue.UpstreamModel)
	ue.Status, ue.LatencyMs = "ok", time.Since(started).Milliseconds()
	ue.PromptTokens, ue.CompletionTokens = prompt, completion
	ue.CostMicros = usage.ComputeCost(pricing, prompt, completion)
	_ = usage.Record(ctx, ue)
	httpx.WriteJSON(w, http.StatusOK, result)
	_ = requestedModel
	return nil
}

func (d *Deps) streamClaude(ctx context.Context, w http.ResponseWriter, account *picker.Account, body map[string]any, ue *usage.Event, started time.Time, tenantID int64, connID int64) error {
	frames, errc := d.ClaudeSub.StreamMessages(ctx, account.Credentials, account.Metadata, body)
	var prompt, completion int64
	headersSent := false
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				goto done
			}
			if !headersSent {
				w.Header().Set("content-type", "text/event-stream")
				w.Header().Set("cache-control", "no-cache, no-transform")
				w.Header().Set("connection", "keep-alive")
				w.WriteHeader(http.StatusOK)
				headersSent = true
			}
			_, _ = w.Write(frame)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			// Claude SSE has `event: message_delta\ndata: {"usage":{...}}`
			p, c := scanAnthropicUsage(frame)
			if p > 0 {
				prompt = p
			}
			if c > 0 {
				completion = c
			}
		case err := <-errc:
			if !headersSent {
				_ = usage.Record(ctx, errEvent(ue, started, err))
				return err
			}
			logger.Warn("claude stream error after headers sent", "err", err)
			goto done
		case <-ctx.Done():
			return nil
		}
	}
done:
	pricing := usage.GetPricing(ctx, tenantID, "claude", ue.UpstreamModel)
	ue.Status, ue.LatencyMs = "ok", time.Since(started).Milliseconds()
	ue.PromptTokens, ue.CompletionTokens = prompt, completion
	ue.CostMicros = usage.ComputeCost(pricing, prompt, completion)
	_ = usage.Record(ctx, ue)
	_ = connID
	return nil
}

// --- openai-compat paths (translate request and response) ---

func (d *Deps) nonStreamMessagesViaOpenAI(ctx context.Context, w http.ResponseWriter, baseURL, apiKey string, openaiBody map[string]any, requestedModel string, ue *usage.Event, started time.Time) error {
	result, err := d.OpenAI.ChatCompletion(ctx, baseURL, apiKey, openaiBody)
	if err != nil {
		_ = usage.Record(ctx, errEvent(ue, started, err))
		return err
	}
	anthropicResp := translator.OpenAIToAnthropicResponse(result, requestedModel)
	u, _ := result["usage"].(map[string]any)
	prompt := toI64Or(u, "prompt_tokens")
	completion := toI64Or(u, "completion_tokens")
	pricing := usage.GetPricing(ctx, ue.TenantID, ue.Provider, ue.UpstreamModel)
	ue.Status, ue.LatencyMs = "ok", time.Since(started).Milliseconds()
	ue.PromptTokens, ue.CompletionTokens = prompt, completion
	ue.CostMicros = usage.ComputeCost(pricing, prompt, completion)
	_ = usage.Record(ctx, ue)
	httpx.WriteJSON(w, http.StatusOK, anthropicResp)
	return nil
}

func (d *Deps) streamMessagesViaOpenAI(ctx context.Context, w http.ResponseWriter, baseURL, apiKey string, openaiBody map[string]any, requestedModel string, ue *usage.Event, started time.Time) error {
	frames, errc := d.OpenAI.StreamChatCompletion(ctx, baseURL, apiKey, openaiBody)
	xlate := translator.NewStream(requestedModel)
	headersSent := false
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				goto flush
			}
			if !headersSent {
				w.Header().Set("content-type", "text/event-stream")
				w.Header().Set("cache-control", "no-cache, no-transform")
				w.Header().Set("connection", "keep-alive")
				w.WriteHeader(http.StatusOK)
				headersSent = true
			}
			for _, out := range xlate.Feed(frame) {
				if _, err := w.Write(out); err != nil {
					return nil
				}
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		case err := <-errc:
			if !headersSent {
				_ = usage.Record(ctx, errEvent(ue, started, err))
				return err
			}
			logger.Warn("openai stream error after headers sent", "err", err)
			goto flush
		case <-ctx.Done():
			return nil
		}
	}
flush:
	for _, out := range xlate.Flush() {
		_, _ = w.Write(out)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	prompt, completion := xlate.Usage()
	pricing := usage.GetPricing(ctx, ue.TenantID, ue.Provider, ue.UpstreamModel)
	ue.Status, ue.LatencyMs = "ok", time.Since(started).Milliseconds()
	ue.PromptTokens, ue.CompletionTokens = prompt, completion
	ue.CostMicros = usage.ComputeCost(pricing, prompt, completion)
	_ = usage.Record(ctx, ue)
	return nil
}

// --- anthropic-compat passthrough (third-party endpoints mimicking /v1/messages) ---

func (d *Deps) nonStreamMessagesViaAnthropicCompat(ctx context.Context, w http.ResponseWriter, baseURL, apiKey string, metadata, body map[string]any, ue *usage.Event, started time.Time) error {
	result, err := d.AnthropicCompat.ChatMessages(ctx, baseURL, apiKey, metadata, body)
	if err != nil {
		_ = usage.Record(ctx, errEvent(ue, started, err))
		return err
	}
	u, _ := result["usage"].(map[string]any)
	prompt := toI64Or(u, "input_tokens")
	completion := toI64Or(u, "output_tokens")
	pricing := usage.GetPricing(ctx, ue.TenantID, ue.Provider, ue.UpstreamModel)
	ue.Status, ue.LatencyMs = "ok", time.Since(started).Milliseconds()
	ue.PromptTokens, ue.CompletionTokens = prompt, completion
	ue.CostMicros = usage.ComputeCost(pricing, prompt, completion)
	_ = usage.Record(ctx, ue)
	httpx.WriteJSON(w, http.StatusOK, result)
	return nil
}

func (d *Deps) streamMessagesViaAnthropicCompat(ctx context.Context, w http.ResponseWriter, baseURL, apiKey string, metadata, body map[string]any, ue *usage.Event, started time.Time) error {
	frames, errc := d.AnthropicCompat.StreamMessages(ctx, baseURL, apiKey, metadata, body)
	var prompt, completion int64
	headersSent := false
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				goto done
			}
			if !headersSent {
				w.Header().Set("content-type", "text/event-stream")
				w.Header().Set("cache-control", "no-cache, no-transform")
				w.Header().Set("connection", "keep-alive")
				w.WriteHeader(http.StatusOK)
				headersSent = true
			}
			_, _ = w.Write(frame)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			p, c := scanAnthropicUsage(frame)
			if p > 0 {
				prompt = p
			}
			if c > 0 {
				completion = c
			}
		case err := <-errc:
			if !headersSent {
				_ = usage.Record(ctx, errEvent(ue, started, err))
				return err
			}
			logger.Warn("anthropic-compat stream error after headers sent", "err", err)
			goto done
		case <-ctx.Done():
			return nil
		}
	}
done:
	pricing := usage.GetPricing(ctx, ue.TenantID, ue.Provider, ue.UpstreamModel)
	ue.Status, ue.LatencyMs = "ok", time.Since(started).Milliseconds()
	ue.PromptTokens, ue.CompletionTokens = prompt, completion
	ue.CostMicros = usage.ComputeCost(pricing, prompt, completion)
	_ = usage.Record(ctx, ue)
	return nil
}

func toI64Or(m map[string]any, k string) int64 {
	if m == nil {
		return 0
	}
	return toI64(m[k])
}
