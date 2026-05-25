// /api/usage — read-only usage views for the current tenant.

import { query } from '@9router-cloud/shared';
import { ok } from '../lib/http.js';
import { requireSession } from '../middleware/sessionAuth.js';

export async function summary(req, res) {
  const s = requireSession(req);
  const url = new URL(req.url, 'http://x');
  const sinceHours = Number(url.searchParams.get('hours')) || 24;
  const { rows } = await query(`
    SELECT provider, model,
           SUM(prompt_tokens)::bigint AS prompt_tokens,
           SUM(completion_tokens)::bigint AS completion_tokens,
           SUM(cost_micros)::bigint AS cost_micros,
           COUNT(*)::bigint AS request_count,
           COUNT(*) FILTER (WHERE status <> 'ok')::bigint AS error_count
    FROM usage_events
    WHERE tenant_id = $1 AND ts > now() - $2 * interval '1 hour'
    GROUP BY provider, model
    ORDER BY request_count DESC
  `, [s.tenantId, sinceHours]);
  ok(res, {
    sinceHours,
    items: rows.map(r => ({
      provider: r.provider,
      model: r.model,
      requestCount: Number(r.request_count),
      errorCount: Number(r.error_count),
      promptTokens: Number(r.prompt_tokens),
      completionTokens: Number(r.completion_tokens),
      costMicros: Number(r.cost_micros),
      costUsd: Number(r.cost_micros) / 1_000_000,
    })),
  });
}

export async function recent(req, res) {
  const s = requireSession(req);
  const url = new URL(req.url, 'http://x');
  const limit = Math.min(Number(url.searchParams.get('limit')) || 50, 500);
  const { rows } = await query(`
    SELECT id, ts, provider, model, prompt_tokens, completion_tokens,
           cost_micros, status, latency_ms, error_code
    FROM usage_events
    WHERE tenant_id = $1
    ORDER BY id DESC
    LIMIT $2
  `, [s.tenantId, limit]);
  ok(res, {
    items: rows.map(r => ({
      id: Number(r.id),
      ts: r.ts,
      provider: r.provider,
      model: r.model,
      promptTokens: r.prompt_tokens,
      completionTokens: r.completion_tokens,
      costMicros: Number(r.cost_micros),
      status: r.status,
      latencyMs: r.latency_ms,
      errorCode: r.error_code,
    })),
  });
}
