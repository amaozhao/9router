// Package usage handles per-request usage events: cost computation, DB INSERT
// into usage_events, and the post-call token-quota bump.
package usage

import (
	"context"
	"encoding/json"

	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/quota"
)

// Pricing is the per-1M-tokens rate (in micro-USD) for a (provider, model).
type Pricing struct {
	PromptMicros     int64
	CompletionMicros int64
}

// Event is the per-request payload written to usage_events.
type Event struct {
	TenantID         int64
	APIKeyID         int64
	ConnectionID     int64
	Provider         string
	Model            string
	RoutedModel      *string
	UpstreamModel    string
	RequestID        string
	Status           string // "ok" | "error"
	LatencyMs        int64
	PromptTokens     int64
	CompletionTokens int64
	CostMicros       int64
	ErrorCode        string
	Meta             map[string]any
}

// GetPricing reads tenant-scoped pricing (NULL tenant_id = default).
func GetPricing(ctx context.Context, tenantID int64, provider, upstreamModel string) Pricing {
	var p Pricing
	row := db.Pool().QueryRow(ctx, `
		SELECT prompt_tokens_per_million_micros, completion_tokens_per_million_micros
		FROM pricing
		WHERE (tenant_id = $1 OR tenant_id IS NULL)
		  AND provider = $2 AND model = $3
		ORDER BY tenant_id DESC NULLS LAST
		LIMIT 1
	`, tenantID, provider, upstreamModel)
	_ = row.Scan(&p.PromptMicros, &p.CompletionMicros)
	return p
}

// ComputeCost returns total micro-USD for a (prompt, completion) pair given
// per-1M rates.
func ComputeCost(p Pricing, prompt, completion int64) int64 {
	return prompt*p.PromptMicros/1_000_000 + completion*p.CompletionMicros/1_000_000
}

// Record writes an Event row and bumps the daily token counter on success.
func Record(ctx context.Context, e *Event) error {
	metaJSON, _ := json.Marshal(e.Meta)
	_, err := db.Pool().Exec(ctx, `
		INSERT INTO usage_events
		  (tenant_id, api_key_id, connection_id, provider, model, routed_model,
		   upstream_model, request_id, status, latency_ms,
		   prompt_tokens, completion_tokens, cost_micros, error_code, meta, ts)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15, now())
	`,
		e.TenantID, e.APIKeyID, e.ConnectionID, e.Provider, e.Model, e.RoutedModel,
		e.UpstreamModel, e.RequestID, e.Status, e.LatencyMs,
		e.PromptTokens, e.CompletionTokens, e.CostMicros, e.ErrorCode, metaJSON,
	)
	if err != nil {
		return err
	}
	if e.Status == "ok" {
		_ = quota.IncrementTokens(ctx, e.TenantID, e.PromptTokens+e.CompletionTokens)
	}
	return nil
}
