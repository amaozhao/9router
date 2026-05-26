// Centralized configuration. Reads environment variables once and freezes the result.
// Throws at load time if a required variable is missing.

function required(name) {
  const v = process.env[name];
  if (!v) throw new Error(`Missing required environment variable: ${name}`);
  return v;
}

function optional(name, fallback) {
  const v = process.env[name];
  return v === undefined || v === '' ? fallback : v;
}

function int(name, fallback) {
  const v = process.env[name];
  if (v === undefined || v === '') return fallback;
  const n = Number(v);
  if (!Number.isInteger(n)) throw new Error(`Env ${name} must be an integer, got "${v}"`);
  return n;
}

function csvLower(name, fallback = []) {
  const v = process.env[name];
  if (v === undefined || v === '') return fallback;
  return v.split(',').map(s => s.trim().toLowerCase()).filter(Boolean);
}

export const config = Object.freeze({
  databaseUrl: required('DATABASE_URL'),
  redisUrl: required('REDIS_URL'),
  masterKey: required('CLOUD_MASTER_KEY'),       // base64-encoded 32-byte key
  routerPort: int('ROUTER_PORT', 30100),
  adminPort: int('ADMIN_PORT', 30200),
  jwtSecret: optional('JWT_SECRET', 'dev-only-do-not-use-in-prod'),
  logLevel: optional('LOG_LEVEL', 'info'),
  nodeEnv: optional('NODE_ENV', 'development'),
  apiKeyCacheTtlSec: int('APIKEY_CACHE_TTL', 300),
  // Lowercased CSV; users whose email matches any entry are treated as global super-admins.
  superAdminEmails: csvLower('SUPER_ADMIN_EMAILS', []),
});

export function isProd() {
  return config.nodeEnv === 'production';
}

export function isSuperAdminEmail(email) {
  if (!email) return false;
  return config.superAdminEmails.includes(String(email).toLowerCase());
}
