// Package worker hosts the background jobs: usage_summaries aggregation and
// OAuth token refresh. Spawned by cmd/worker.
package worker

import (
	"context"
	"time"

	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/logger"
)

// AggregateOnce rolls usage_events of the last 25h into usage_summaries via
// UPSERT. Cheap to re-run; the current hour gets re-aggregated each tick.
// Returns the number of rows touched.
func AggregateOnce(ctx context.Context) (int64, error) {
	started := time.Now()
	tag, err := db.Pool().Exec(ctx, `
		INSERT INTO usage_summaries (
		  tenant_id, bucket_hour, provider, model,
		  request_count, prompt_tokens, completion_tokens, cost_micros, error_count
		)
		SELECT
		  tenant_id,
		  date_trunc('hour', ts) AS bucket_hour,
		  provider,
		  model,
		  COUNT(*)::bigint                                AS request_count,
		  SUM(prompt_tokens)::bigint                      AS prompt_tokens,
		  SUM(completion_tokens)::bigint                  AS completion_tokens,
		  SUM(cost_micros)::bigint                        AS cost_micros,
		  COUNT(*) FILTER (WHERE status <> 'ok')::bigint  AS error_count
		FROM usage_events
		WHERE ts > now() - interval '25 hours'
		GROUP BY tenant_id, date_trunc('hour', ts), provider, model
		ON CONFLICT (tenant_id, bucket_hour, provider, model) DO UPDATE SET
		  request_count     = EXCLUDED.request_count,
		  prompt_tokens     = EXCLUDED.prompt_tokens,
		  completion_tokens = EXCLUDED.completion_tokens,
		  cost_micros       = EXCLUDED.cost_micros,
		  error_count       = EXCLUDED.error_count
	`)
	if err != nil {
		return 0, err
	}
	n := tag.RowsAffected()
	logger.Info("aggregated", "rows", n, "ms", time.Since(started).Milliseconds())
	return n, nil
}

// ScheduleAggregator runs AggregateOnce once immediately and then at every
// interval until ctx is cancelled. Re-entrant safe.
func ScheduleAggregator(ctx context.Context, interval time.Duration) {
	go func() {
		var busy bool
		run := func() {
			if busy {
				return
			}
			busy = true
			defer func() { busy = false }()
			if _, err := AggregateOnce(ctx); err != nil {
				logger.Error("aggregate failed", "err", err)
			}
		}
		run()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				run()
			case <-ctx.Done():
				return
			}
		}
	}()
}
