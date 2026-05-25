// Integration tests for scenarioRouter.
// Requires DATABASE_URL + REDIS_URL pointing at a working dev DB + Redis.
// Uses a scratch tenant id (777_777) far above any real one.

import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import {
  query, getRedis, closeDb, closeRedis,
  tenantRoutingKey, invalidateTenantRouting,
  ValidationError,
} from '@9router-cloud/shared';
import { resolveScenarioTarget } from './scenarioRouter.js';

const TID = 777_777;

async function setup() {
  await query(
    "INSERT INTO tenants (id, name, plan, status) VALUES ($1, 'scenarioRouter-test', 'free', 'active') ON CONFLICT (id) DO NOTHING",
    [TID],
  );
  await query('DELETE FROM tenant_routing WHERE tenant_id = $1', [TID]);
  await getRedis().del(tenantRoutingKey(TID));
}

async function cleanup() {
  await query('DELETE FROM tenant_routing WHERE tenant_id = $1', [TID]);
  await query('DELETE FROM tenants WHERE id = $1', [TID]);
  await getRedis().del(tenantRoutingKey(TID));
}

test('throws when default scenario is missing', async () => {
  await setup();
  await assert.rejects(
    () => resolveScenarioTarget(TID, 'default'),
    (err) => err instanceof ValidationError &&
      err.message.includes('no default auto-routing target'),
  );
});

test('returns target when scenario configured', async () => {
  await setup();
  await query(
    'INSERT INTO tenant_routing (tenant_id, scenario, target) VALUES ($1, $2, $3)',
    [TID, 'default', 'openai:gpt-x'],
  );
  await invalidateTenantRouting(TID);
  assert.equal(await resolveScenarioTarget(TID, 'default'), 'openai:gpt-x');
});

test('falls back to default when scenario missing but default configured', async () => {
  await setup();
  await query(
    'INSERT INTO tenant_routing (tenant_id, scenario, target) VALUES ($1, $2, $3)',
    [TID, 'default', 'openai:gpt-x'],
  );
  await invalidateTenantRouting(TID);
  assert.equal(await resolveScenarioTarget(TID, 'think'), 'openai:gpt-x');
});

test('returns scenario-specific target over default', async () => {
  await setup();
  await query(
    'INSERT INTO tenant_routing (tenant_id, scenario, target) VALUES ($1, $2, $3), ($1, $4, $5)',
    [TID, 'default', 'openai:gpt-x', 'think', 'openai:o1-mini'],
  );
  await invalidateTenantRouting(TID);
  assert.equal(await resolveScenarioTarget(TID, 'think'), 'openai:o1-mini');
});

test('recovers from corrupted cache by refetching from DB', async () => {
  await setup();
  await query(
    'INSERT INTO tenant_routing (tenant_id, scenario, target) VALUES ($1, $2, $3)',
    [TID, 'default', 'openai:gpt-x'],
  );
  await getRedis().set(tenantRoutingKey(TID), 'not valid json', 'EX', 60);
  assert.equal(await resolveScenarioTarget(TID, 'default'), 'openai:gpt-x');
});

test('invalidateTenantRouting clears cache so PUT shows immediately', async () => {
  await setup();
  await query(
    'INSERT INTO tenant_routing (tenant_id, scenario, target) VALUES ($1, $2, $3)',
    [TID, 'default', 'openai:gpt-x'],
  );
  // Prime the cache
  assert.equal(await resolveScenarioTarget(TID, 'default'), 'openai:gpt-x');
  // Mutate DB out-of-band
  await query(
    `UPDATE tenant_routing SET target = $1 WHERE tenant_id = $2 AND scenario = 'default'`,
    ['openai:gpt-y', TID],
  );
  // Without invalidation, the cache would still return gpt-x
  await invalidateTenantRouting(TID);
  assert.equal(await resolveScenarioTarget(TID, 'default'), 'openai:gpt-y');
});

after(async () => {
  await cleanup();
  await closeRedis();
  await closeDb();
});
