// /auth/signup, /auth/login, /auth/me
//
// signup: creates a new tenant (current user becomes owner). One-call onboarding.
// login: email + password → JWT.
// me: returns current user + tenant from JWT.

import { query, tx, ValidationError, AuthError } from '@9router-cloud/shared';
import { hashPassword, verifyPassword } from '../lib/password.js';
import { signJwt } from '../lib/jwt.js';
import { readJson, ok, requireField } from '../lib/http.js';
import { requireSession } from '../middleware/sessionAuth.js';

export async function signup(req, res) {
  const body = await readJson(req);
  const email = requireField(body, 'email').toLowerCase().trim();
  const password = requireField(body, 'password');
  const tenantName = body.tenantName?.trim() || `${email.split('@')[0]}'s workspace`;

  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
    throw new ValidationError('Invalid email');
  }
  if (password.length < 8) {
    throw new ValidationError('Password must be ≥ 8 chars');
  }

  const existing = await query('SELECT 1 FROM users WHERE LOWER(email) = $1', [email]);
  if (existing.rowCount > 0) {
    throw new ValidationError('Email already registered', { field: 'email' });
  }

  const hash = await hashPassword(password);

  const result = await tx(async (client) => {
    const t = await client.query(
      `INSERT INTO tenants (name, plan, status) VALUES ($1, 'free', 'active') RETURNING id`,
      [tenantName]
    );
    const tenantId = Number(t.rows[0].id);
    const u = await client.query(
      `INSERT INTO users (tenant_id, email, password_hash, role) VALUES ($1, $2, $3, 'owner') RETURNING id`,
      [tenantId, email, hash]
    );
    return { tenantId, userId: Number(u.rows[0].id) };
  });

  const token = signJwt({ sub: result.userId, tid: result.tenantId, role: 'owner' });
  ok(res, {
    token,
    user: { id: result.userId, email, role: 'owner' },
    tenant: { id: result.tenantId, name: tenantName, plan: 'free' },
  }, 201);
}

export async function login(req, res) {
  const body = await readJson(req);
  const email = requireField(body, 'email').toLowerCase().trim();
  const password = requireField(body, 'password');

  const { rows } = await query(`
    SELECT u.id, u.email, u.password_hash, u.role, u.tenant_id,
           t.name AS tenant_name, t.plan, t.status AS tenant_status
    FROM users u
    JOIN tenants t ON t.id = u.tenant_id
    WHERE LOWER(u.email) = $1
    LIMIT 1
  `, [email]);
  if (rows.length === 0) throw new AuthError('Invalid credentials');
  const u = rows[0];
  if (u.tenant_status !== 'active') throw new AuthError('Tenant is not active');

  const okPw = await verifyPassword(password, u.password_hash);
  if (!okPw) throw new AuthError('Invalid credentials');

  const token = signJwt({ sub: Number(u.id), tid: Number(u.tenant_id), role: u.role });
  ok(res, {
    token,
    user: { id: Number(u.id), email: u.email, role: u.role },
    tenant: { id: Number(u.tenant_id), name: u.tenant_name, plan: u.plan },
  });
}

export async function me(req, res) {
  const session = requireSession(req);
  const { rows } = await query(`
    SELECT u.id, u.email, u.role, u.tenant_id,
           t.name AS tenant_name, t.plan, t.status AS tenant_status
    FROM users u
    JOIN tenants t ON t.id = u.tenant_id
    WHERE u.id = $1 AND u.tenant_id = $2
  `, [session.userId, session.tenantId]);
  if (rows.length === 0) throw new AuthError('Session user not found');
  const u = rows[0];
  ok(res, {
    user: { id: Number(u.id), email: u.email, role: u.role },
    tenant: { id: Number(u.tenant_id), name: u.tenant_name, plan: u.plan, status: u.tenant_status },
  });
}
