// POST /v1/messages — Anthropic-compatible entry point.
// Reuses the entire chat-completions pipeline (auth, rate limit, combo,
// account picking, cooldown, usage) — only the body translation differs.
//
// If the resolved provider is 'anthropic' we pass through. Otherwise we
// translate Anthropic→OpenAI on the way in and OpenAI→Anthropic on the way out.

import crypto from 'node:crypto';
import {
  ValidationError, UpstreamError, AuthError,
  NoAccountAvailableError, AppError,
} from '@9router-cloud/shared';
import { resolveApiKey, enforceTenantActive } from '../middleware/edgeAuth.js';
import { enforceRateLimit } from '../middleware/rateLimit.js';
import { pickAccount, markCooldown } from '../services/accountPicker.js';
import { recordUsage, getPricing, computeCost } from '../services/usage.js';
import { resolveAttempts } from '../services/combo.js';
import { chatCompletion, streamChatCompletion } from '../providers/openaiCompatible.js';
import {
  anthropicToOpenaiRequest,
  openaiToAnthropicResponse,
  createOpenaiToAnthropicStream,
} from '../translator/anthropic.js';

export async function handleMessages(req, res) {
  const startedAt = Date.now();
  const requestId = crypto.randomUUID();

  const authHeader = req.headers['authorization'] || '';
  const apiKey = authHeader.startsWith('Bearer ') ? authHeader.slice(7) : '';
  // Anthropic SDKs use the `x-api-key` header instead of Authorization
  const xApi = req.headers['x-api-key'];
  const effectiveKey = apiKey || (typeof xApi === 'string' ? xApi : '');
  if (!effectiveKey) throw new AuthError('Missing Authorization or x-api-key header');
  const ctx = await resolveApiKey(effectiveKey);
  enforceTenantActive(ctx);
  await enforceRateLimit(ctx);

  const anthropicBody = await readJson(req);
  if (!anthropicBody.model) throw new ValidationError('Missing field: model');
  if (!Array.isArray(anthropicBody.messages)) throw new ValidationError('Missing field: messages[]');

  // The model string controls combo resolution exactly like /v1/chat/completions
  const attempts = await resolveAttempts(ctx.tenantId, anthropicBody.model);
  const isStream = anthropicBody.stream === true;

  const errors = [];
  for (let i = 0; i < attempts.length; i++) {
    const attempt = attempts[i];
    let account;
    try {
      account = await pickAccount(ctx.tenantId, attempt.provider);
    } catch (e) {
      if (e instanceof NoAccountAvailableError) {
        errors.push({ step: attempt.step, provider: attempt.provider, reason: e.message });
        continue;
      }
      throw e;
    }

    const baseUrl = account.metadata.base_url || defaultBaseUrl(attempt.provider);
    if (!baseUrl) { errors.push({ step: attempt.step, reason: `no base_url for ${attempt.provider}` }); continue; }
    const upstreamApiKey = account.credentials.api_key;
    if (!upstreamApiKey) { errors.push({ step: attempt.step, reason: 'missing api_key' }); continue; }

    // Translate request unless the provider is native Anthropic
    const usesAnthropicShape = attempt.provider === 'anthropic';
    const upstreamBody = usesAnthropicShape
      ? { ...anthropicBody, model: attempt.upstreamModel }
      : { ...anthropicToOpenaiRequest(anthropicBody), model: attempt.upstreamModel };

    const usageBase = {
      tenantId: ctx.tenantId,
      apiKeyId: ctx.apiKeyId,
      connectionId: account.connectionId,
      provider: attempt.provider,
      model: anthropicBody.model,
      upstreamModel: attempt.upstreamModel,
      requestId,
    };

    try {
      if (isStream) {
        await handleStream({
          req, res, baseUrl, upstreamApiKey, upstreamBody,
          usesAnthropicShape, requestedModel: anthropicBody.model,
          usageBase, startedAt,
        });
      } else {
        await handleNonStream({
          res, baseUrl, upstreamApiKey, upstreamBody,
          usesAnthropicShape, requestedModel: anthropicBody.model,
          usageBase, startedAt,
        });
      }
      return;
    } catch (err) {
      const status = err instanceof UpstreamError ? err.meta?.status_code : null;
      const retriable = status === 429 || (status >= 500 && status < 600) || !(err instanceof AppError);
      if (retriable && status) await markCooldown(ctx.tenantId, attempt.provider, account.connectionId);
      await recordUsage({
        ...usageBase, status: 'error',
        latencyMs: Date.now() - startedAt,
        errorCode: status ? `upstream_${status}` : (err.code || 'unknown'),
        meta: { error: err.message, attempt: attempt.step, transport: '/v1/messages' },
      });
      errors.push({ step: attempt.step, provider: attempt.provider, status, message: err.message });
      if (res.headersSent) throw err;
      if (!retriable) throw err;
    }
  }
  throw new UpstreamError('All upstream attempts failed', { attempts: errors });
}

