// /api/routing — per-tenant scenario→target CRUD.
// Targets are 'combo:<slug>' or '<provider>:<model>'.
//
// GET    /api/routing               → { items: [{scenario, target}], scenarios: [...] }
// PUT    /api/routing/:scenario     → upsert; body: { target: string }
// DELETE /api/routing/:scenario     → delete; default scenario cannot be deleted

import { query, NotFoundError, ValidationError, invalidateTenantRouting } from '@lazirouter-cloud/shared';
import { readJson, ok, noContent } from '../lib/http.js';
import { requireSession, requireRole } from '../middleware/sessionAuth.js';

const SCENARIOS = ['default', 'think', 'long_context', 'vision', 'tool_use', 'web'];
const TARGET_RE = /^([a-z0-9_-]+):([A-Za-z0-9._\-:/]+)$/;

export async function listRouting(req, res) {
  const s = requireSession(req);
  const { rows } = await query(
    `SELECT scenario, target, updated_at
     FROM tenant_routing
     WHERE tenant_id = $1
     ORDER BY scenario ASC`,
    [s.tenantId],
  );
  const byScenario = Object.create(null);
  for (const r of rows) byScenario[r.scenario] = { target: r.target, updatedAt: r.updated_at };
  const items = SCENARIOS.map(name => ({
    scenario: name,
    target: byScenario[name]?.target ?? null,
    updatedAt: byScenario[name]?.updatedAt ?? null,
  }));
  ok(res, { items, scenarios: SCENARIOS });
}

export async function putRouting(req, res, { params }) {
  const s = requireSession(req);
  requireRole(s, 'owner', 'admin');
  const scenario = params.scenario;
  if (!SCENARIOS.includes(scenario)) {
    throw new ValidationError(`Unknown scenario '${scenario}'. Must be one of: ${SCENARIOS.join(', ')}`);
  }
  const body = await readJson(req);
  const target = body?.target;
  if (typeof target !== 'string' || !target.trim()) {
    throw new ValidationError('Missing field: target (must be non-empty string)');
  }
  const trimmed = target.trim();
  await validateTarget(s.tenantId, trimmed);

  const r = await query(
    `INSERT INTO tenant_routing (tenant_id, scenario, target, updated_at)
     VALUES ($1, $2, $3, now())
     ON CONFLICT (tenant_id, scenario) DO UPDATE
       SET target = EXCLUDED.target, updated_at = now()
     RETURNING scenario, target, updated_at`,
    [s.tenantId, scenario, trimmed],
  );
  await invalidateTenantRouting(s.tenantId);
  ok(res, r.rows[0]);
}

export async function deleteRouting(req, res, { params }) {
  const s = requireSession(req);
  requireRole(s, 'owner', 'admin');
  const scenario = params.scenario;
  if (scenario === 'default') {
    throw new ValidationError('default scenario cannot be deleted; update it instead');
  }
  if (!SCENARIOS.includes(scenario)) {
    throw new ValidationError(`Unknown scenario '${scenario}'`);
  }
  const { rowCount } = await query(
    `DELETE FROM tenant_routing WHERE tenant_id = $1 AND scenario = $2`,
    [s.tenantId, scenario],
  );
  if (rowCount === 0) throw new NotFoundError(`Routing for scenario '${scenario}' not found`);
  await invalidateTenantRouting(s.tenantId);
  noContent(res);
}

async function validateTarget(tenantId, target) {
  const m = TARGET_RE.exec(target);
  if (!m) {
    throw new ValidationError(`target must be 'combo:<slug>' or '<provider>:<model>', got '${target}'`);
  }
  const head = m[1];
  const tail = m[2];
  if (head === 'combo') {
    const { rowCount } = await query(
      `SELECT 1 FROM combos WHERE tenant_id = $1 AND slug = $2 AND enabled = TRUE`,
      [tenantId, tail],
    );
    if (rowCount === 0) {
      throw new ValidationError(`combo '${tail}' does not exist or is disabled for this tenant`);
    }
  }
  // provider:model: we don't pre-validate the model name; resolveAttempts will fail
  // loudly at request time if no connection exists. This is the same behaviour as
  // today's /api/combos create flow.
}
