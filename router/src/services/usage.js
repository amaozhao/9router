// Usage recording. Phase 1: synchronous INSERT into usage_events.
// Phase 4 swaps this for a Kafka producer; the call sites won't change.

import { query, logger, incrementTokenUsage } from '@lazirouter-cloud/shared';

export async function recordUsage(event) {
  const totalTokens = (event.promptTokens ?? 0) + (event.completionTokens ?? 0);
  try {
    await query(`
      INSERT INTO usage_events (
        tenant_id, api_key_id, connection_id, provider, model, routed_model, upstream_model,
        prompt_tokens, completion_tokens, total_tokens, cost_micros,
        status, latency_ms, request_id, error_code, meta
      ) VALUES (
        $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16
      )
    `, [
      event.tenantId,
      event.apiKeyId ?? null,
      event.connectionId ?? null,
      event.provider,
      event.model,
      event.routedModel ?? null,
      event.upstreamModel ?? null,
      event.promptTokens ?? 0,
      event.completionTokens ?? 0,
      totalTokens,
      event.costMicros ?? 0,
      event.status,
      event.latencyMs ?? null,
      event.requestId ?? null,
      event.errorCode ?? null,
      event.meta ? JSON.stringify(event.meta) : null,
    ]);
  } catch (err) {
    logger.error({ err: err.message, event }, 'failed to record usage');
  }
  // Bump the daily token quota counter regardless of status — any tokens that
  // actually crossed the wire (including partial streams that later errored)
  // should count against the tenant's daily cap.
  if (totalTokens > 0 && event.tenantId) {
    try {
      await incrementTokenUsage(event.tenantId, totalTokens);
    } catch (err) {
      logger.warn({ err: err.message, tenantId: event.tenantId }, 'token quota incr failed');
    }
  }
}

/**
 * Lookup unit pricing for a (tenant, provider, model). Returns micros/token
 * for prompt & completion. Tenant-specific override wins over global default.
 */
export async function getPricing(tenantId, provider, model) {
  const { rows } = await query(`
    SELECT prompt_price_micros_per_token AS prompt, completion_price_micros_per_token AS completion
    FROM pricing
    WHERE provider = $1 AND model = $2
      AND (tenant_id = $3 OR tenant_id IS NULL)
      AND effective_from <= now()
    ORDER BY tenant_id IS NULL ASC, effective_from DESC
    LIMIT 1
  `, [provider, model, tenantId]);
  if (rows.length === 0) return null;
  return { promptMicros: Number(rows[0].prompt), completionMicros: Number(rows[0].completion) };
}

export function computeCost(pricing, promptTokens, completionTokens) {
  if (!pricing) return 0;
  return pricing.promptMicros * promptTokens + pricing.completionMicros * completionTokens;
}