async function handleNonStream({ res, baseUrl, upstreamApiKey, upstreamBody, usesAnthropicShape, requestedModel, usageBase, startedAt }) {
  const result = await chatCompletion({ baseUrl, apiKey: upstreamApiKey, body: upstreamBody });
  const anthropicResp = usesAnthropicShape ? result : openaiToAnthropicResponse(result, requestedModel);

  const usage = result.usage || {};
  const promptTokens = usage.prompt_tokens ?? usage.input_tokens ?? 0;
  const completionTokens = usage.completion_tokens ?? usage.output_tokens ?? 0;
  const pricing = await getPricing(usageBase.tenantId, usageBase.provider, usageBase.upstreamModel);
  const costMicros = computeCost(pricing, promptTokens, completionTokens);

  res.writeHead(200, { 'content-type': 'application/json' });
  res.end(JSON.stringify(anthropicResp));

  await recordUsage({
    ...usageBase, status: 'ok', latencyMs: Date.now() - startedAt,
    promptTokens, completionTokens, costMicros,
    meta: { transport: '/v1/messages' },
  });
}

async function handleStream({ req, res, baseUrl, upstreamApiKey, upstreamBody, usesAnthropicShape, requestedModel, usageBase, startedAt }) {
  const ac = new AbortController();
  req.on('close', () => ac.abort());

  let headersSent = false;
  let promptTokens = 0;
  let completionTokens = 0;
  const xlate = usesAnthropicShape ? null : createOpenaiToAnthropicStream(requestedModel);

  for await (const frame of streamChatCompletion({ baseUrl, apiKey: upstreamApiKey, body: upstreamBody, signal: ac.signal })) {
    if (!headersSent) {
      res.writeHead(200, {
        'content-type': 'text/event-stream',
        'cache-control': 'no-cache, no-transform',
        'connection': 'keep-alive',
      });
      headersSent = true;
    }
    if (xlate) {
      for (const out of xlate.feed(frame)) res.write(out);
    } else {
      res.write(frame);
    }
    // Sniff usage even for passthrough
    const m = /"usage"\s*:\s*\{[^}]*"(?:prompt_tokens|input_tokens)"\s*:\s*(\d+)[^}]*"(?:completion_tokens|output_tokens)"\s*:\s*(\d+)/.exec(frame);
    if (m) { promptTokens = Number(m[1]); completionTokens = Number(m[2]); }
  }
  if (xlate) {
    for (const out of xlate.flush()) res.write(out);
    const u = xlate.getUsage();
    if (u.input_tokens)  promptTokens     = u.input_tokens;
    if (u.output_tokens) completionTokens = u.output_tokens;
  }
  res.end();

  const pricing = await getPricing(usageBase.tenantId, usageBase.provider, usageBase.upstreamModel);
  const costMicros = computeCost(pricing, promptTokens, completionTokens);
  await recordUsage({
    ...usageBase, status: 'ok', latencyMs: Date.now() - startedAt,
    promptTokens, completionTokens, costMicros,
    meta: { transport: '/v1/messages' },
  });
}

function defaultBaseUrl(provider) {
  switch (provider) {
    case 'openai':    return 'https://api.openai.com';
    case 'glm':       return 'https://open.bigmodel.cn/api/paas/v4';
    case 'deepseek':  return 'https://api.deepseek.com';
    case 'minimax':   return 'https://api.minimaxi.com';
    case 'anthropic': return 'https://api.anthropic.com';
    default: return null;
  }
}

function readJson(req) {
  return new Promise((resolve, reject) => {
    let buf = '';
    req.on('data', c => { buf += c; if (buf.length > 5 * 1024 * 1024) { req.destroy(); reject(new ValidationError('Body too large')); } });
    req.on('end', () => {
      if (!buf) return resolve({});
      try { resolve(JSON.parse(buf)); } catch { reject(new ValidationError('Invalid JSON body')); }
    });
    req.on('error', reject);
  });
}
