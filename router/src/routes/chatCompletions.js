// POST /v1/chat/completions — OpenAI-compatible entry point.
// Pipeline:
//   1. Edge-auth → tenant context
//   2. Validate body
//   3. resolveAttempts() — combo / explicit / heuristic produces N attempts
//   4. For each attempt: pickAccount → call upstream → on retriable failure,
//      cooldown that connection and move on; only the LAST failure surfaces.
//   5. Record usage event (success or final failure)

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
import { chatCompletion, streamChatCompletion } from '../providers/openaiCompatible.js';
import { readJson, defaultBaseUrl } from '../lib/http.js';

export async function handleChatCompletions(req, res) {
  const startedAt = Date.now();
  const requestId = crypto.randomUUID();

  // 1) Auth
  const authHeader = req.headers['authorization'] || '';
  const apiKey = authHeader.startsWith('Bearer ') ? authHeader.slice(7) : '';
  if (!apiKey) throw new AuthError('Missing Authorization header');
  const ctx = await resolveApiKey(apiKey);
  enforceTenantActive(ctx);

  // 1b) Rate limit (per-key and per-tenant plan) + daily quota
  await enforceRateLimit(ctx);
  await enforceDailyQuota(ctx);

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
  const isStream = body.stream === true;

  // 5) Try in order
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
    if (!baseUrl) {
      errors.push({ step: attempt.step, provider: attempt.provider, reason: `no base_url for ${attempt.provider}` });
      continue;
    }
    const upstreamApiKey = account.credentials.api_key;
    if (!upstreamApiKey) {
      errors.push({ step: attempt.step, provider: attempt.provider, reason: 'connection missing api_key' });
      continue;
    }

    const upstreamBody = { ...body, model: attempt.upstreamModel };
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
        await handleStream({ req, res, baseUrl, upstreamApiKey, upstreamBody, usageBase, startedAt, attemptIndex: i, totalAttempts: attempts.length });
      } else {
        await handleNonStream({ res, baseUrl, upstreamApiKey, upstreamBody, usageBase, startedAt });
      }
      return; // success — exit the attempts loop
    } catch (err) {
      const status = err instanceof UpstreamError ? err.meta?.status_code : null;
      const retriable = status === 429 || (status >= 500 && status < 600) || !(err instanceof AppError);

      // Cooldown the connection on 429/5xx
      if (retriable && status) {
        await markCooldown(ctx.tenantId, attempt.provider, account.connectionId);
      }

      // Record the error
      await recordUsage({
        ...usageBase,
        status: 'error',
        latencyMs: Date.now() - startedAt,
        errorCode: status ? `upstream_${status}` : (err.code || 'unknown'),
        meta: { error: err.message, attempt: attempt.step },
      });

      errors.push({ step: attempt.step, provider: attempt.provider, status, message: err.message });

      // Headers already sent (mid-stream) → can't retry, bail
      if (res.headersSent) throw err;

      // Non-retriable client error → bail
      if (!retriable) throw err;

      // Otherwise fall through to the next attempt
      continue;
    }
  }

  // All attempts exhausted. If every attempt failed because no eligible account
  // was available (vs upstream errors), report 503 (config / availability) rather
  // than 502 (bad upstream response).
  const allNoAccount = errors.length > 0 && errors.every(e => !e.status);
  if (allNoAccount) {
    throw new NoAccountAvailableError({ attempts: errors });
  }
  throw new UpstreamError('All upstream attempts failed',
    { attempts: errors, hint: 'check provider availability or configure more connections' });
}

async function handleNonStream({ res, baseUrl, upstreamApiKey, upstreamBody, usageBase, startedAt }) {
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

async function handleStream({ req, res, baseUrl, upstreamApiKey, upstreamBody, usageBase, startedAt, attemptIndex, totalAttempts }) {
  let promptTokens = 0;
  let completionTokens = 0;

  const ac = new AbortController();
  req.on('close', () => ac.abort());

  // Important: we don't send headers until we get the first frame from upstream.
  // That way, an early-failure attempt can be retried without already having
  // committed to a 200 OK on the client connection.
  let headersSent = false;
  for await (const frame of streamChatCompletion({
    baseUrl, apiKey: upstreamApiKey, body: upstreamBody, signal: ac.signal,
  })) {
    if (!headersSent) {
      res.writeHead(200, {
        'content-type': 'text/event-stream',
        'cache-control': 'no-cache, no-transform',
        'connection': 'keep-alive',
      });
      headersSent = true;
    }
    res.write(frame);
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

