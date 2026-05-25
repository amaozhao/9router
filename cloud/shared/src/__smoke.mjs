// Smoke test for cloud/shared: connect db + redis, encrypt/decrypt round-trip.
// Run with: cd cloud/shared && node src/__smoke.mjs

import { query, closeDb } from './db.js';
import { getRedis, closeRedis } from './redis.js';
import { encryptForTenant, decryptForTenant, generateApiKey, sha256Hex } from './crypto.js';
import { logger } from './logger.js';

async function main() {
  // 1) DB roundtrip
  const r = await query('SELECT now() AS now, current_database() AS db');
  logger.info({ db: r.rows[0].db, now: r.rows[0].now }, 'db OK');

  // 2) Tables visible?
  const tables = await query(`
    SELECT table_name FROM information_schema.tables
    WHERE table_schema='public' ORDER BY table_name
  `);
  logger.info({ tables: tables.rows.map(t => t.table_name) }, 'tables');

  // 3) Redis ping
  const redis = getRedis();
  const pong = await redis.ping();
  logger.info({ pong }, 'redis OK');

  // 4) Crypto round-trip
  const tenantA = 1;
  const tenantB = 2;
  const cred = { api_key: 'sk-upstream-secret', expires_at: '2026-12-31T00:00:00Z' };
  const blob = encryptForTenant(tenantA, cred);
  const restored = decryptForTenant(tenantA, blob);
  if (JSON.stringify(restored) !== JSON.stringify(cred)) throw new Error('crypto roundtrip broken');
  logger.info({ blob_len: blob.length }, 'crypto roundtrip OK');

  // 5) Cross-tenant decrypt must fail
  let crossTenantFailed = false;
  try { decryptForTenant(tenantB, blob); }
  catch (e) { crossTenantFailed = true; }
  if (!crossTenantFailed) throw new Error('crypto isolation broken — tenant B decrypted tenant A blob!');
  logger.info('tenant isolation OK');

  // 6) API key gen + hash
  const k = generateApiKey();
  if (!k.startsWith('sk-9r-') || k.length < 30) throw new Error('api key format wrong');
  const h = sha256Hex(k);
  if (h.length !== 64) throw new Error('hash length wrong');
  logger.info({ prefix: k.slice(0, 14), hash_prefix: h.slice(0, 12) }, 'api key OK');

  logger.info('ALL SMOKE TESTS PASSED');
  await closeDb();
  await closeRedis();
}

main().catch(e => { console.error(e); process.exit(1); });
