// /api/keys — client-facing API key management.
// list / create / revoke. Key plaintext is shown ONLY at creation time.

import { query, sha256Hex, generateApiKey, NotFoundError, getRedis } from '@lazirouter-cloud/shared';
import { readJson, ok, noContent } from '../lib/http.js';
import { requireSession, requireRole } from '../middleware/sessionAuth.js';

export async function listKeys(req, res) {
  const s = requireSession(req);
  const { rows } = await query(`
    SELECT id, key_prefix, name, scopes, rate_limit_rpm, last_used_at, revoked_at, created_at
    FROM api_keys
    WHERE tenant_id = $1
    ORDER BY id DESC
  `, [s.tenantId]);
  ok(res, { items: rows.map(serialize) });
}

export async function createKey(req, res) {
  const s = requireSession(req);
  requireRole(s, 'owner', 'admin', 'member');
  const body = await readJson(req);
  const name = (body.name || '').toString().trim() || 'unnamed';
  const scopes = body.scopes && typeof body.scopes === 'object' ? body.scopes : {};
  const rateLimitRpm = Number.isInteger(body.rateLimitRpm) ? body.rateLimitRpm : null;

  const key = generateApiKey();
  const hash = sha256Hex(key);
  const prefix = key.slice(0, 14);

  const { rows } = await query(`
    INSERT INTO api_keys (tenant_id, created_by, name, scopes, rate_limit_rpm, key_prefix, key_hash)
    VALUES ($1, $2, $3, $4, $5, $6, $7)
    RETURNING id, key_prefix, name, scopes, rate_limit_rpm, created_at
  `, [s.tenantId, s.userId, name, scopes, rateLimitRpm, prefix, hash]);

  // The plaintext is returned exactly once. Persist nothing more than the hash.
  ok(res, { ...serialize(rows[0]), key }, 201);
}

export async function revokeKey(req, res, { params }) {
  const s = requireSession(req);
  requireRole(s, 'owner', 'admin');
  const id = Number(params.id);
  const { rows } = await query(`
    UPDATE api_keys SET revoked_at = now()
    WHERE id = $1 AND tenant_id = $2 AND revoked_at IS NULL
    RETURNING key_hash
  `, [id, s.tenantId]);
  if (rows.length === 0) throw new NotFoundError('API key not found');
  // Invalidate the cached lookup so the next call from this key fails immediately.
  await getRedis().del(`apikey:${rows[0].key_hash}`);
  noContent(res);
}

function serialize(row) {
  return {
    id: Number(row.id),
    name: row.name,
    keyPrefix: row.key_prefix,
    scopes: row.scopes || {},
    rateLimitRpm: row.rate_limit_rpm,
    lastUsedAt: row.last_used_at,
    revokedAt: row.revoked_at,
    createdAt: row.created_at,
  };
}
