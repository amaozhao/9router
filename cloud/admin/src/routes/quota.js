// /api/me/quota — every user's current daily quota snapshot.
// /api/admin/tenants[/:id/quota] — super-admin tenant browsing + quota override.

import {
  query, ValidationError, NotFoundError,
  readQuotaUsage, invalidateQuotaCache, PLAN_QUOTAS,
} from '@9router-cloud/shared';
import { readJson, ok } from '../lib/http.js';
import { requireSession, requireSuperAdmin } from '../middleware/sessionAuth.js';

export async function meQuota(req, res) {
  const session = requireSession(req);
  // Look up the plan for accurate fallback limits.
  const { rows } = await query(
    `SELECT plan FROM tenants WHERE id = $1`, [session.tenantId]
  );
  if (rows.length === 0) throw new NotFoundError('Tenant not found');
  const usage = await readQuotaUsage(session.tenantId, rows[0].plan);
  ok(res, usage);
}

export async function listTenants(req, res) {
  await requireSuperAdmin(req);
  // Join tenant_quotas + a 24h usage summary for the super-admin view.
  const { rows } = await query(`
    SELECT
      t.id, t.name, t.plan, t.status, t.created_at,
      tq.daily_token_limit, tq.daily_request_limit, tq.note,
      (SELECT COUNT(*) FROM users u WHERE u.tenant_id = t.id)        AS user_count,
      (SELECT COUNT(*) FROM api_keys ak WHERE ak.tenant_id = t.id
         AND ak.revoked_at IS NULL)                                   AS active_keys,
      (SELECT COUNT(*) FROM connections c WHERE c.tenant_id = t.id
         AND c.enabled)                                               AS active_connections
    FROM tenants t
    LEFT JOIN tenant_quotas tq ON tq.tenant_id = t.id
    ORDER BY t.id DESC
    LIMIT 500
  `);
  const items = await Promise.all(rows.map(async (r) => {
    const usage = await readQuotaUsage(Number(r.id), r.plan);
    return {
      id: Number(r.id),
      name: r.name,
      plan: r.plan,
      status: r.status,
      createdAt: r.created_at,
      userCount: Number(r.user_count),
      activeKeys: Number(r.active_keys),
      activeConnections: Number(r.active_connections),
      quota: {
        dailyTokenLimit:   r.daily_token_limit   != null ? Number(r.daily_token_limit)   : null,
        dailyRequestLimit: r.daily_request_limit != null ? Number(r.daily_request_limit) : null,
        note: r.note,
        defaults: PLAN_QUOTAS[r.plan] || PLAN_QUOTAS.free,
        effective: { tokens: usage.tokens.limit, requests: usage.requests.limit },
        usage: { tokens: usage.tokens.used, requests: usage.requests.used, date: usage.date },
      },
    };
  }));
  ok(res, { items });
}

export async function putTenantQuota(req, res, { params }) {
  await requireSuperAdmin(req);
  const id = Number(params.id);
  if (!Number.isInteger(id) || id <= 0) throw new ValidationError('Invalid tenant id');
  const body = await readJson(req);

  // null/undefined explicitly removes the override → fall back to plan default.
  const tokens = parseLimit(body.dailyTokenLimit, 'dailyTokenLimit');
  const requests = parseLimit(body.dailyRequestLimit, 'dailyRequestLimit');
  const note = body.note != null ? String(body.note).slice(0, 200) : null;

  // Confirm tenant exists.
  const t = await query('SELECT id FROM tenants WHERE id = $1', [id]);
  if (t.rowCount === 0) throw new NotFoundError('Tenant not found');

  await query(`
    INSERT INTO tenant_quotas (tenant_id, daily_token_limit, daily_request_limit, note, updated_at)
    VALUES ($1, $2, $3, $4, now())
    ON CONFLICT (tenant_id) DO UPDATE SET
      daily_token_limit = EXCLUDED.daily_token_limit,
      daily_request_limit = EXCLUDED.daily_request_limit,
      note = EXCLUDED.note,
      updated_at = now()
  `, [id, tokens, requests, note]);

  await invalidateQuotaCache(id);
  ok(res, { ok: true, tenantId: id, dailyTokenLimit: tokens, dailyRequestLimit: requests });
}

function parseLimit(v, field) {
  if (v === null || v === undefined || v === '') return null;
  const n = Number(v);
  if (!Number.isFinite(n) || n < 0 || !Number.isInteger(n)) {
    throw new ValidationError(`${field} must be a non-negative integer or null`);
  }
  return n;
}
