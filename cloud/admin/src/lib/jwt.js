// Minimal HS256 JWT signer/verifier. No external deps.
// Tokens carry { sub: user_id, tid: tenant_id, role, iat, exp }.

import crypto from 'node:crypto';
import { config, AuthError } from '@lazirouter-cloud/shared';

const ALG = { alg: 'HS256', typ: 'JWT' };

function b64url(buf) {
  return Buffer.from(buf).toString('base64')
    .replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_');
}
function b64urlDecode(s) {
  s = s.replace(/-/g, '+').replace(/_/g, '/');
  while (s.length % 4) s += '=';
  return Buffer.from(s, 'base64');
}

export function signJwt(payload, expiresInSec = 60 * 60 * 12) {
  const now = Math.floor(Date.now() / 1000);
  const body = { iat: now, exp: now + expiresInSec, ...payload };
  const head = b64url(JSON.stringify(ALG));
  const data = b64url(JSON.stringify(body));
  const sig = b64url(
    crypto.createHmac('sha256', config.jwtSecret).update(`${head}.${data}`).digest()
  );
  return `${head}.${data}.${sig}`;
}

export function verifyJwt(token) {
  if (!token || typeof token !== 'string') throw new AuthError('Missing token');
  const parts = token.split('.');
  if (parts.length !== 3) throw new AuthError('Malformed token');
  const [head, data, sig] = parts;
  const expected = b64url(
    crypto.createHmac('sha256', config.jwtSecret).update(`${head}.${data}`).digest()
  );
  // Length mismatch would throw RangeError from timingSafeEqual; reject as bad sig.
  const sigBuf = Buffer.from(sig);
  const expBuf = Buffer.from(expected);
  if (sigBuf.length !== expBuf.length || !crypto.timingSafeEqual(sigBuf, expBuf)) {
    throw new AuthError('Bad signature');
  }
  let payload;
  try {
    payload = JSON.parse(b64urlDecode(data).toString('utf8'));
  } catch {
    throw new AuthError('Malformed payload');
  }
  if (typeof payload.exp === 'number' && payload.exp < Math.floor(Date.now() / 1000)) {
    throw new AuthError('Token expired');
  }
  return payload;
}
