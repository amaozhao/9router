// POST /v1/messages — Anthropic-compatible entry point.
// Reuses the entire chat-completions pipeline (auth, rate limit, combo,
// account picking, cooldown, usage). The actual upstream call is selected
// by provider:
//   * provider === 'claude'   → claude.ai subscription executor (Anthropic
//     native passthrough with spoof headers).
//   * everything else         → OpenAI-compatible client; the Anthropic body
//     is translated to OpenAI and the response back to Anthropic.

import crypto from 'node:crypto';
import {
  logger, ValidationError, UpstreamError, AuthError,
  NoAccountAvailableError, AppError,
} from '@lazirouter-cloud/shared';
import { enforceDailyQuota } from '@lazirouter-cloud/shared';
import { resolveApiKey, enforceTenantActive } from '../middleware/edgeAuth.js';
import { enforceRateLimit } from '../middleware/rateLimit.js';
import { pickAccount, markCooldown } from '../services/accountPicker.js';
import { recordUsage, getPricing, computeCost } from '../services/usage.js';
import { resolveAttempts } from '../services/combo.js';
import { classifyScenario } from '../services/scenarioClassifier.js';
import { resolveScenarioTarget } from '../services/scenarioRouter.js';
import { getUpstream, isAnthropicNative } from '../providers/index.js';
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
  await enforceDailyQuota(ctx);

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

    const upstream = getUpstream(attempt.provider);
    const usesAnthropicShape = isAnthropicNative(attempt.provider);

    // For OpenAI-compat path, require base_url + api_key credentials.
    // For Anthropic-native (claude) path, require access_token from OAuth.
    let baseUrl = null;
    if (upstream.kind === 'openai') {
      baseUrl = account.metadata.base_url || defaultBaseUrl(attempt.provider);
      if (!baseUrl) { errors.push({ step: attempt.step, reason: `no base_url for ${attempt.provider}` }); continue; }
      if (!account.credentials.api_key) { errors.push({ step: attempt.step, reason: 'missing api_key' }); continue; }
    } else if (!account.credentials.access_token) {
      errors.push({ step: attempt.step, reason: 'missing OAuth access_token' });
      continue;
    }

    const upstreamBody = usesAnthropicShape
      ? { ...anthropicBody, model: attempt.upstreamModel }
      : { ...anthropicToOpenaiRequest(anthropicBody), model: attempt.upstreamModel };

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

    try {
      if (isStream) {
        await handleStream({
          req, res, upstream, credentials: account.credentials, metadata: account.metadata,
          baseUrl, upstreamBody,
          usesAnthropicShape, requestedModel: anthropicBody.model,
          usageBase, startedAt,
        });
      } else {
        await handleNonStream({
          res, upstream, credentials: account.credentials, metadata: account.metadata,
          baseUrl, upstreamBody,
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
  const allNoAccount = errors.length > 0 && errors.every(e => !e.status);
  if (allNoAccount) {
    throw new NoAccountAvailableError({ attempts: errors });
  }
  throw new UpstreamError('All upstream attempts failed', { attempts: errors });
}

async function handleNonStream({ res, upstream, credentials, metadata, baseUrl, upstreamBody, usesAnthropicShape, requestedModel, usageBase, startedAt }) {
  const result = await upstream.chat({ credentials, metadata, baseUrl, body: upstreamBody });
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

async function handleStream({ req, res, upstream, credentials, metadata, baseUrl, upstreamBody, usesAnthropicShape, requestedModel, usageBase, startedAt }) {
  const ac = new AbortController();
  req.on('close', () => ac.abort());

  let headersSent = false;
  let promptTokens = 0;
  let completionTokens = 0;
  const xlate = usesAnthropicShape ? null : createOpenaiToAnthropicStream(requestedModel);

  for await (const frame of upstream.stream({ credentials, metadata, baseUrl, body: upstreamBody, signal: ac.signal })) {
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
    // Sniff usage even for passthrough. Anthropic emits both input_tokens
    // and output_tokens; they can appear in either order across frames so
    // capture them independently and keep the latest seen.
    const pin  = /"(?:prompt_tokens|input_tokens)"\s*:\s*(\d+)/.exec(frame);
    const pout = /"(?:completion_tokens|output_tokens)"\s*:\s*(\d+)/.exec(frame);
    if (pin)  promptTokens = Number(pin[1]);
    if (pout) completionTokens = Number(pout[1]);
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
