// Token refresher — proactively rotates OAuth access tokens for tenant
// connections that are about to expire. Each provider plugs in via the
// registry: a refresh function that takes the decrypted credentials object
// and returns the new credentials + the new expiry.
//
// Operating model:
//   * Every 30s we SELECT connections where auth_type='oauth' AND
//     oauth_expires_at IS within the next 90s.
//   * Plugins are looked up by `provider`. Missing plugin = skip (log warn).
//   * On success we re-encrypt and UPDATE; on failure we record an error in
//     metadata.last_refresh_error and let the next tick retry.
//   * We hold per-connection in-process locks so concurrent ticks don't
//     refresh the same row twice on a slow upstream.

import {
  query, encryptForTenant, decryptForTenant, getRedis, logger,
} from '@lazirouter-cloud/shared';

const log = logger.child({ job: 'token-refresher' });

const refreshers = new Map(); // provider → async (credentials, ctx) => { credentials, expiresAtIso, meta? }

export function registerRefresher(provider, fn) {
  refreshers.set(provider, fn);
}

/** Single sweep. Returns number of rows refreshed. */
export async function refreshOnce() {
  const startedAt = Date.now();
  const { rows } = await query(`
    SELECT id, tenant_id, provider, name, credentials_encrypted, metadata
    FROM connections
    WHERE auth_type = 'oauth' AND enabled = TRUE
      AND oauth_expires_at IS NOT NULL
      AND oauth_expires_at < now() + interval '90 seconds'
    ORDER BY oauth_expires_at ASC
    LIMIT 200
  `);
  if (rows.length === 0) return 0;

  let refreshed = 0;
  for (const row of rows) {
    const provider = row.provider;
    const fn = refreshers.get(provider);
    if (!fn) {
      log.warn({ provider }, 'no refresher registered, skipping');
      continue;
    }
    // Cross-process lock via Redis SETNX
    const lockKey = `lock:refresh:${row.id}`;
    const got = await getRedis().set(lockKey, '1', 'EX', 30, 'NX');
    if (got !== 'OK') continue;
    try {
      const current = decryptForTenant(Number(row.tenant_id), row.credentials_encrypted);
      const next = await fn(current, { tenantId: Number(row.tenant_id), connectionId: Number(row.id), name: row.name });
      if (!next || !next.credentials) throw new Error('refresher returned no credentials');
      const blob = encryptForTenant(Number(row.tenant_id), next.credentials);
      await query(`
        UPDATE connections SET
          credentials_encrypted = $1,
          oauth_expires_at = $2,
          metadata = COALESCE(metadata, '{}'::jsonb) || $3::jsonb,
          updated_at = now()
        WHERE id = $4
      `, [blob, next.expiresAt || null, JSON.stringify(next.meta || {}), row.id]);
      await getRedis().del(`conn_cache:${row.tenant_id}:${provider}`);
      refreshed++;
      log.info({ tenantId: row.tenant_id, connectionId: row.id, provider }, 'refreshed');
    } catch (err) {
      log.error({ err: err.message, connectionId: row.id }, 'refresh failed');
      await query(`
        UPDATE connections SET
          metadata = COALESCE(metadata, '{}'::jsonb) || jsonb_build_object('last_refresh_error', $1, 'last_refresh_attempt', now())
        WHERE id = $2
      `, [err.message.slice(0, 200), row.id]);
    } finally {
      await getRedis().del(lockKey);
    }
  }
  log.info({ refreshed, scanned: rows.length, ms: Date.now() - startedAt }, 'refresh tick done');
  return refreshed;
}

/** Schedule periodic refresh. */
export function scheduleRefresher(intervalMs = 30_000) {
  let running = false;
  const tick = async () => {
    if (running) return;
    running = true;
    try { await refreshOnce(); }
    catch (err) { log.error({ err: err.message }, 'tick error'); }
    finally { running = false; }
  };
  tick();
  return setInterval(tick, intervalMs);
}
