// POST /v1/chat/completions — OpenAI-compatible entry point.
// Pipeline:
//   1. Edge-auth → tenant context
//   2. Validate body
//   3. Resolve provider via tenant settings / alias / direct
//   4. AccountPicker → upstream connection
//   5. Call upstream (stream or non-stream)
//   6. On error: cooldown + bubble up (Phase 3 will add combo fallback)
//   7. Record usage event

import crypto from 'node:crypto';
import { logger, ValidationError, UpstreamError, AuthError } from '@9router-cloud/shared';
import { resolveApiKey, enforceTenantActive } from '../middleware/edgeAuth.js';
import { pickAccount, markCooldown } from '../services/accountPicker.js';
import { recordUsage, getPricing, computeCost } from '../services/usage.js';
import { chatCompletion, streamChatCompletion } from '../providers/openaiCompatible.js';

export async function handleChatCompletions(req, res) {
  const startedAt = Date.now();
  const requestId = crypto.randomUUID();

  // 1) Auth
  const authHeader = req.headers['authorization'] || '';
  const apiKey = authHeader.startsWith('Bearer ') ? authHeader.slice(7) : '';
  if (!apiKey) throw new AuthError('Missing Authorization header');
  const ctx = await resolveApiKey(apiKey);
  enforceTenantActive(ctx);

  // 2) Parse body
  const body = await readJson(req);
  if (!body || !body.model) throw new ValidationError('Missing field: model');
  if (!Array.isArray(body.messages)) throw new ValidationError('Missing field: messages[]');

  // 3) Resolve provider — Phase 1: assume model is `<provider>:<upstream_model>`
  //    or simply `<upstream_model>` (defaults to provider taken from settings).
  const { provider, upstreamModel } = parseModel(body.model);

  // 4) Pick account
  const account = await pickAccount(ctx.tenantId, provider);

  // 5) Build upstream call
  const upstreamBody = { ...body, model: upstreamModel };
  const baseUrl = account.metadata.base_url || defaultBaseUrl(provider);
  if (!baseUrl) {
    throw new ValidationError(`No base_url configured for provider "${provider}"`,
      { provider, hint: 'set metadata.base_url on the connection' });
  }
  const upstreamApiKey = account.credentials.api_key;
  if (!upstreamApiKey) {
    throw new ValidationError('Connection has no api_key',
      { connection_id: account.connectionId });
  }

  const isStream = body.stream === true;

  const usageBase = {
    tenantId: ctx.tenantId,
    apiKeyId: ctx.apiKeyId,
    connectionId: account.connectionId,
    provider,
    model: body.model,
    upstreamModel,
    requestId,
  };

  try {
    if (isStream) {
      await handleStream({ req, res, baseUrl, upstreamApiKey, upstreamBody, account, usageBase, startedAt });
    } else {
      await handleNonStream({ res, baseUrl, upstreamApiKey, upstreamBody, account, usageBase, startedAt });
    }
  } catch (err) {
    const latency = Date.now() - startedAt;
    if (err instanceof UpstreamError) {
      const status = err.meta?.status_code;
      if (status === 429 || (status >= 500 && status < 600)) {
        await markCooldown(ctx.tenantId, provider, account.connectionId);
      }
      await recordUsage({
        ...usageBase, status: 'error', latencyMs: latency, errorCode: `upstream_${status || 'unknown'}`,
        meta: { error: err.message },
      });
    }
    throw err;
  }
}

async function handleNonStream({ res, baseUrl, upstreamApiKey, upstreamBody, account, usageBase, startedAt }) {
  const result = await chatCompletion({ baseUrl, apiKey: upstreamApiKey, body: upstreamBody });
  const usage = result.usage || {};
  const promptTokens = usage.prompt_tokens || 0;
  const completionTokens = usage.completion_tokens || 0;

  const pricing = await getPricing(usageBase.tenantId, usageBase.provider, usageBase.upstreamModel);
  const costMicros = computeCost(pricing, promptTokens, completionTokens);

  res.writeHead(200, { 'content-type': 'application/json' });
  res.end(JSON.stringify(result));

  await recordUsage({
    ...usageBase, status: 'ok', latencyMs: Date.now() - startedAt,
    promptTokens, completionTokens, costMicros,
  });
}

async function handleStream({ req, res, baseUrl, upstreamApiKey, upstreamBody, account, usageBase, startedAt }) {
  res.writeHead(200, {
    'content-type': 'text/event-stream',
    'cache-control': 'no-cache, no-transform',
    'connection': 'keep-alive',
  });

  let promptTokens = 0;
  let completionTokens = 0;

  const ac = new AbortController();
  req.on('close', () => ac.abort());

  for await (const frame of streamChatCompletion({
    baseUrl, apiKey: upstreamApiKey, body: upstreamBody, signal: ac.signal,
  })) {
    res.write(frame);
    // Sniff usage if the provider emits a final `data: {"usage": ...}` frame
    const m = /"usage"\s*:\s*\{[^}]*"prompt_tokens"\s*:\s*(\d+)[^}]*"completion_tokens"\s*:\s*(\d+)/.exec(frame);
    if (m) { promptTokens = Number(m[1]); completionTokens = Number(m[2]); }
  }
  res.end();

  const pricing = await getPricing(usageBase.tenantId, usageBase.provider, usageBase.upstreamModel);
  const costMicros = computeCost(pricing, promptTokens, completionTokens);
  await recordUsage({
    ...usageBase, status: 'ok', latencyMs: Date.now() - startedAt,
    promptTokens, completionTokens, costMicros,
  });
}

function parseModel(model) {
  const idx = model.indexOf(':');
  if (idx > 0 && idx < model.length - 1) {
    return { provider: model.slice(0, idx), upstreamModel: model.slice(idx + 1) };
  }
  // Heuristic for raw model id
  if (model.startsWith('glm-')) return { provider: 'glm', upstreamModel: model };
  if (model.startsWith('deepseek-')) return { provider: 'deepseek', upstreamModel: model };
  if (model.startsWith('claude-')) return { provider: 'anthropic', upstreamModel: model };
  if (model.startsWith('gemini-')) return { provider: 'gemini', upstreamModel: model };
  return { provider: 'openai', upstreamModel: model };
}

function defaultBaseUrl(provider) {
  switch (provider) {
    case 'openai':   return 'https://api.openai.com';
    case 'glm':      return 'https://open.bigmodel.cn/api/paas/v4';
    case 'deepseek': return 'https://api.deepseek.com';
    case 'minimax':  return 'https://api.minimaxi.com';
    default: return null;
  }
}

function readJson(req) {
  return new Promise((resolve, reject) => {
    let buf = '';
    req.on('data', c => { buf += c; if (buf.length > 5 * 1024 * 1024) { req.destroy(); reject(new ValidationError('Body too large')); } });
    req.on('end', () => {
      if (!buf) return resolve({});
      try { resolve(JSON.parse(buf)); }
      catch (e) { reject(new ValidationError('Invalid JSON body')); }
    });
    req.on('error', reject);
  });
}
