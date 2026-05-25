// scrypt-based password hashing — zero native deps, OWASP-recommended parameters.
// Format: scrypt$N=16384,r=8,p=1$<salt-hex>$<derived-hex>

import crypto from 'node:crypto';
import { promisify } from 'node:util';

const scrypt = promisify(crypto.scrypt);
const N = 16384, r = 8, p = 1, keylen = 64;

export async function hashPassword(plain) {
  const salt = crypto.randomBytes(16);
  const derived = await scrypt(plain, salt, keylen, { N, r, p });
  return `scrypt$N=${N},r=${r},p=${p}$${salt.toString('hex')}$${derived.toString('hex')}`;
}

export async function verifyPassword(plain, stored) {
  if (!stored || !stored.startsWith('scrypt$')) return false;
  const parts = stored.split('$');
  if (parts.length !== 4) return false;
  const params = Object.fromEntries(parts[1].split(',').map(p => p.split('=')));
  const salt = Buffer.from(parts[2], 'hex');
  const expected = Buffer.from(parts[3], 'hex');
  const derived = await scrypt(plain, salt, expected.length, {
    N: Number(params.N), r: Number(params.r), p: Number(params.p),
  });
  return crypto.timingSafeEqual(derived, expected);
}
