// /v1/responses — OpenAI Responses-API entry point. Codex subscription is
// the only provider that's "Responses-native" today; this handler errors out
// for everything else.
package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/amaozhao/lazirouter/internal/combo"
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

func (d *Deps) handleResponses(w http.ResponseWriter, r *http.Request) {
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
	model, _ := body["model"].(string)
	if model == "" {
		httpx.WriteError(w, errs.Validation("Missing field: model"))
		return
	}
	if _, hasInput := body["input"]; !hasInput {
		if _, hasMsgs := body["messages"].([]any); !hasMsgs {
			httpx.WriteError(w, errs.Validation("Missing field: input[] or messages[]"))
			return
		}
	}
	// Default to streaming because the Codex backend doesn't support non-stream.
	stream := true
	if v, ok := body["stream"]; ok && v == false {
		stream = false
	}

	attempts, err := combo.Resolve(r.Context(), ctx.TenantID, model)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}

	var errsList []map[string]any
	for _, attempt := range attempts {
		if attempt.Provider != "codex" {
			errsList = append(errsList, map[string]any{"step": attempt.Step, "reason": "provider " + attempt.Provider + " does not expose a Responses API path"})
			continue
		}
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
		body["model"] = attempt.UpstreamModel
		sc := provider.SessionContext{TenantID: ctx.TenantID, ConnectionID: account.ConnectionID}
		ueBase := &usage.Event{
			TenantID: ctx.TenantID, APIKeyID: ctx.APIKeyID, ConnectionID: account.ConnectionID,
			Provider: attempt.Provider, Model: model, UpstreamModel: attempt.UpstreamModel,
			RequestID: reqID, Meta: map[string]any{"transport": "/v1/responses"},
		}
		var callErr error
		if stream {
			callErr = d.streamResponses(r.Context(), w, account, body, sc, ueBase, started)
		} else {
			callErr = d.nonStreamResponses(r.Context(), w, account, body, sc, ueBase, started)
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

func (d *Deps) nonStreamResponses(ctx context.Context, w http.ResponseWriter, account *picker.Account, body map[string]any, sc provider.SessionContext, ue *usage.Event, started time.Time) error {
	raw, err := d.CodexSub.ResponsesCall(ctx, account.Credentials, account.Metadata, body, sc)
	if err != nil {
		_ = usage.Record(ctx, errEvent(ue, started, err))
		return err
	}
	prompt, completion := extractResponsesUsage(raw)
	pricing := usage.GetPricing(ctx, ue.TenantID, ue.Provider, ue.UpstreamModel)
	ue.Status, ue.LatencyMs = "ok", time.Since(started).Milliseconds()
	ue.PromptTokens, ue.CompletionTokens = prompt, completion
	ue.CostMicros = usage.ComputeCost(pricing, prompt, completion)
	_ = usage.Record(ctx, ue)
	// Codex backend always streams; we forward the SSE text under an
	// event-stream content type so clients can parse frames the same way.
	w.Header().Set("content-type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
	return nil
}

func (d *Deps) streamResponses(ctx context.Context, w http.ResponseWriter, account *picker.Account, body map[string]any, sc provider.SessionContext, ue *usage.Event, started time.Time) error {
	frames, errc := d.CodexSub.StreamResponses(ctx, account.Credentials, account.Metadata, body, sc)
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
			if p, c := extractResponsesUsage(frame); p > 0 {
				prompt, completion = p, c
			}
		case err := <-errc:
			if !headersSent {
				_ = usage.Record(ctx, errEvent(ue, started, err))
				return err
			}
			logger.Warn("codex stream error after headers sent", "err", err)
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

// extractResponsesUsage parses the last `event: response.completed` frame from
// a chunk of SSE text. Image_gen / web_search usage objects are nested inside
// other events and zero-valued, so we must target response.completed precisely.
func extractResponsesUsage(buf []byte) (int64, int64) {
	lines := strings.Split(string(buf), "\n")
	var event, lastData string
	for _, ln := range lines {
		if strings.HasPrefix(ln, "event: ") {
			event = strings.TrimSpace(ln[7:])
		} else if strings.HasPrefix(ln, "data: ") && event == "response.completed" {
			lastData = ln[6:]
		} else if ln == "" {
			event = ""
		}
	}
	if lastData == "" {
		return 0, 0
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(lastData), &obj); err != nil {
		return 0, 0
	}
	resp, _ := obj["response"].(map[string]any)
	usage, _ := resp["usage"].(map[string]any)
	return toI64(usage["input_tokens"]), toI64(usage["output_tokens"])
}

// scanAnthropicUsage pulls input/output tokens from Anthropic stream frames
// (event: message_delta or message_start with usage in the data payload).
func scanAnthropicUsage(buf []byte) (int64, int64) {
	lines := strings.Split(string(buf), "\n")
	for _, ln := range lines {
		if !strings.HasPrefix(ln, "data: ") {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(ln[6:]), &obj); err != nil {
			continue
		}
		// usage may be at top level (message_start) or nested under message.usage
		// or inside message_delta.usage.
		if u, ok := obj["usage"].(map[string]any); ok {
			p := toI64(u["input_tokens"])
			c := toI64(u["output_tokens"])
			if p > 0 || c > 0 {
				return p, c
			}
		}
		if msg, ok := obj["message"].(map[string]any); ok {
			if u, ok := msg["usage"].(map[string]any); ok {
				return toI64(u["input_tokens"]), toI64(u["output_tokens"])
			}
		}
	}
	return 0, 0
}
