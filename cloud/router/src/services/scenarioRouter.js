// Per-tenant scenario→target resolver.
// Reads from tenant_routing table; caches the full per-tenant map in Redis
// for 30s. PUT/DELETE in the admin route must call invalidateTenantRouting().

import { query, getRedis, ValidationError, logger } from '@9router-cloud/shared';

const CACHE_TTL_SECONDS = 30;

function cacheKey(tenantId) {
  return `routing:${tenantId}`;
}

async function loadTenantRouting(tenantId) {
  const r = getRedis();
  const cached = await r.get(cacheKey(tenantId));
  if (cached) {
    try { return JSON.parse(cached); }
    catch (e) { logger.warn({ err: e.message, tenantId }, 'routing cache parse failed; refetching'); }
  }
  const { rows } = await query(
    `SELECT scenario, target FROM tenant_routing WHERE tenant_id = $1`,
    [tenantId],
  );
  const map = Object.create(null);
  for (const row of rows) map[row.scenario] = row.target;
  await r.set(cacheKey(tenantId), JSON.stringify(map), 'EX', CACHE_TTL_SECONDS);
  return map;
}

export async function invalidateTenantRouting(tenantId) {
  await getRedis().del(cacheKey(tenantId));
}

export async function resolveScenarioTarget(tenantId, scenario) {
  const map = await loadTenantRouting(tenantId);
  if (map[scenario]) return map[scenario];
  // Scenario not configured — fall back to default.
  if (scenario !== 'default' && map.default) return map.default;
  throw new ValidationError(
    'Tenant has no default auto-routing target. Configure it in the dashboard.',
  );
}
