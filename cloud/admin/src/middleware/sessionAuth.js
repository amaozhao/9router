// Session auth for the admin API — verifies the Authorization: Bearer <jwt> header.

import { AuthError, ForbiddenError } from '@9router-cloud/shared';
import { verifyJwt } from '../lib/jwt.js';

export function requireSession(req) {
  const h = req.headers['authorization'] || '';
  if (!h.startsWith('Bearer ')) throw new AuthError('Missing Bearer token');
  const payload = verifyJwt(h.slice(7));
  return {
    userId: payload.sub,
    tenantId: payload.tid,
    role: payload.role,
  };
}

export function requireRole(session, ...allowed) {
  if (!allowed.includes(session.role)) {
    throw new ForbiddenError(`Requires role: ${allowed.join('|')}`, { actual: session.role });
  }
}
