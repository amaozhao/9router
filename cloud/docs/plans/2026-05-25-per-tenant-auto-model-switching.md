# Per-Tenant Auto Model Switching — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable each tenant to configure a one-time scenario→model mapping; router auto-classifies inbound requests (`model=auto` or empty) into 6 scenarios and resolves to the tenant's target.

**Architecture:** Pure-function classifier reads request body (no I/O). Resolver reads per-tenant `tenant_routing` table via Redis-cached map (TTL 30s, invalidated on PUT/DELETE). `chatCompletions.js` and `messages.js` insert classify+resolve only when `model in ('', 'auto')`; explicit `combo:slug` or `provider:model` bypasses the classifier and reaches the existing `resolveAttempts()` unchanged. `usage_events` gains a `routed_model` column so audit can distinguish "client sent 'auto'" from "actually hit anthropic:claude-sonnet".

**Tech Stack:** Node.js ESM, Postgres via `pg` through `@lazirouter-cloud/shared` (`query`, `tx`), Redis via ioredis through `getRedis()`, `node --test` for unit tests, bash assertion scripts in `cloud/scripts/` for end-to-end verification.

**Spec:** `cloud/docs/specs/2026-05-25-per-tenant-auto-model-switching-design.md`

---

## Task 1: Database migration

**Files:**
- Create: `cloud/migrations/0002_tenant_routing.sql`

- [ ] **Step 1: Write the migration file**

Create `cloud/migrations/0002_tenant_routing.sql` with this exact content:

```sql
-- tenant_routing: per-tenant scenario→target table for auto model switching.
-- target is a free-form string consumed by router/services/combo.js#resolveAttempts:
--   - 'combo:<slug>' resolves to the tenant's combo fallback chain
--   - '<provider>:<model>' resolves to a single attempt
-- scenario is restricted to the 6 hardcoded scenarios computed by
-- router/services/scenarioClassifier.js.

CREATE TABLE tenant_routing (
  tenant_id   BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  scenario    TEXT NOT NULL
    CHECK (scenario IN ('default','think','long_context','vision','tool_use','web')),
  target      TEXT NOT NULL,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, scenario)
);

CREATE INDEX idx_tenant_routing_tenant ON tenant_routing(tenant_id);

-- routed_model: the actually-used model string after auto-classification.
-- NULL when the client sent an explicit non-auto model (i.e. classifier was bypassed).
ALTER TABLE usage_events ADD COLUMN routed_model TEXT NULL;
```

- [ ] **Step 2: Apply the migration**

Run from repo root:
```bash
export DATABASE_URL='postgres://router:router_dev_pw@localhost:55432/router'
( cd cloud/scripts && node migrate.mjs )
```
Expected stdout contains: `[apply] 0002_tenant_routing`.

If the line is missing or shows `[skip]` on the first run, abort — the file was placed in the wrong directory.

- [ ] **Step 3: Verify schema in psql**

Run:
```bash
PGPASSWORD=router_dev_pw psql -h localhost -p 55432 -U router -d router -c '\d tenant_routing' -c '\d usage_events' | grep -E 'tenant_routing|scenario|routed_model'
```
Expected output contains all of: `tenant_routing`, `scenario`, `routed_model`.

- [ ] **Step 4: Commit**

```bash
git add cloud/migrations/0002_tenant_routing.sql
git commit -m "feat(cloud): tenant_routing table + usage_events.routed_model"
```

---

## Task 2: Scenario classifier (pure function, TDD)

**Files:**
- Create: `cloud/router/src/services/scenarioClassifier.js`
- Test: `cloud/router/src/services/scenarioClassifier.test.js`

- [ ] **Step 1: Write the failing test**

Create `cloud/router/src/services/scenarioClassifier.test.js`:

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { classifyScenario, SCENARIOS } from './scenarioClassifier.js';

test('web: tool with name matching /search|web|browse/i wins', () => {
  const body = { tools: [{ type: 'function', function: { name: 'web_search' } }], messages: [] };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.WEB);
});

test('web: anthropic-shape tool with name browse_url wins', () => {
  const body = { tools: [{ name: 'browse_url' }], messages: [] };
  assert.equal(classifyScenario(body, 'anthropic'), SCENARIOS.WEB);
});

test('tool_use: non-empty tools without web names', () => {
  const body = { tools: [{ type: 'function', function: { name: 'get_weather' } }], messages: [] };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.TOOL_USE);
});

test('vision: openai image_url part', () => {
  const body = {
    messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: 'http://x' } }] }],
  };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.VISION);
});

test('vision: anthropic image part with image/png media_type', () => {
  const body = {
    messages: [{ role: 'user', content: [
      { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'xxx' } },
    ] }],
  };
  assert.equal(classifyScenario(body, 'anthropic'), SCENARIOS.VISION);
});

test('long_context: char count >256000 (≈64k tokens)', () => {
  const big = 'a'.repeat(300_000);
  const body = { messages: [{ role: 'user', content: big }] };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.LONG_CONTEXT);
});

test('long_context: anthropic system prompt counted too', () => {
  const body = { system: 'a'.repeat(300_000), messages: [{ role: 'user', content: 'hi' }] };
  assert.equal(classifyScenario(body, 'anthropic'), SCENARIOS.LONG_CONTEXT);
});

test('think: reasoning_effort=high', () => {
  const body = { reasoning_effort: 'high', messages: [{ role: 'user', content: 'solve' }] };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.THINK);
});

