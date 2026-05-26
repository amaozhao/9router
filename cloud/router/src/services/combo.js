// Combo resolver — turns a logical model name into an ordered list of
// (provider, upstream_model) attempts. Two input shapes are supported:
//
//   1. `combo:<slug>` — look up combos.nodes for the tenant.
//      nodes = [{ provider, model, connection_id?, weight? }, ...]
//
//   2. `<provider>:<model>` — single-attempt resolution, no fallback.
//
//   3. `<bare-model>` — heuristic provider detection (kept for OpenAI compat).
//
// The result is an array of attempt descriptors; the route layer iterates
// them and stops at the first success.

import { query, ValidationError } from '@9router-cloud/shared';

export async function resolveAttempts(tenantId, modelInput) {
  if (!modelInput || typeof modelInput !== 'string') {
    throw new ValidationError('model must be a non-empty string');
  }

  if (modelInput.startsWith('combo:')) {
    const slug = modelInput.slice(6);
    const { rows } = await query(`
      SELECT nodes
      FROM combos
      WHERE tenant_id = $1 AND slug = $2 AND enabled = TRUE
      LIMIT 1
    `, [tenantId, slug]);
    if (rows.length === 0) {
      throw new ValidationError(`Unknown combo: ${slug}`,
        { hint: 'create it via POST /api/combos' });
    }
    const nodes = rows[0].nodes;
    if (!Array.isArray(nodes) || nodes.length === 0) {
      throw new ValidationError(`Combo ${slug} has no nodes`);
    }
    return nodes.map((n, i) => ({
      provider: String(n.provider).toLowerCase(),
      upstreamModel: String(n.model),
      connectionId: n.connection_id != null ? Number(n.connection_id) : null,
      step: i,
      sourceCombo: slug,
    }));
  }

  // Single attempt
  const idx = modelInput.indexOf(':');
  if (idx > 0 && idx < modelInput.length - 1) {
    return [{
      provider: modelInput.slice(0, idx).toLowerCase(),
      upstreamModel: modelInput.slice(idx + 1),
      connectionId: null,
      step: 0,
      sourceCombo: null,
    }];
  }

  // Heuristic for raw model id (matches the historical 9router behaviour).
  // GPT and o-series default to `codex` — the ChatGPT subscription path —
  // because that's the only OpenAI-side route a user normally has without
  // an API key. Callers who want the api-key OpenAI path can still send
  // `openai:gpt-4` explicitly.
  let provider = 'openai';
  if (modelInput.startsWith('glm-')) provider = 'glm';
  else if (modelInput.startsWith('deepseek-')) provider = 'deepseek';
  else if (modelInput.startsWith('claude-')) provider = 'anthropic';
  else if (modelInput.startsWith('gemini-')) provider = 'gemini';
  else if (modelInput.startsWith('mock-')) provider = 'mock';
  else if (/^(gpt-|o\d|chatgpt-)/i.test(modelInput)) provider = 'codex';

  return [{ provider, upstreamModel: modelInput, connectionId: null, step: 0, sourceCombo: null }];
}
