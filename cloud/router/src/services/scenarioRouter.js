// Per-tenant scenario→target resolver.
// Reads from tenant_routing table; caches the full per-tenant map in Redis
// for 30s. PUT/DELETE in the admin route must call invalidateTenantRouting().

import {
  query, getRedis, ValidationError, logger,
  tenantRoutingKey, invalidateTenantRouting,
} from '@lazirouter-cloud/shared';

const CACHE_TTL_SECONDS = 30;

async function loadTenantRouting(tenantId) {
  const r = getRedis();
  const cached = await r.get(tenantRoutingKey(tenantId));
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
  await r.set(tenantRoutingKey(tenantId), JSON.stringify(map), 'EX', CACHE_TTL_SECONDS);
  return map;
}

export { invalidateTenantRouting }; // re-export for backwards compatibility of import path

export async function resolveScenarioTarget(tenantId, scenario) {
  const map = await loadTenantRouting(tenantId);
  if (map[scenario]) return map[scenario];
  if (scenario !== 'default' && map.default) return map.default;
  throw new ValidationError(
    'Tenant has no default auto-routing target. Configure it in the dashboard.',
  );
}