test('think: thinking.enabled=true', () => {
  const body = { thinking: { enabled: true }, messages: [{ role: 'user', content: 'solve' }] };
  assert.equal(classifyScenario(body, 'anthropic'), SCENARIOS.THINK);
});

test('default: plain short text', () => {
  const body = { messages: [{ role: 'user', content: 'hi' }] };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.DEFAULT);
});

test('web beats tool_use when both present', () => {
  const body = {
    tools: [
      { type: 'function', function: { name: 'get_weather' } },
      { type: 'function', function: { name: 'web_search' } },
    ],
    messages: [],
  };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.WEB);
});

test('tool_use beats vision when both present', () => {
  const body = {
    tools: [{ type: 'function', function: { name: 'get_weather' } }],
    messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: 'x' } }] }],
  };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.TOOL_USE);
});
```

- [ ] **Step 2: Run test to verify it fails**

```bash
( cd cloud/router && node --test src/services/scenarioClassifier.test.js )
```
Expected: every test fails with `Cannot find module ... scenarioClassifier.js`.

- [ ] **Step 3: Implement the classifier**

Create `cloud/router/src/services/scenarioClassifier.js`:

```js
// Pure-function request classifier. No I/O. Identical contract for both
// OpenAI-shape (chat/completions) and Anthropic-shape (messages) bodies.
//
// Priority order (first match wins):
//   1. web        - any tool name matches /search|web|browse/i
//   2. tool_use   - any tools entry at all
//   3. vision     - any message content part is an image
//   4. long_context - rough estimate prompt_tokens > 64000 (chars/4)
//   5. think      - reasoning_effort high/max OR thinking.enabled
//   6. default    - fallback

export const SCENARIOS = Object.freeze({
  DEFAULT: 'default',
  THINK: 'think',
  LONG_CONTEXT: 'long_context',
  VISION: 'vision',
  TOOL_USE: 'tool_use',
  WEB: 'web',
});

const LONG_CONTEXT_CHAR_THRESHOLD = 64_000 * 4; // ~64k tokens via chars/4 estimate
const WEB_TOOL_NAME_RE = /search|web|browse/i;

export function classifyScenario(body, protocol) {
  if (hasWebTool(body)) return SCENARIOS.WEB;
  if (hasTools(body)) return SCENARIOS.TOOL_USE;
  if (hasImage(body)) return SCENARIOS.VISION;
  if (estimateChars(body) > LONG_CONTEXT_CHAR_THRESHOLD) return SCENARIOS.LONG_CONTEXT;
  if (isThink(body)) return SCENARIOS.THINK;
  return SCENARIOS.DEFAULT;
}

function hasTools(body) {
  return Array.isArray(body?.tools) && body.tools.length > 0;
}

function hasWebTool(body) {
  if (!Array.isArray(body?.tools)) return false;
  return body.tools.some(t => {
    if (!t) return false;
    const name = t?.function?.name ?? t?.name;
    return typeof name === 'string' && WEB_TOOL_NAME_RE.test(name);
  });
}

function hasImage(body) {
  const messages = body?.messages;
  if (!Array.isArray(messages)) return false;
  for (const msg of messages) {
    const content = msg?.content;
    if (!Array.isArray(content)) continue;
    for (const part of content) {
      if (!part) continue;
      if (part.type === 'image' || part.type === 'image_url') return true;
      const mt = part?.source?.media_type;
      if (typeof mt === 'string' && mt.startsWith('image/')) return true;
    }
  }
  return false;
}

function estimateChars(body) {
  let total = 0;
  if (typeof body?.system === 'string') total += body.system.length;
  const messages = body?.messages;
  if (Array.isArray(messages)) {
    for (const m of messages) {
      const c = m?.content;
      if (typeof c === 'string') total += c.length;
      else total += JSON.stringify(c ?? '').length;
    }
  }
  return total;
}

