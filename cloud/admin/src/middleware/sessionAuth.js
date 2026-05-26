// Session auth for the admin API — verifies the Authorization: Bearer <jwt> header.

import { AuthError, ForbiddenError, isSuperAdminEmail, query } from '@lazirouter-cloud/shared';
import { verifyJwt } from '../lib/jwt.js';

export function requireSession(req) {
  const h = req.headers['authorization'] || '';
  if (!h.startsWith('Bearer ')) throw new AuthError('Missing Bearer token');
  const payload = verifyJwt(h.slice(7));
  return {
    userId: payload.sub,
    tenantId: payload.tid,
    role: payload.role,
    email: payload.email || null,
  };
}

export function requireRole(session, ...allowed) {
  if (!allowed.includes(session.role)) {
    throw new ForbiddenError(`Requires role: ${allowed.join('|')}`, { actual: session.role });
  }
}

// Super-admin check. Uses the email embedded in the JWT when available, falling
// back to a DB lookup. Throws ForbiddenError when the user is not a super-admin.
export async function requireSuperAdmin(req) {
  const session = requireSession(req);
  let email = session.email;
  if (!email) {
    const { rows } = await query('SELECT email FROM users WHERE id = $1', [session.userId]);
    email = rows[0]?.email || null;
  }
  if (!isSuperAdminEmail(email)) {
    throw new ForbiddenError('Super-admin only', { email });
  }
  return { ...session, email, isSuperAdmin: true };
}
