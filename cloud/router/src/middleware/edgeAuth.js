// Edge auth: extract sk-lr-xxx → tenant context.
// Fast path: Redis cache (apikey:<hash>) → 1ms
// Slow path: Postgres query, then populate cache
// Failure mode: 401

import { query, getRedis, sha256Hex, AuthError, ForbiddenError, config, logger } from '@lazirouter-cloud/shared';

const CACHE_TTL = config.apiKeyCacheTtlSec;

/**
 * Resolve an API key string to a tenant context.
 * Returns { tenantId, apiKeyId, scopes, plan, status } or throws AuthError.
 */
export async function resolveApiKey(apiKey) {
  if (!apiKey || typeof apiKey !== 'string' || !apiKey.startsWith('sk-lr-')) {
    throw new AuthError('Invalid API key format');
  }
  const hash = sha256Hex(apiKey);
  const redis = getRedis();
  const cacheKey = `apikey:${hash}`;

  // 1) Cache hit
  const cached = await redis.get(cacheKey);
  if (cached) {
    if (cached === '__not_found__') throw new AuthError('Unknown API key');
    return JSON.parse(cached);
  }

  // 2) DB lookup
  const { rows } = await query(`
    SELECT k.id AS api_key_id, k.tenant_id, k.scopes, k.rate_limit_rpm,
           t.plan, t.status AS tenant_status
    FROM api_keys k
    JOIN tenants t ON t.id = k.tenant_id
    WHERE k.key_hash = $1
      AND k.revoked_at IS NULL
    LIMIT 1
  `, [hash]);

  if (rows.length === 0) {
    await redis.set(cacheKey, '__not_found__', 'EX', 60); // negative cache, short TTL
    throw new AuthError('Unknown API key');
  }

  const row = rows[0];
  const ctx = {
    tenantId: Number(row.tenant_id),
    apiKeyId: Number(row.api_key_id),
    scopes: row.scopes || {},
    rateLimitRpm: row.rate_limit_rpm,
    plan: row.plan,
    tenantStatus: row.tenant_status,
  };
  await redis.set(cacheKey, JSON.stringify(ctx), 'EX', CACHE_TTL);

  // 3) Last-used (fire & forget; don't await)
  query('UPDATE api_keys SET last_used_at = now() WHERE id = $1', [ctx.apiKeyId])
    .catch(err => logger.warn({ err: err.message }, 'last_used_at update failed'));

  return ctx;
}

/** Enforce tenant status — must be active or we reject the request. */
export function enforceTenantActive(ctx) {
  if (ctx.tenantStatus !== 'active') {
    throw new ForbiddenError(`Tenant is ${ctx.tenantStatus}`, { tenant_status: ctx.tenantStatus });
  }
}

/** Invalidate the cache for a key (call this on revoke or rotate). */
export async function invalidateApiKey(apiKey) {
  const hash = sha256Hex(apiKey);
  await getRedis().del(`apikey:${hash}`);
}

/** Invalidate by hash (admin path doesn't have the plaintext). */
export async function invalidateApiKeyHash(hash) {
  await getRedis().del(`apikey:${hash}`);
}
