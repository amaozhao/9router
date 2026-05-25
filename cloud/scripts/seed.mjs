#!/usr/bin/env node
// Seed minimal dev data: 1 tenant + 1 user + 1 api_key + 1 mock connection.
// Idempotent: re-running upserts by deterministic keys, prints the API key only once.

import pg from 'pg';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const sharedPath = resolve(dirname(fileURLToPath(import.meta.url)), '../shared/src');
const { encryptForTenant, generateApiKey, sha256Hex } = await import(`${sharedPath}/crypto.js`);

const DATABASE_URL = process.env.DATABASE_URL || 'postgres://router:router_dev_pw@localhost:55432/router';
const SEED_API_KEY = process.env.SEED_API_KEY; // if set, will create/replace with this exact key

const client = new pg.Client({ connectionString: DATABASE_URL });

async function main() {
  await client.connect();
  await client.query('BEGIN');
  try {
    // Tenant
    const t = await upsert(`
      INSERT INTO tenants (name, plan, status)
      VALUES ('Demo Tenant', 'pro', 'active')
      ON CONFLICT DO NOTHING
      RETURNING id
    `);
    let tenantId;
    if (t) tenantId = Number(t.id);
    else {
      const { rows } = await client.query(`SELECT id FROM tenants WHERE name = 'Demo Tenant' LIMIT 1`);
      tenantId = Number(rows[0].id);
    }
    console.log(`tenant_id = ${tenantId}`);

    // User (owner)
    const userEmail = 'owner@demo.local';
    const userId = await ensureRow(
      `SELECT id FROM users WHERE LOWER(email)=LOWER($1)`,
      [userEmail],
      `INSERT INTO users (tenant_id, email, role) VALUES ($1, $2, 'owner') RETURNING id`,
      [tenantId, userEmail],
    );
    console.log(`user_id = ${userId} (${userEmail})`);

    // API key — generate or use SEED_API_KEY
    const apiKey = SEED_API_KEY || generateApiKey();
    const hash = sha256Hex(apiKey);
    const prefix = apiKey.slice(0, 12);
    // If a key already exists with name 'seed', replace it
    await client.query(`DELETE FROM api_keys WHERE tenant_id=$1 AND name='seed'`, [tenantId]);
    const { rows: keyRows } = await client.query(`
      INSERT INTO api_keys (tenant_id, created_by, name, key_prefix, key_hash)
      VALUES ($1, $2, 'seed', $3, $4)
      RETURNING id
    `, [tenantId, userId, prefix, hash]);
    console.log(`api_key_id = ${keyRows[0].id}`);
    console.log('');
    console.log(`╔════════════════════════════════════════════════════════════╗`);
    console.log(`║  TEST API KEY (only printed once — copy now!)              ║`);
    console.log(`╠════════════════════════════════════════════════════════════╣`);
    console.log(`║  ${apiKey.padEnd(58)}║`);
    console.log(`╚════════════════════════════════════════════════════════════╝`);
    console.log('');

    // Mock provider connection — points to the LOCAL fake upstream we'll start during integration tests.
    // For real testing later, replace base_url + api_key with a real provider.
    const mockUpstream = process.env.SEED_UPSTREAM_URL || 'http://localhost:31999';
    const mockApiKey = process.env.SEED_UPSTREAM_KEY || 'fake-upstream-key';
    const cred = { api_key: mockApiKey };
    const credBlob = encryptForTenant(tenantId, cred);
    await client.query(`
      INSERT INTO connections (tenant_id, provider, name, auth_type, credentials_encrypted, metadata, enabled, weight)
      VALUES ($1, 'mock', 'mock-1', 'api_key', $2, $3, TRUE, 1)
      ON CONFLICT (tenant_id, provider, name) DO UPDATE SET
        credentials_encrypted = EXCLUDED.credentials_encrypted,
        metadata = EXCLUDED.metadata,
        enabled = TRUE,
        updated_at = now()
    `, [tenantId, credBlob, JSON.stringify({ base_url: mockUpstream })]);
    console.log(`connection upserted: provider=mock url=${mockUpstream}`);

    // Pricing — global default for mock provider
    await client.query(`
      INSERT INTO pricing (tenant_id, provider, model, prompt_price_micros_per_token, completion_price_micros_per_token)
      VALUES (NULL, 'mock', 'mock-model-1', 600, 1800)
      ON CONFLICT DO NOTHING
    `);

    await client.query('COMMIT');
    console.log('seed OK');
  } catch (err) {
    await client.query('ROLLBACK');
    throw err;
  } finally {
    await client.end();
  }
}

async function upsert(sql, params = []) {
  const { rows } = await client.query(sql, params);
  return rows[0];
}

async function ensureRow(selectSql, selectParams, insertSql, insertParams) {
  const { rows } = await client.query(selectSql, selectParams);
  if (rows.length) return Number(rows[0].id);
  const ins = await client.query(insertSql, insertParams);
  return Number(ins.rows[0].id);
}

// Need to load config (which checks env)
process.env.REDIS_URL = process.env.REDIS_URL || 'redis://localhost:56379/0';
process.env.CLOUD_MASTER_KEY = process.env.CLOUD_MASTER_KEY || 'ZjBzZjBzZjBzZjBzZjBzZjBzZjBzZjBzZjBzZjBzZjBzZjA=';

main().catch(e => { console.error(e); process.exit(1); });
