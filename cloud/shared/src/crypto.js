// Envelope encryption for upstream-provider credentials.
//
// Design:
//   - In production, replace generateDek() with a call to AWS KMS / GCP KMS / Vault.
//     Each tenant gets a KMS context "tenant-<id>" so the audit trail is per-tenant.
//   - In dev, we use a local 32-byte master key (CLOUD_MASTER_KEY, base64) and derive
//     per-tenant DEKs via HKDF.
//   - The on-disk blob format is:
//       [1 byte version] [12 bytes IV] [16 bytes tag] [ciphertext]
//     For envelope mode (future), we'd prefix the wrapped DEK before the IV.

import crypto from 'node:crypto';
import { config } from './config.js';

const VERSION = 0x01;

function getMasterKey() {
  const raw = Buffer.from(config.masterKey, 'base64');
  if (raw.length !== 32) {
    throw new Error('CLOUD_MASTER_KEY must decode to exactly 32 bytes (base64-encoded)');
  }
  return raw;
}

/** Derive a per-tenant 32-byte data encryption key from the master key. */
function deriveDek(tenantId) {
  const salt = Buffer.from('9router-cloud-tenant', 'utf8');
  const info = Buffer.from(`tenant-${tenantId}`, 'utf8');
  return crypto.hkdfSync('sha256', getMasterKey(), salt, info, 32);
}

/**
 * Encrypt arbitrary JSON-serializable data for a tenant.
 * Returns a Buffer suitable for storing in BYTEA.
 */
export function encryptForTenant(tenantId, plain) {
  const key = Buffer.from(deriveDek(tenantId));
  const iv = crypto.randomBytes(12);
  const cipher = crypto.createCipheriv('aes-256-gcm', key, iv);
  const json = Buffer.from(JSON.stringify(plain), 'utf8');
  const ct = Buffer.concat([cipher.update(json), cipher.final()]);
  const tag = cipher.getAuthTag();
  return Buffer.concat([Buffer.from([VERSION]), iv, tag, ct]);
}

/** Decrypt a blob produced by encryptForTenant. */
export function decryptForTenant(tenantId, blob) {
  if (!Buffer.isBuffer(blob)) blob = Buffer.from(blob);
  if (blob[0] !== VERSION) {
    throw new Error(`Unsupported crypto blob version: ${blob[0]}`);
  }
  const iv = blob.subarray(1, 13);
  const tag = blob.subarray(13, 29);
  const ct = blob.subarray(29);
  const key = Buffer.from(deriveDek(tenantId));
  const decipher = crypto.createDecipheriv('aes-256-gcm', key, iv);
  decipher.setAuthTag(tag);
  const plain = Buffer.concat([decipher.update(ct), decipher.final()]);
  return JSON.parse(plain.toString('utf8'));
}

/** sha256(secret) hex — used to hash API keys before storing them. */
export function sha256Hex(secret) {
  return crypto.createHash('sha256').update(secret).digest('hex');
}

/** Generate a new client-facing API key (sk-9r-<22 base32 chars>). */
export function generateApiKey() {
  const random = crypto.randomBytes(20);
  const body = random.toString('base64url').slice(0, 28);
  return `sk-9r-${body}`;
}
