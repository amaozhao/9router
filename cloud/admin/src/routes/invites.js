// /api/admin/invites — super-admin-only mint/list/revoke of signup invite codes.
//
// Codes are consumed at /auth/signup time (see auth.js) inside a single
// transaction so concurrent signups can't oversubscribe a code.

import crypto from 'node:crypto';
import { query, ValidationError, NotFoundError } from '@9router-cloud/shared';
import { readJson, ok, noContent } from '../lib/http.js';
import { requireSuperAdmin } from '../middleware/sessionAuth.js';

function generateCode() {
  // 12 chars, base36, ambiguous letters (0/O/I/l) dropped.
  const alphabet = '23456789ABCDEFGHJKMNPQRSTUVWXYZ';
  const bytes = crypto.randomBytes(12);
  let out = '';
  for (let i = 0; i < 12; i++) out += alphabet[bytes[i] % alphabet.length];
  return out;
}

export async function listInvites(req, res) {
  await requireSuperAdmin(req);
  const { rows } = await query(`
    SELECT id, code, max_uses, used_count, expires_at, note, created_by, enabled, created_at
    FROM invite_codes
    ORDER BY id DESC
    LIMIT 500
  `);
  ok(res, { items: rows.map(serialize) });
}

export async function createInvite(req, res) {
  const session = await requireSuperAdmin(req);
  const body = await readJson(req);
  const maxUses = Number.isInteger(body.maxUses) && body.maxUses > 0 ? body.maxUses : 1;
  const note = body.note ? String(body.note).slice(0, 200) : null;

  let expiresAt = null;
  if (body.expiresAt) {
    const d = new Date(body.expiresAt);
    if (Number.isNaN(d.getTime())) throw new ValidationError('Invalid expiresAt');
    expiresAt = d.toISOString();
  }

  // Retry on the (astronomically unlikely) UNIQUE collision.
  for (let attempt = 0; attempt < 5; attempt++) {
    const code = generateCode();
    try {
      const { rows } = await query(`
        INSERT INTO invite_codes (code, max_uses, expires_at, note, created_by)
        VALUES ($1, $2, $3, $4, $5)
        RETURNING id, code, max_uses, used_count, expires_at, note, created_by, enabled, created_at
      `, [code, maxUses, expiresAt, note, session.userId]);
      return ok(res, serialize(rows[0]), 201);
    } catch (e) {
      if (e.code === '23505' && attempt < 4) continue;
      throw e;
    }
  }
}

export async function disableInvite(req, res, { params }) {
  await requireSuperAdmin(req);
  const code = String(params.code || '').trim();
  if (!code) throw new ValidationError('Missing code');
  const { rows } = await query(`
    UPDATE invite_codes SET enabled = FALSE
    WHERE code = $1
    RETURNING id
  `, [code]);
  if (rows.length === 0) throw new NotFoundError('Invite code not found');
  noContent(res);
}

function serialize(row) {
  return {
    id: Number(row.id),
    code: row.code,
    maxUses: row.max_uses,
    usedCount: row.used_count,
    expiresAt: row.expires_at,
    note: row.note,
    createdBy: row.created_by != null ? Number(row.created_by) : null,
    enabled: row.enabled,
    createdAt: row.created_at,
  };
}