function isThink(body) {
  if (body?.reasoning_effort === 'high' || body?.reasoning_effort === 'max') return true;
  if (body?.thinking && body.thinking.enabled === true) return true;
  return false;
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
( cd cloud/router && node --test src/services/scenarioClassifier.test.js )
```
Expected: `# pass 12` (all 12 cases green, 0 fail).

- [ ] **Step 5: Commit**

```bash
git add cloud/router/src/services/scenarioClassifier.js cloud/router/src/services/scenarioClassifier.test.js
git commit -m "feat(cloud/router): scenarioClassifier pure-function with 12 unit tests"
```

---

## Task 3: Scenario resolver (DB + Redis cache)

**Files:**
- Create: `cloud/router/src/services/scenarioRouter.js`

(No unit test — DB-touching code is covered by the verify script in Task 8, matching the project's existing convention of end-to-end verify scripts over DB mocks.)

- [ ] **Step 1: Implement the resolver**

Create `cloud/router/src/services/scenarioRouter.js`:

```js
// Per-tenant scenario→target resolver.
// Reads from tenant_routing table; caches the full per-tenant map in Redis
// for 30s. PUT/DELETE in the admin route must call invalidateTenantRouting().

import { query, getRedis, ValidationError, logger } from '@lazirouter-cloud/shared';

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
```

- [ ] **Step 2: Sanity-check the import surface**

```bash
( cd cloud/router && node -e "import('./src/services/scenarioRouter.js').then(m => console.log(Object.keys(m).sort().join(',')))" )
```
Expected: `invalidateTenantRouting,resolveScenarioTarget`.

If output differs, the file has a syntax error — open it and read the stack.

- [ ] **Step 3: Commit**

```bash
git add cloud/router/src/services/scenarioRouter.js
git commit -m "feat(cloud/router): scenarioRouter — per-tenant scenario→target with 30s Redis cache"
```

---

## Task 4: Wire classifier + resolver into `/v1/chat/completions`

**Files:**
- Modify: `cloud/router/src/routes/chatCompletions.js`

- [ ] **Step 1: Add imports**

Open `cloud/router/src/routes/chatCompletions.js`. At the top, find the existing import block (line 10-20). After the `import { resolveAttempts } from '../services/combo.js';` line, add:

```js
import { classifyScenario } from '../services/scenarioClassifier.js';
import { resolveScenarioTarget } from '../services/scenarioRouter.js';
```

- [ ] **Step 2: Replace the model-required guard and resolve effective model**

Find this block (≈ lines 37-43):
```js
  // 2) Body
  const body = await readJson(req);
  if (!body || !body.model) throw new ValidationError('Missing field: model');
  if (!Array.isArray(body.messages)) throw new ValidationError('Missing field: messages[]');

  // 3) Attempts
  const attempts = await resolveAttempts(ctx.tenantId, body.model);
```

Replace it with:
```js
  // 2) Body
  const body = await readJson(req);
  if (!body) throw new ValidationError('Missing body');
  if (!Array.isArray(body.messages)) throw new ValidationError('Missing field: messages[]');

  // 3) Auto-routing: model="auto" or empty triggers content-based scenario routing.
  //    Any other string (combo:slug, provider:model, bare model) bypasses the
  //    classifier and reaches resolveAttempts as today.
  const rawModel = (body.model ?? '').toString();
  let effectiveModel = rawModel;
  let routedScenario = null;
  if (rawModel === '' || rawModel === 'auto') {
    routedScenario = classifyScenario(body, 'openai');
    effectiveModel = await resolveScenarioTarget(ctx.tenantId, routedScenario);
    logger.info({ tenantId: ctx.tenantId, scenario: routedScenario, effectiveModel }, 'auto-routing');
  }

  // 4) Attempts
  const attempts = await resolveAttempts(ctx.tenantId, effectiveModel);
```

- [ ] **Step 3: Pass `routedModel` into usage records**

Find the `const usageBase = {` block (≈ line 72-80). Add the `routedModel` field:

```js
    const usageBase = {
      tenantId: ctx.tenantId,
      apiKeyId: ctx.apiKeyId,
      connectionId: account.connectionId,
      provider: attempt.provider,
      model: rawModel || 'auto',
      routedModel: routedScenario ? effectiveModel : null,
      upstreamModel: attempt.upstreamModel,
      requestId,
    };
```

(The `model` is what the client sent — `'auto'` literal if they omitted it. `routedModel` is non-null only when classification ran.)

- [ ] **Step 4: Smoke test by booting router**

In two terminals:
```bash
# terminal 1 — start router
export DATABASE_URL='postgres://router:router_dev_pw@localhost:55432/router'
export REDIS_URL='redis://localhost:56379/0'
export CLOUD_MASTER_KEY="$(openssl rand -base64 32)"
( cd cloud/router && node src/server.js )

# terminal 2 — assert no syntax error
sleep 1 && curl -s -o /dev/null -w '%{http_code}\n' http://localhost:30100/health
```
Expected: `200`. If router crashes on import, fix the syntax before continuing.

Then kill the router (Ctrl-C).

- [ ] **Step 5: Commit**

```bash
git add cloud/router/src/routes/chatCompletions.js
git commit -m "feat(cloud/router): auto-routing in /v1/chat/completions when model=auto/empty"
```

---

## Task 5: Wire classifier + resolver into `/v1/messages` + usage `routed_model` column

**Files:**
- Modify: `cloud/router/src/routes/messages.js`
- Modify: `cloud/router/src/services/usage.js`

- [ ] **Step 1: Add imports to messages.js**

Open `cloud/router/src/routes/messages.js`. After the line `import { resolveAttempts } from '../services/combo.js';`, add:

```js
import { classifyScenario } from '../services/scenarioClassifier.js';
import { resolveScenarioTarget } from '../services/scenarioRouter.js';
```

Also add `logger` to the shared import on line 9-12 if not already present (it is needed for the log line below):
```js
import {
  logger, ValidationError, UpstreamError, AuthError,
  NoAccountAvailableError, AppError,
} from '@lazirouter-cloud/shared';
```

- [ ] **Step 2: Insert classifier in messages.js**

Find (≈ lines 39-45):
```js
  const anthropicBody = await readJson(req);
  if (!anthropicBody.model) throw new ValidationError('Missing field: model');
  if (!Array.isArray(anthropicBody.messages)) throw new ValidationError('Missing field: messages[]');

  // The model string controls combo resolution exactly like /v1/chat/completions
  const attempts = await resolveAttempts(ctx.tenantId, anthropicBody.model);
```

Replace with:
```js
  const anthropicBody = await readJson(req);
  if (!Array.isArray(anthropicBody.messages)) throw new ValidationError('Missing field: messages[]');

  // Auto-routing: 'auto' or empty model triggers content-based scenario routing.
  const rawModel = (anthropicBody.model ?? '').toString();
  let effectiveModel = rawModel;
  let routedScenario = null;
  if (rawModel === '' || rawModel === 'auto') {
    routedScenario = classifyScenario(anthropicBody, 'anthropic');
    effectiveModel = await resolveScenarioTarget(ctx.tenantId, routedScenario);
    logger.info({ tenantId: ctx.tenantId, scenario: routedScenario, effectiveModel }, 'auto-routing');
  }

  const attempts = await resolveAttempts(ctx.tenantId, effectiveModel);
```

- [ ] **Step 3: Add `routedModel` to messages.js `usageBase`**

Find the `usageBase` block (≈ lines 72-80) and update:
```js
    const usageBase = {
      tenantId: ctx.tenantId,
      apiKeyId: ctx.apiKeyId,
      connectionId: account.connectionId,
      provider: attempt.provider,
      model: rawModel || 'auto',
      routedModel: routedScenario ? effectiveModel : null,
      upstreamModel: attempt.upstreamModel,
      requestId,
    };
```

- [ ] **Step 4: Wire `routedModel` into `recordUsage`**

Open `cloud/router/src/services/usage.js`. Update the INSERT to write the new column. Replace the `recordUsage` function body:

```js
export async function recordUsage(event) {
  try {
    await query(`
      INSERT INTO usage_events (
        tenant_id, api_key_id, connection_id, provider, model, routed_model, upstream_model,
        prompt_tokens, completion_tokens, total_tokens, cost_micros,
        status, latency_ms, request_id, error_code, meta
      ) VALUES (
        $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16
      )
    `, [
      event.tenantId,
      event.apiKeyId ?? null,
      event.connectionId ?? null,
      event.provider,
      event.model,
      event.routedModel ?? null,
      event.upstreamModel ?? null,
      event.promptTokens ?? 0,
      event.completionTokens ?? 0,
      (event.promptTokens ?? 0) + (event.completionTokens ?? 0),
      event.costMicros ?? 0,
      event.status,
      event.latencyMs ?? null,
      event.requestId ?? null,
      event.errorCode ?? null,
      event.meta ? JSON.stringify(event.meta) : null,
    ]);
  } catch (err) {
    logger.error({ err: err.message, event }, 'failed to record usage');
  }
}
```

- [ ] **Step 5: Smoke check both routers still boot**

```bash
export DATABASE_URL='postgres://router:router_dev_pw@localhost:55432/router'
export REDIS_URL='redis://localhost:56379/0'
export CLOUD_MASTER_KEY="$(openssl rand -base64 32)"
( cd cloud/router && timeout 3 node src/server.js & sleep 1 && curl -s -o /dev/null -w 'health=%{http_code}\n' http://localhost:30100/health; pkill -f 'cloud/router/src/server.js' || true )
```
Expected: `health=200`.

- [ ] **Step 6: Commit**

```bash
git add cloud/router/src/routes/messages.js cloud/router/src/services/usage.js
git commit -m "feat(cloud/router): auto-routing in /v1/messages + persist routed_model"
```

---

## Task 6: Admin REST `/api/routing` CRUD

**Files:**
- Create: `cloud/admin/src/routes/routing.js`
- Modify: `cloud/admin/src/server.js`

- [ ] **Step 1: Create the routing route handlers**

Create `cloud/admin/src/routes/routing.js`:

```js
// /api/routing — per-tenant scenario→target CRUD.
// Targets are 'combo:<slug>' or '<provider>:<model>'.
//
// GET    /api/routing               → { items: [{scenario, target}], scenarios: [...] }
// PUT    /api/routing/:scenario     → upsert; body: { target: string }
// DELETE /api/routing/:scenario     → delete; default scenario cannot be deleted

import { query, NotFoundError, ValidationError, getRedis } from '@lazirouter-cloud/shared';
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
  await invalidateCache(s.tenantId);
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
  await invalidateCache(s.tenantId);
  noContent(res);
}

async function invalidateCache(tenantId) {
  await getRedis().del(`routing:${tenantId}`);
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
```

- [ ] **Step 2: Register the routes in admin server**

Open `cloud/admin/src/server.js`. Find the existing combo route lines (around the lines registering `'/api/combos/:slug'`). After them, add:

```js
import { listRouting, putRouting, deleteRouting } from './routes/routing.js';
```

Add to the `routes` array (immediately after the combos block, before the usage block):

```js
  { method: 'GET',    pattern: '/api/routing',            handler: listRouting },
  { method: 'PUT',    pattern: '/api/routing/:scenario',  handler: putRouting },
  { method: 'DELETE', pattern: '/api/routing/:scenario',  handler: deleteRouting },
```

- [ ] **Step 3: Boot admin and exercise the endpoints**

Boot admin in background, then walk through GET → PUT → GET → DELETE → GET. Run this whole block:

```bash
export DATABASE_URL='postgres://router:router_dev_pw@localhost:55432/router'
export REDIS_URL='redis://localhost:56379/0'
export CLOUD_MASTER_KEY="$(openssl rand -base64 32)"
export JWT_SECRET='dev-secret'
( cd cloud/admin && node src/server.js & ); sleep 1

# 1. Sign up a fresh tenant
EMAIL="t-$(date +%s)@x.io"
TOKEN=$(curl -s -X POST http://localhost:30200/auth/signup \
  -H 'content-type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"abcd1234\"}" | node -e "process.stdin.on('data',d=>{console.log(JSON.parse(d).token)})")
echo "TOKEN=$TOKEN"

# 2. Initial routing list — all targets null
curl -s -H "authorization: Bearer $TOKEN" http://localhost:30200/api/routing | head -c 400; echo

# 3. PUT default
curl -s -X PUT -H "authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"target":"openai:gpt-4o-mini"}' \
  http://localhost:30200/api/routing/default | head -c 200; echo

# 4. PUT think
curl -s -X PUT -H "authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"target":"openai:o1-mini"}' \
  http://localhost:30200/api/routing/think | head -c 200; echo

# 5. GET — default and think should now be set
curl -s -H "authorization: Bearer $TOKEN" http://localhost:30200/api/routing | head -c 400; echo

# 6. DELETE think
curl -s -o /dev/null -w 'delete=%{http_code}\n' -X DELETE \
  -H "authorization: Bearer $TOKEN" http://localhost:30200/api/routing/think

# 7. DELETE default — must 400
curl -s -o /dev/null -w 'delete_default=%{http_code}\n' -X DELETE \
  -H "authorization: Bearer $TOKEN" http://localhost:30200/api/routing/default

# 8. PUT invalid target — must 400
curl -s -o /dev/null -w 'invalid_target=%{http_code}\n' -X PUT \
  -H "authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"target":"garbage"}' \
  http://localhost:30200/api/routing/default

# 9. PUT unknown scenario — must 400
curl -s -o /dev/null -w 'unknown_scenario=%{http_code}\n' -X PUT \
  -H "authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"target":"openai:gpt-4o-mini"}' \
  http://localhost:30200/api/routing/foo

pkill -f 'cloud/admin/src/server.js' || true
```

Expected:
- step 5 list shows `default` = `openai:gpt-4o-mini` and `think` = `openai:o1-mini`
- `delete=204`
- `delete_default=400`
- `invalid_target=400`
- `unknown_scenario=400`

- [ ] **Step 4: Commit**

```bash
git add cloud/admin/src/routes/routing.js cloud/admin/src/server.js
git commit -m "feat(cloud/admin): /api/routing CRUD for per-tenant scenario mapping"
```

---

## Task 7: Admin UI section — Auto Routing

**Files:**
- Modify: `cloud/admin-ui/app.js`

- [ ] **Step 1: Find an appropriate insertion point**

Open `cloud/admin-ui/app.js`. Search for the section that renders the **Combos** panel. The Auto Routing section will be inserted directly after the Combos block (parallel placement — they are sibling concerns).

- [ ] **Step 2: Add an AutoRouting component**

Add this component definition near the other panel components (e.g. just above where `Combos` is defined):

```js
const SCENARIO_META = [
  { key: 'default',      label: 'Default',      desc: 'Fallback when no other scenario fires' },
  { key: 'think',        label: 'Think',        desc: 'reasoning_effort=high or thinking.enabled' },
  { key: 'long_context', label: 'Long Context', desc: '~64k+ tokens (chars/4 estimate)' },
  { key: 'vision',       label: 'Vision',       desc: 'Any image part in messages' },
  { key: 'tool_use',     label: 'Tool Use',     desc: 'Any tools array entry' },
  { key: 'web',          label: 'Web',          desc: 'Tool name matches /search|web|browse/i' },
];

function AutoRouting({ token, combos }) {
  const [items, setItems] = useState([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);

  async function load() {
    setBusy(true); setErr(null);
    try {
      const r = await fetch('/api/routing', { headers: { authorization: 'Bearer ' + token } });
      const d = await r.json();
      if (!r.ok) throw new Error(d.error || 'load failed');
      setItems(d.items || []);
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  }

  async function save(scenario, target) {
    setBusy(true); setErr(null);
    try {
      const r = await fetch('/api/routing/' + encodeURIComponent(scenario), {
        method: 'PUT',
        headers: { authorization: 'Bearer ' + token, 'content-type': 'application/json' },
        body: JSON.stringify({ target }),
      });
      const d = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(d.error || 'save failed');
      await load();
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  }

  async function remove(scenario) {
    if (scenario === 'default') return; // UI guard
    setBusy(true); setErr(null);
    try {
      const r = await fetch('/api/routing/' + encodeURIComponent(scenario), {
        method: 'DELETE',
        headers: { authorization: 'Bearer ' + token },
      });
      if (!r.ok && r.status !== 204) {
        const d = await r.json().catch(() => ({}));
        throw new Error(d.error || 'delete failed');
      }
      await load();
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  }

  useEffect(() => { load(); }, []);

  return html`
    <section class="panel">
      <h2>Auto Routing</h2>
      <p class="muted">When a client sends model="auto" or omits it, the router classifies the request and uses the target you set here. Explicit models bypass this.</p>
      ${err && html`<div class="err">${err}</div>`}
      <table>
        <thead><tr><th>Scenario</th><th>Description</th><th>Target</th><th></th></tr></thead>
        <tbody>
          ${SCENARIO_META.map(meta => {
            const cur = items.find(i => i.scenario === meta.key) || { target: null };
            return html`<${RoutingRow}
              key=${meta.key}
              meta=${meta}
              current=${cur}
              combos=${combos}
              onSave=${(t) => save(meta.key, t)}
              onDelete=${meta.key === 'default' ? null : () => remove(meta.key)}
              busy=${busy} />`;
          })}
        </tbody>
      </table>
    </section>
  `;
}

function RoutingRow({ meta, current, combos, onSave, onDelete, busy }) {
  const [draft, setDraft] = useState(current.target || '');
  useEffect(() => { setDraft(current.target || ''); }, [current.target]);
  const dirty = (draft.trim() || null) !== current.target;
  return html`
    <tr>
      <td><strong>${meta.label}</strong></td>
      <td class="muted">${meta.desc}</td>
      <td>
        <input type="text" placeholder="combo:slug or provider:model"
               list="combo-slugs-${meta.key}"
               value=${draft} onInput=${(e) => setDraft(e.target.value)} />
        <datalist id="combo-slugs-${meta.key}">
          ${(combos || []).map(c => html`<option value=${'combo:' + c.slug} />`)}
        </datalist>
      </td>
      <td>
        <button disabled=${busy || !dirty || !draft.trim()} onClick=${() => onSave(draft.trim())}>Save</button>
        ${onDelete && current.target && html`
          <button disabled=${busy} onClick=${onDelete}>Clear</button>
        `}
      </td>
    </tr>
  `;
}
```

- [ ] **Step 3: Mount it in the dashboard layout**

Find the existing dashboard render block (the one that returns JSX/htm containing `<Combos>`). Add `<AutoRouting>` directly after `<Combos>` — pass `token` and the loaded `combos` list it already has in state:

```js
        ${html`<${Combos} token=${token} combos=${combos} setCombos=${setCombos} />`}
        ${html`<${AutoRouting} token=${token} combos=${combos} />`}
```

(If the dashboard component doesn't already lift `combos` to a parent, leave that refactor for a separate task and simply have `AutoRouting` independently fetch `/api/combos` — but the project's existing pattern already shares it.)

- [ ] **Step 4: Visual sanity check in browser**

```bash
export DATABASE_URL='postgres://router:router_dev_pw@localhost:55432/router'
export REDIS_URL='redis://localhost:56379/0'
export CLOUD_MASTER_KEY="$(openssl rand -base64 32)"
export JWT_SECRET='dev-secret'
( cd cloud/admin    && node src/server.js & )
( cd cloud/admin-ui && node server.mjs    & )
sleep 1
open http://localhost:30300
```
Manually: log in (or sign up), see "Auto Routing" section under Combos, type `openai:gpt-4o-mini` into the Default row, hit Save. Reload — value persists.

Kill both servers when done.

- [ ] **Step 5: Commit**

```bash
git add cloud/admin-ui/app.js
git commit -m "feat(cloud/admin-ui): Auto Routing section with 6 scenario rows"
```

---

## Task 8: End-to-end verify script

**Files:**
- Create: `cloud/scripts/verify-auto-routing.sh`

This script proves the full feature works against running services. It mirrors the structure of the existing `cloud/scripts/full-verify.sh`.

- [ ] **Step 1: Write the verify script**

Create `cloud/scripts/verify-auto-routing.sh` (chmod +x after):

```bash
#!/usr/bin/env bash
# End-to-end verification for per-tenant auto model switching.
# Boots fake-upstream + admin + router with a mocked OpenAI-compatible target,
# signs up two isolated tenants, configures different routing per tenant, and
# asserts each scenario lands on the correct upstream model.
#
# Expects Postgres + Redis already running via docker-compose.

set -u
cd "$(dirname "$0")/.."

export DATABASE_URL='postgres://router:router_dev_pw@localhost:55432/router'
export REDIS_URL='redis://localhost:56379/0'
export CLOUD_MASTER_KEY="${CLOUD_MASTER_KEY:-+rB3s6hXL3gKZmhnywQQeMNUwiWSLkIo7wJRWa3BrY4=}"
export JWT_SECRET='verify-auto-routing'

PASS=0; FAIL=0; RESULTS=()

assert_eq() {
  local label="$1" expected="$2" got="$3"
  if [[ "$got" == "$expected" ]]; then
    PASS=$((PASS+1)); RESULTS+=("  ✓ $label")
  else
    FAIL=$((FAIL+1)); RESULTS+=("  ✗ $label  expected=$expected  got=$got")
  fi
}

cleanup() {
  pkill -f 'cloud/router/src/server.js'  || true
  pkill -f 'cloud/admin/src/server.js'   || true
  pkill -f 'cloud/scripts/fake-upstream' || true
}
trap cleanup EXIT

# Reset state. Drops the two scratch tenants if they exist.
PGPASSWORD=router_dev_pw psql -h localhost -p 55432 -U router -d router -q <<'SQL'
DELETE FROM tenants WHERE name LIKE 'verify-routing-%';
SQL

# Boot supporting processes
( cd scripts && node fake-upstream.mjs --port 40991 ) &
( cd admin && node src/server.js ) &
( cd router && node src/server.js ) &
sleep 1.5

# Sign up tenant A
EMAIL_A="verify-routing-a-$(date +%s)@x.io"
RESP_A=$(curl -sS -X POST http://localhost:30200/auth/signup \
  -H 'content-type: application/json' \
  -d "{\"email\":\"$EMAIL_A\",\"password\":\"abcd1234\",\"tenantName\":\"verify-routing-a\"}")
TOKEN_A=$(node -e "process.stdin.on('data',d=>{const j=JSON.parse(d);console.log(j.token||'')})" <<<"$RESP_A")
[[ -z "$TOKEN_A" ]] && { echo "signup A failed: $RESP_A"; exit 1; }

# Sign up tenant B
EMAIL_B="verify-routing-b-$(date +%s)@x.io"
RESP_B=$(curl -sS -X POST http://localhost:30200/auth/signup \
  -H 'content-type: application/json' \
  -d "{\"email\":\"$EMAIL_B\",\"password\":\"abcd1234\",\"tenantName\":\"verify-routing-b\"}")
TOKEN_B=$(node -e "process.stdin.on('data',d=>{const j=JSON.parse(d);console.log(j.token||'')})" <<<"$RESP_B")
[[ -z "$TOKEN_B" ]] && { echo "signup B failed: $RESP_B"; exit 1; }

# For each tenant: create a connection pointing at fake-upstream
make_connection() {
  local token="$1"
  curl -sS -X POST http://localhost:30200/api/connections \
    -H "authorization: Bearer $token" -H 'content-type: application/json' \
    -d '{"provider":"openai","name":"fake","authType":"api_key",
         "credentials":{"api_key":"sk-fake","base_url":"http://localhost:40991"}}'
}
make_connection "$TOKEN_A" >/dev/null
make_connection "$TOKEN_B" >/dev/null

# For each tenant: create a client API key
make_apikey() {
  local token="$1"
  curl -sS -X POST http://localhost:30200/api/keys \
    -H "authorization: Bearer $token" -H 'content-type: application/json' \
    -d '{"name":"verify"}' | node -e "process.stdin.on('data',d=>{const j=JSON.parse(d);console.log(j.plaintextKey||j.key||'')})"
}
SK_A=$(make_apikey "$TOKEN_A")
SK_B=$(make_apikey "$TOKEN_B")

# Tenant A routing: default=fake-default, think=fake-think, vision=fake-vision, web=fake-web, tool_use=fake-tool, long_context=fake-long
set_routing() {
  local token="$1" scenario="$2" target="$3"
  curl -sS -X PUT "http://localhost:30200/api/routing/$scenario" \
    -H "authorization: Bearer $token" -H 'content-type: application/json' \
    -d "{\"target\":\"$target\"}" >/dev/null
}
for pair in default:openai:fake-default think:openai:fake-think vision:openai:fake-vision \
            web:openai:fake-web tool_use:openai:fake-tool long_context:openai:fake-long; do
  scenario=${pair%%:*}; rest=${pair#*:}
  set_routing "$TOKEN_A" "$scenario" "$rest"
done

# Tenant B routing: every scenario falls back to a tenant-B-specific default
set_routing "$TOKEN_B" default "openai:tenant-b-default"

# Helper: hit /v1/chat/completions with a body, capture which upstream model was selected.
# fake-upstream echoes the requested model into x-fake-received-model header.
hit() {
  local sk="$1" body="$2"
  curl -sS -X POST http://localhost:30100/v1/chat/completions \
    -H "authorization: Bearer $sk" -H 'content-type: application/json' \
    -D - -o /dev/null -d "$body" | tr -d '\r' | awk '/^x-fake-received-model:/{print $2}'
}

# Tenant A scenarios
M=$(hit "$SK_A" '{"model":"auto","messages":[{"role":"user","content":"hi"}]}')
assert_eq "tenantA / default → fake-default" "fake-default" "$M"

M=$(hit "$SK_A" '{"model":"auto","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}')
assert_eq "tenantA / think → fake-think" "fake-think" "$M"

M=$(hit "$SK_A" "$(node -e 'const big="a".repeat(300000);process.stdout.write(JSON.stringify({model:"auto",messages:[{role:"user",content:big}]}))')")
assert_eq "tenantA / long_context → fake-long" "fake-long" "$M"

M=$(hit "$SK_A" '{"model":"auto","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}')
assert_eq "tenantA / vision → fake-vision" "fake-vision" "$M"

M=$(hit "$SK_A" '{"model":"auto","tools":[{"type":"function","function":{"name":"get_weather"}}],"messages":[{"role":"user","content":"hi"}]}')
assert_eq "tenantA / tool_use → fake-tool" "fake-tool" "$M"

M=$(hit "$SK_A" '{"model":"auto","tools":[{"type":"function","function":{"name":"web_search"}}],"messages":[{"role":"user","content":"hi"}]}')
assert_eq "tenantA / web → fake-web" "fake-web" "$M"

# Tenant B: same scenarios but every one falls back to tenant-B-default because B only configured default
M=$(hit "$SK_B" '{"model":"auto","messages":[{"role":"user","content":"hi"}]}')
assert_eq "tenantB / default → tenant-b-default" "tenant-b-default" "$M"

M=$(hit "$SK_B" '{"model":"auto","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}')
assert_eq "tenantB / think falls back to default → tenant-b-default" "tenant-b-default" "$M"

# Explicit model bypasses classifier
M=$(hit "$SK_A" '{"model":"openai:explicit-model","messages":[{"role":"user","content":"hi"}]}')
assert_eq "tenantA / explicit model bypasses classifier" "explicit-model" "$M"

# routed_model column populated for auto, NULL for explicit
ROUTED_AUTO=$(PGPASSWORD=router_dev_pw psql -h localhost -p 55432 -U router -d router -tAc \
  "SELECT routed_model FROM usage_events WHERE model='auto' ORDER BY id DESC LIMIT 1")
assert_eq "usage_events.routed_model set when model=auto" "openai:fake-default" "$ROUTED_AUTO"

ROUTED_EXPLICIT=$(PGPASSWORD=router_dev_pw psql -h localhost -p 55432 -U router -d router -tAc \
  "SELECT COALESCE(routed_model,'NULL') FROM usage_events WHERE model='openai:explicit-model' ORDER BY id DESC LIMIT 1")
assert_eq "usage_events.routed_model NULL when explicit" "NULL" "$ROUTED_EXPLICIT"

# Cross-tenant isolation: tenant A's PUT not visible to tenant B
LIST_B=$(curl -sS -H "authorization: Bearer $TOKEN_B" http://localhost:30200/api/routing)
node -e "
const d = JSON.parse(process.argv[1]);
const long = d.items.find(i=>i.scenario==='long_context');
process.exit(long.target===null ? 0 : 1);
" "$LIST_B" && assert_eq "tenantB.long_context not affected by tenantA PUT" "ok" "ok" \
                || assert_eq "tenantB.long_context not affected by tenantA PUT" "ok" "leaked"

printf '\n'
printf '%s\n' "${RESULTS[@]}"
printf '\nPass: %d   Fail: %d\n' "$PASS" "$FAIL"
exit $([[ $FAIL -eq 0 ]] && echo 0 || echo 1)
```

- [ ] **Step 2: Verify the fake-upstream echoes the model header**

The script depends on `fake-upstream.mjs` setting an `x-fake-received-model` response header. Open `cloud/scripts/fake-upstream.mjs` and confirm — or, if it's not present, add this header to the response (in the request handler, before writing the response body):

```js
res.setHeader('x-fake-received-model', body?.model ?? '');
```

If the file already echoes the model in the JSON body but not in a header, adding the header is a one-line patch that doesn't change existing behaviour.

- [ ] **Step 3: Make the script executable and run it**

```bash
chmod +x cloud/scripts/verify-auto-routing.sh
./cloud/scripts/verify-auto-routing.sh
```
Expected last line: `Pass: 11   Fail: 0`.

If any case fails, do NOT proceed to commit. Read the `expected=... got=...` line, locate the corresponding step (Task 4-6), fix, re-run.

- [ ] **Step 4: Commit**

```bash
git add cloud/scripts/verify-auto-routing.sh
# Only add fake-upstream.mjs if it was actually modified
git add -p cloud/scripts/fake-upstream.mjs 2>/dev/null || true
git commit -m "test(cloud): e2e verify-auto-routing.sh — 11 assertions across 2 tenants"
```

---

## Task 9: Documentation — README + spec status update

**Files:**
- Modify: `cloud/README.md`

- [ ] **Step 1: Update the capabilities table**

Open `cloud/README.md`. Find the "关键能力 - 验证状态" table (around line 96-114). Add this row at the end, before the closing `|` table marker:

```markdown
| **Per-tenant 自动模型切换 (model=auto)** | `router/services/scenarioClassifier.js` + `scenarioRouter.js` + `admin/routes/routing.js` | ✅ `scripts/verify-auto-routing.sh` 11 个断言 |
```

- [ ] **Step 2: Add an "Auto Routing" section**

Insert this section between "客户端接入示例" and "安全模型":

```markdown
## Auto Routing — 一次配置,按场景自动切换模型

当客户端发请求时传 `model="auto"`(或省略),路由层会按请求内容自动分类到 6 个场景之一,然后查该租户预配置的 `场景 → target` 映射。其他显式 model 取值(`combo:slug`、`provider:model`)完全不走分类器。

### 场景分类(服务端硬编码,优先级首个命中获胜)

| 顺序 | 场景 | 判定 |
|---|---|---|
| 1 | `web` | `tools` 数组中 name 匹配 `/search\|web\|browse/i` |
| 2 | `tool_use` | `tools` 数组非空 |
| 3 | `vision` | 任意 message content 是 `image` / `image_url` / `image/*` |
| 4 | `long_context` | 估算 prompt tokens > 64000 (chars/4) |
| 5 | `think` | `reasoning_effort∈{high,max}` 或 `thinking.enabled=true` |
| 6 | `default` | 兜底 |

### 配置(Admin UI 或 REST)

```bash
# 设 default 场景
curl -X PUT http://localhost:30200/api/routing/default \
  -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"target":"combo:smart"}'

# 设 long_context 场景
curl -X PUT http://localhost:30200/api/routing/long_context \
  -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"target":"anthropic:claude-3-5-sonnet-20241022"}'
```

target 必须是 `combo:<slug>` 或 `<provider>:<model>` 格式。`combo:<slug>` 在 PUT 时会校验该 combo 在本租户存在且 enabled。

### 未配置场景的兜底

- 该 scenario 未配 → 自动用该租户的 `default` 配置
- `default` 也未配 → 请求返回 400 `Tenant has no default auto-routing target. Configure it in the dashboard.`

### 审计

`usage_events.routed_model` 列在客户端传 `model=auto` 时记录"实际选中的 target",显式 model 时为 NULL。
```

- [ ] **Step 3: Commit**

```bash
git add cloud/README.md
git commit -m "docs(cloud): document auto-routing — scenarios, configuration, audit"
```

---

## Self-review (run AFTER completing all 9 tasks)

- [ ] **Spec coverage check.** Re-read `cloud/docs/specs/2026-05-25-per-tenant-auto-model-switching-design.md`. Every section should map to at least one task:
  - §4 schema → Task 1
  - §5 classifier → Task 2
  - §6 resolver → Task 3
  - §7 chat completions integration → Task 4
  - §7 messages + usage routed_model → Task 5
  - §8 admin REST → Task 6
  - §8 admin UI → Task 7
  - §9 error handling → covered by classifier+resolver+route handlers (Task 6 step 3 asserts the error codes)
  - §10 isolation → exercised by Task 8 cross-tenant assertions
  - §11 tests → unit (Task 2), integration (Task 8)
  - §12 migration + rollback → Task 1 (rollback is `DROP TABLE tenant_routing; ALTER TABLE usage_events DROP COLUMN routed_model;` — not automated, but spec'd)
  - §13 unit list → covered by Task 1-9
  - §14 DoD → all 9 tasks committed + verify passes + UI works + README updated

- [ ] **No placeholders.** Search the plan for `TBD`, `TODO`, `FIXME`, `implement later`. Expected: zero hits.

- [ ] **Type consistency.** `routedModel` (camelCase, JS) maps to `routed_model` (snake_case, SQL). `effectiveModel` and `routedScenario` names are identical between Task 4 (`chatCompletions.js`) and Task 5 (`messages.js`). Module export names (`classifyScenario`, `resolveScenarioTarget`, `invalidateTenantRouting`, `SCENARIOS`) are referenced consistently across Tasks 2-7.

If any drift, fix inline and move on.
