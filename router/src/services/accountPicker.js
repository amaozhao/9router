// AccountPicker — picks the next active upstream connection for (tenant, provider).
//
// State lives in Redis so multiple router instances stay in lockstep:
//   account_cursor:{tenant}:{provider}      INT  (atomic INCR for round-robin)
//   cooldown:{tenant}:{provider}:{conn_id}  EX   (presence = in cooldown)
//   conn_cache:{tenant}:{provider}          JSON 5s  (avoids per-request DB hit)
//
// Connections that are in cooldown are skipped. If every active connection is
// cooling down we throw NoAccountAvailableError; callers may decide to fall back
// to a different combo step.

import { query, getRedis, decryptForTenant, NoAccountAvailableError, logger } from '@lazirouter-cloud/shared';

const CONN_CACHE_TTL_SEC = 5;
const COOLDOWN_DEFAULT_SEC = 60;
const COOLDOWN_BACKOFF_MAX_SEC = 30 * 60;

/**
 * Pick the next available connection for a (tenant, provider) pair.
 * Returns { connectionId, name, weight, credentials, metadata } — credentials decrypted.
 * Throws NoAccountAvailableError if none are eligible.
 */
export async function pickAccount(tenantId, provider) {
  const conns = await loadActiveConnections(tenantId, provider);
  if (conns.length === 0) {
    throw new NoAccountAvailableError({ tenantId, provider, reason: 'no connections configured' });
  }

  const redis = getRedis();

  // Pull cooldown state in one pipeline
  const pipeline = redis.pipeline();
  for (const c of conns) pipeline.exists(cooldownKey(tenantId, provider, c.id));
  const cooldowns = await pipeline.exec();
  const alive = [];
  cooldowns.forEach(([err, exists], idx) => {
    if (err) logger.warn({ err: err.message }, 'cooldown probe failed; assuming alive');
    if (!exists) alive.push(conns[idx]);
  });
  if (alive.length === 0) {
    throw new NoAccountAvailableError({ tenantId, provider, reason: 'all in cooldown' });
  }

  // Weighted round-robin: build a virtual ring sized by sum(weight), INCR to pick.
  const totalWeight = alive.reduce((s, c) => s + Math.max(1, c.weight), 0);
  const cursor = await redis.incr(cursorKey(tenantId, provider));
  const slot = ((cursor - 1) % totalWeight + totalWeight) % totalWeight;
  let acc = 0;
  let picked = alive[alive.length - 1];
  for (const c of alive) {
    acc += Math.max(1, c.weight);
    if (slot < acc) { picked = c; break; }
  }

  const credentials = decryptForTenant(tenantId, picked.credentials_encrypted);
  return {
    connectionId: picked.id,
    name: picked.name,
    weight: picked.weight,
    credentials,
    metadata: picked.metadata || {},
  };
}

/**
 * Mark a connection as in cooldown.
 * `seconds` is the base cooldown; we double on repeated failures up to COOLDOWN_BACKOFF_MAX_SEC.
 */
export async function markCooldown(tenantId, provider, connectionId, seconds = COOLDOWN_DEFAULT_SEC) {
  const redis = getRedis();
  const failureKey = `cooldown_strikes:${tenantId}:${provider}:${connectionId}`;
  const strikes = await redis.incr(failureKey);
  await redis.expire(failureKey, 60 * 60); // 1h window for backoff
  const backoff = Math.min(seconds * Math.pow(2, strikes - 1), COOLDOWN_BACKOFF_MAX_SEC);
  await redis.set(cooldownKey(tenantId, provider, connectionId), '1', 'EX', Math.ceil(backoff));
  logger.info({ tenantId, provider, connectionId, strikes, backoff }, 'cooldown set');
  return Math.ceil(backoff);
}

/** Clear a cooldown manually (e.g. when the user re-enables a connection). */
export async function clearCooldown(tenantId, provider, connectionId) {
  const redis = getRedis();
  await redis.del(cooldownKey(tenantId, provider, connectionId));
  await redis.del(`cooldown_strikes:${tenantId}:${provider}:${connectionId}`);
}

/** Invalidate the cached connection list (call on add/remove/enable change). */
export async function invalidateConnectionCache(tenantId, provider) {
  await getRedis().del(connCacheKey(tenantId, provider));
}

async function loadActiveConnections(tenantId, provider) {
  const redis = getRedis();
  const key = connCacheKey(tenantId, provider);
  const cached = await redis.get(key);
  if (cached) return JSON.parse(cached, (k, v) => {
    if (k === 'credentials_encrypted') return Buffer.from(v, 'base64');
    return v;
  });

  const { rows } = await query(`
    SELECT id, name, weight, credentials_encrypted, metadata
    FROM connections
    WHERE tenant_id = $1 AND provider = $2 AND enabled = TRUE
    ORDER BY id ASC
  `, [tenantId, provider]);

  const serializable = rows.map(r => ({
    ...r,
    id: Number(r.id),
    credentials_encrypted: Buffer.isBuffer(r.credentials_encrypted)
      ? r.credentials_encrypted.toString('base64')
      : Buffer.from(r.credentials_encrypted).toString('base64'),
  }));
  await redis.set(key, JSON.stringify(serializable), 'EX', CONN_CACHE_TTL_SEC);

  // Return with buffers (rehydrate)
  return serializable.map(s => ({ ...s, credentials_encrypted: Buffer.from(s.credentials_encrypted, 'base64') }));
}

const cursorKey   = (t, p)    => `account_cursor:${t}:${p}`;
const cooldownKey = (t, p, c) => `cooldown:${t}:${p}:${c}`;
const connCacheKey = (t, p)   => `conn_cache:${t}:${p}`;
