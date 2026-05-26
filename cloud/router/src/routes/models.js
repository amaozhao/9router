// GET /v1/models — OpenAI-compatible model listing.
// Aggregates the tenant's configured surface:
//   - every combo:slug (logical model the tenant defined)
//   - every <provider>:<known-model> derivable from active connections
// Returns {"object":"list","data":[{id,object:"model",owned_by,created},...]}.

import { query } from '@lazirouter-cloud/shared';
import { resolveApiKey, enforceTenantActive } from '../middleware/edgeAuth.js';

const WELL_KNOWN = {
  openai:   ['gpt-4o', 'gpt-4o-mini', 'gpt-4-turbo', 'o1-mini', 'o3-mini', 'text-embedding-3-small', 'text-embedding-3-large'],
  gemini:   ['gemini-2.0-flash', 'gemini-1.5-pro', 'gemini-1.5-flash', 'text-embedding-004'],
  deepseek: ['deepseek-chat', 'deepseek-reasoner'],
  glm:      ['glm-4', 'glm-4-flash'],
  claude:   ['claude-sonnet-4-5', 'claude-opus-4-5', 'claude-haiku-4-5'],
  codex:    ['gpt-5', 'gpt-5.5', 'gpt-5-codex'],
};

export async function handleModels(req, res) {
  const authHeader = req.headers['authorization'] || '';
  const xApi = typeof req.headers['x-api-key'] === 'string' ? req.headers['x-api-key'] : '';
  const apiKey = authHeader.startsWith('Bearer ') ? authHeader.slice(7) : xApi;
  if (!apiKey) {
    res.writeHead(401, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ error: { code: 'auth_error', message: 'Missing Authorization or x-api-key header' } }));
    return;
  }
  const ctx = await resolveApiKey(apiKey);
  enforceTenantActive(ctx);

  const [{ rows: combos }, { rows: conns }] = await Promise.all([
    query('SELECT slug FROM combos WHERE tenant_id = $1 AND enabled = TRUE ORDER BY slug', [ctx.tenantId]),
    query('SELECT DISTINCT provider FROM connections WHERE tenant_id = $1 AND enabled = TRUE', [ctx.tenantId]),
  ]);

  const now = Math.floor(Date.now() / 1000);
  const ids = new Set();
  const data = [];
  const push = (id, owned_by) => {
    if (ids.has(id)) return;
    ids.add(id);
    data.push({ id, object: 'model', created: now, owned_by });
  };

  for (const c of combos) push(`combo:${c.slug}`, 'tenant');
  for (const { provider } of conns) {
    for (const m of WELL_KNOWN[provider] || []) push(`${provider}:${m}`, provider);
  }

  res.writeHead(200, { 'content-type': 'application/json' });
  res.end(JSON.stringify({ object: 'list', data }));
}
