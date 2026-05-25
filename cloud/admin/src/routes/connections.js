// /api/connections — upstream provider credentials per tenant.
// Plaintext credentials are encrypted with the per-tenant DEK before storage
// and never read back (the response only describes shape).

import { query, encryptForTenant, NotFoundError, ValidationError, getRedis } from '@9router-cloud/shared';
import { readJson, ok, noContent } from '../lib/http.js';
import { requireSession, requireRole } from '../middleware/sessionAuth.js';

export async function listConnections(req, res) {
  const s = requireSession(req);
  const { rows } = await query(`
    SELECT id, provider, name, auth_type, metadata, enabled, weight, oauth_expires_at, created_at, updated_at
    FROM connections
    WHERE tenant_id = $1
    ORDER BY provider ASC, id ASC
  `, [s.tenantId]);
  ok(res, { items: rows.map(serialize) });
}

export async function createConnection(req, res) {
  const s = requireSession(req);
  requireRole(s, 'owner', 'admin');
  const body = await readJson(req);

  const provider = required(body, 'provider').toLowerCase().trim();
  const name = required(body, 'name').trim();
  const authType = (body.authType || 'api_key').toLowerCase();
  if (!['api_key', 'oauth', 'passthrough'].includes(authType)) {
    throw new ValidationError(`authType must be one of api_key|oauth|passthrough`);
  }
  if (!body.credentials || typeof body.credentials !== 'object') {
    throw new ValidationError('credentials object is required');
  }
  if (authType === 'api_key' && !body.credentials.api_key) {
    throw new ValidationError('credentials.api_key is required for authType=api_key');
  }
  const metadata = body.metadata && typeof body.metadata === 'object' ? body.metadata : {};
  const weight = Number.isInteger(body.weight) ? body.weight : 1;
  const enabled = body.enabled !== false;

  const blob = encryptForTenant(s.tenantId, body.credentials);
  let row;
  try {
    const r = await query(`
      INSERT INTO connections (tenant_id, provider, name, auth_type, credentials_encrypted, metadata, enabled, weight)
      VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
      RETURNING id, provider, name, auth_type, metadata, enabled, weight, oauth_expires_at, created_at, updated_at
    `, [s.tenantId, provider, name, authType, blob, metadata, enabled, weight]);
    row = r.rows[0];
  } catch (e) {
    if (e.code === '23505') {
      throw new ValidationError(`Connection (${provider}, ${name}) already exists`);
    }
    throw e;
  }
  await invalidateConnCache(s.tenantId, provider);
  ok(res, serialize(row), 201);
}

export async function updateConnection(req, res, { params }) {
  const s = requireSession(req);
  requireRole(s, 'owner', 'admin');
  const id = Number(params.id);
  const body = await readJson(req);

  // Fetch current (for tenant scoping + provider for cache invalidation)
  const cur = await query(`SELECT id, provider FROM connections WHERE id = $1 AND tenant_id = $2`, [id, s.tenantId]);
  if (cur.rowCount === 0) throw new NotFoundError('Connection not found');
  const provider = cur.rows[0].provider;

  const sets = [];
  const args = [];
  let i = 1;
  if (body.name !== undefined) { sets.push(`name = $${i++}`); args.push(String(body.name).trim()); }
  if (body.metadata !== undefined) { sets.push(`metadata = $${i++}`); args.push(body.metadata); }
  if (body.enabled !== undefined) { sets.push(`enabled = $${i++}`); args.push(!!body.enabled); }
  if (Number.isInteger(body.weight)) { sets.push(`weight = $${i++}`); args.push(body.weight); }
  if (body.credentials && typeof body.credentials === 'object') {
    sets.push(`credentials_encrypted = $${i++}`);
    args.push(encryptForTenant(s.tenantId, body.credentials));
  }
  if (sets.length === 0) throw new ValidationError('No updatable fields supplied');
  sets.push(`updated_at = now()`);
  args.push(id, s.tenantId);
  const { rows } = await query(
    `UPDATE connections SET ${sets.join(', ')}
     WHERE id = $${i++} AND tenant_id = $${i}
     RETURNING id, provider, name, auth_type, metadata, enabled, weight, oauth_expires_at, created_at, updated_at`,
    args
  );
  await invalidateConnCache(s.tenantId, provider);
  ok(res, serialize(rows[0]));
}

export async function deleteConnection(req, res, { params }) {
  const s = requireSession(req);
  requireRole(s, 'owner', 'admin');
  const id = Number(params.id);
  const { rows } = await query(
    `DELETE FROM connections WHERE id = $1 AND tenant_id = $2 RETURNING provider`,
    [id, s.tenantId]
  );
  if (rows.length === 0) throw new NotFoundError('Connection not found');
  await invalidateConnCache(s.tenantId, rows[0].provider);
  noContent(res);
}

async function invalidateConnCache(tenantId, provider) {
  await getRedis().del(`conn_cache:${tenantId}:${provider}`);
}

function required(obj, field) {
  if (obj[field] === undefined || obj[field] === null || obj[field] === '') {
    throw new ValidationError(`Missing field: ${field}`);
  }
  return String(obj[field]);
}

function serialize(row) {
  return {
    id: Number(row.id),
    provider: row.provider,
    name: row.name,
    authType: row.auth_type,
    metadata: row.metadata || {},
    enabled: row.enabled,
    weight: row.weight,
    oauthExpiresAt: row.oauth_expires_at,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}
