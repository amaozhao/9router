// /api/combos — fallback-chain CRUD.

import { query, NotFoundError, ValidationError } from '@9router-cloud/shared';
import { readJson, ok, noContent } from '../lib/http.js';
import { requireSession, requireRole } from '../middleware/sessionAuth.js';

export async function listCombos(req, res) {
  const s = requireSession(req);
  const { rows } = await query(`
    SELECT id, slug, name, nodes, enabled, created_at, updated_at
    FROM combos
    WHERE tenant_id = $1
    ORDER BY slug ASC
  `, [s.tenantId]);
  ok(res, { items: rows.map(serialize) });
}

export async function createCombo(req, res) {
  const s = requireSession(req);
  requireRole(s, 'owner', 'admin');
  const body = await readJson(req);
  const slug = required(body, 'slug').toLowerCase().trim();
  if (!/^[a-z0-9_-]{1,40}$/.test(slug)) {
    throw new ValidationError('slug must be 1-40 chars of [a-z0-9_-]');
  }
  const nodes = body.nodes;
  validateNodes(nodes);
  const name = (body.name || slug).toString();
  const enabled = body.enabled !== false;

  let row;
  try {
    const r = await query(`
      INSERT INTO combos (tenant_id, slug, name, nodes, enabled)
      VALUES ($1, $2, $3, $4, $5)
      RETURNING id, slug, name, nodes, enabled, created_at, updated_at
    `, [s.tenantId, slug, name, JSON.stringify(nodes), enabled]);
    row = r.rows[0];
  } catch (e) {
    if (e.code === '23505') throw new ValidationError(`Combo ${slug} already exists`);
    throw e;
  }
  ok(res, serialize(row), 201);
}

export async function updateCombo(req, res, { params }) {
  const s = requireSession(req);
  requireRole(s, 'owner', 'admin');
  const slug = params.slug;
  const body = await readJson(req);
  const sets = [];
  const args = [];
  let i = 1;
  if (body.name !== undefined) { sets.push(`name = $${i++}`); args.push(String(body.name)); }
  if (body.nodes !== undefined) {
    validateNodes(body.nodes);
    sets.push(`nodes = $${i++}`); args.push(JSON.stringify(body.nodes));
  }
  if (body.enabled !== undefined) { sets.push(`enabled = $${i++}`); args.push(!!body.enabled); }
  if (sets.length === 0) throw new ValidationError('No updatable fields');
  sets.push(`updated_at = now()`);
  args.push(slug, s.tenantId);
  const { rows } = await query(
    `UPDATE combos SET ${sets.join(', ')}
     WHERE slug = $${i++} AND tenant_id = $${i}
     RETURNING id, slug, name, nodes, enabled, created_at, updated_at`,
    args,
  );
  if (rows.length === 0) throw new NotFoundError(`Combo ${slug} not found`);
  ok(res, serialize(rows[0]));
}

export async function deleteCombo(req, res, { params }) {
  const s = requireSession(req);
  requireRole(s, 'owner', 'admin');
  const slug = params.slug;
  const { rowCount } = await query(
    `DELETE FROM combos WHERE slug = $1 AND tenant_id = $2`,
    [slug, s.tenantId]
  );
  if (rowCount === 0) throw new NotFoundError(`Combo ${slug} not found`);
  noContent(res);
}

function validateNodes(nodes) {
  if (!Array.isArray(nodes) || nodes.length === 0) {
    throw new ValidationError('nodes must be a non-empty array');
  }
  for (let i = 0; i < nodes.length; i++) {
    const n = nodes[i];
    if (!n || typeof n !== 'object') throw new ValidationError(`nodes[${i}] must be an object`);
    if (!n.provider || typeof n.provider !== 'string') throw new ValidationError(`nodes[${i}].provider required`);
    if (!n.model || typeof n.model !== 'string') throw new ValidationError(`nodes[${i}].model required`);
  }
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
    slug: row.slug,
    name: row.name,
    nodes: row.nodes,
    enabled: row.enabled,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}
