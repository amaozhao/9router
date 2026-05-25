// Usage aggregator — rolls completed hours of usage_events into usage_summaries.
//
// Strategy:
//   For each closed hour bucket that has events but no matching summary row,
//   compute the aggregate via GROUP BY and UPSERT into usage_summaries.
//   We re-aggregate the CURRENT hour every run (cheap) so the dashboard sees
//   near-realtime numbers without coupling to the write path.

import { query, logger } from '@9router-cloud/shared';

const log = logger.child({ job: 'usage-aggregator' });

/** Run a single aggregation pass. */
export async function aggregateOnce() {
  const startedAt = Date.now();
  // Compute aggregates from the last 25 hours (covers boundary cases at midnight UTC).
  // The UPSERT replaces existing rows so re-runs are safe.
  const result = await query(`
    INSERT INTO usage_summaries (
      tenant_id, bucket_hour, provider, model,
      request_count, prompt_tokens, completion_tokens, cost_micros, error_count
    )
    SELECT
      tenant_id,
      date_trunc('hour', ts) AS bucket_hour,
      provider,
      model,
      COUNT(*)::bigint AS request_count,
      SUM(prompt_tokens)::bigint AS prompt_tokens,
      SUM(completion_tokens)::bigint AS completion_tokens,
      SUM(cost_micros)::bigint AS cost_micros,
      COUNT(*) FILTER (WHERE status <> 'ok')::bigint AS error_count
    FROM usage_events
    WHERE ts > now() - interval '25 hours'
    GROUP BY tenant_id, date_trunc('hour', ts), provider, model
    ON CONFLICT (tenant_id, bucket_hour, provider, model) DO UPDATE SET
      request_count = EXCLUDED.request_count,
      prompt_tokens = EXCLUDED.prompt_tokens,
      completion_tokens = EXCLUDED.completion_tokens,
      cost_micros = EXCLUDED.cost_micros,
      error_count = EXCLUDED.error_count
    RETURNING tenant_id, bucket_hour
  `);
  log.info({ rows: result.rowCount, ms: Date.now() - startedAt }, 'aggregated');
  return result.rowCount;
}

/** Schedule periodic aggregation. Returns the interval handle. */
export function scheduleAggregator(intervalMs = 60_000) {
  let running = false;
  const tick = async () => {
    if (running) return;
    running = true;
    try { await aggregateOnce(); }
    catch (err) { log.error({ err: err.message }, 'aggregation failed'); }
    finally { running = false; }
  };
  // Kick once immediately
  tick();
  return setInterval(tick, intervalMs);
}
