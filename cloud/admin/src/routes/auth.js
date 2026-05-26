// /auth/signup, /auth/login, /auth/me
//
// signup: creates a new tenant (current user becomes owner). One-call onboarding.
// login: email + password → JWT.
// me: returns current user + tenant from JWT.

import {
  query, tx, ValidationError, AuthError, isSuperAdminEmail,
  generateApiKey, sha256Hex,
} from '@9router-cloud/shared';
import { hashPassword, verifyPassword } from '../lib/password.js';
import { signJwt } from '../lib/jwt.js';
import { readJson, ok, requireField } from '../lib/http.js';
import { requireSession } from '../middleware/sessionAuth.js';

export async function signup(req, res) {
  const body = await readJson(req);
  const email = requireField(body, 'email').toLowerCase().trim();
  const password = requireField(body, 'password');
  const tenantName = body.tenantName?.trim() || `${email.split('@')[0]}'s workspace`;
  const inviteCode = (body.inviteCode ? String(body.inviteCode) : '').trim();
  const isSuperAdmin = isSuperAdminEmail(email);

  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
    throw new ValidationError('Invalid email');
  }
  if (password.length < 8) {
    throw new ValidationError('Password must be ≥ 8 chars');
  }
  // Super-admins skip the invite gate so the first admin can bootstrap.
  if (!isSuperAdmin && !inviteCode) {
    throw new ValidationError('Invite code is required', { field: 'inviteCode' });
  }

  const existing = await query('SELECT 1 FROM users WHERE LOWER(email) = $1', [email]);
  if (existing.rowCount > 0) {
    throw new ValidationError('Email already registered', { field: 'email' });
  }

  const hash = await hashPassword(password);

  // Default API key is created in the same tx so the user has something usable
  // on first login. The plaintext is returned exactly once via the response.
  const defaultKey = generateApiKey();
  const defaultKeyHash = sha256Hex(defaultKey);
  const defaultKeyPrefix = defaultKey.slice(0, 14);

  let result;
  try {
    result = await tx(async (client) => {
      if (!isSuperAdmin) {
        const cr = await client.query(`
          SELECT id, max_uses, used_count, expires_at, enabled
          FROM invite_codes
          WHERE code = $1
          FOR UPDATE
        `, [inviteCode]);
        if (cr.rowCount === 0) {
          throw new ValidationError('Invalid invite code', { field: 'inviteCode' });
        }
        const c = cr.rows[0];
        if (!c.enabled) throw new ValidationError('Invite code disabled', { field: 'inviteCode' });
        if (c.expires_at && new Date(c.expires_at) < new Date()) {
          throw new ValidationError('Invite code expired', { field: 'inviteCode' });
        }
        if (c.used_count >= c.max_uses) {
          throw new ValidationError('Invite code already used', { field: 'inviteCode' });
        }
        await client.query(
          'UPDATE invite_codes SET used_count = used_count + 1 WHERE id = $1',
          [c.id]
        );
      }

      const t = await client.query(
        `INSERT INTO tenants (name, plan, status) VALUES ($1, 'free', 'active') RETURNING id`,
        [tenantName]
      );
      const tenantId = Number(t.rows[0].id);
      const u = await client.query(
        `INSERT INTO users (tenant_id, email, password_hash, role) VALUES ($1, $2, $3, 'owner') RETURNING id`,
        [tenantId, email, hash]
      );
      const userId = Number(u.rows[0].id);
      await client.query(`
        INSERT INTO api_keys (tenant_id, created_by, name, key_prefix, key_hash)
        VALUES ($1, $2, $3, $4, $5)
      `, [tenantId, userId, 'default', defaultKeyPrefix, defaultKeyHash]);
      return { tenantId, userId };
    });
  } catch (err) {
    // Pass through ValidationError thrown inside the tx unchanged.
    throw err;
  }

  const token = signJwt({ sub: result.userId, tid: result.tenantId, role: 'owner', email });
  ok(res, {
    token,
    user: { id: result.userId, email, role: 'owner', isSuperAdmin },
    tenant: { id: result.tenantId, name: tenantName, plan: 'free' },
    defaultApiKey: { keyPrefix: defaultKeyPrefix, key: defaultKey },
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

  const token = signJwt({ sub: Number(u.id), tid: Number(u.tenant_id), role: u.role, email: u.email });
  ok(res, {
    token,
    user: { id: Number(u.id), email: u.email, role: u.role, isSuperAdmin: isSuperAdminEmail(u.email) },
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
    user: { id: Number(u.id), email: u.email, role: u.role, isSuperAdmin: isSuperAdminEmail(u.email) },
    tenant: { id: Number(u.tenant_id), name: u.tenant_name, plan: u.plan, status: u.tenant_status },
  });
}
