// POST /v1/responses — OpenAI Responses-API entry point.
//
// Codex CLI (wire_api="responses") and any caller that speaks the
// Responses API can target this endpoint. The route mirrors the chat /
// messages pipeline (auth → rate limit → combo → pick account → upstream →
// usage event) but the upstream call is provider-native: codex subscription
// goes to https://chatgpt.com/backend-api/codex/responses with the spoof
// headers; the OpenAI Responses path (api.openai.com/v1/responses) is
// reachable via an `openai` connection.

import crypto from 'node:crypto';
import {
  ValidationError, UpstreamError, AuthError,
  NoAccountAvailableError, AppError,
} from '@lazirouter-cloud/shared';
import { enforceDailyQuota } from '@lazirouter-cloud/shared';
import { resolveApiKey, enforceTenantActive } from '../middleware/edgeAuth.js';
import { enforceRateLimit } from '../middleware/rateLimit.js';
import { pickAccount, markCooldown } from '../services/accountPicker.js';
import { recordUsage, getPricing, computeCost } from '../services/usage.js';
import { resolveAttempts } from '../services/combo.js';
import { getUpstream, isResponsesNative } from '../providers/index.js';
import { readJson } from '../lib/http.js';

export async function handleResponses(req, res) {
  const startedAt = Date.now();
  const requestId = crypto.randomUUID();

  // 1) Auth — both Bearer and x-api-key accepted to match SDK conventions
  const authHeader = req.headers['authorization'] || '';
  const bearer = authHeader.startsWith('Bearer ') ? authHeader.slice(7) : '';
  const xApi = typeof req.headers['x-api-key'] === 'string' ? req.headers['x-api-key'] : '';
  const apiKey = bearer || xApi;
  if (!apiKey) throw new AuthError('Missing Authorization or x-api-key header');
  const ctx = await resolveApiKey(apiKey);
  enforceTenantActive(ctx);
  await enforceRateLimit(ctx);
  await enforceDailyQuota(ctx);

  const body = await readJson(req);
  if (!body.model) throw new ValidationError('Missing field: model');
  if (!body.input && !Array.isArray(body.messages)) {
    throw new ValidationError('Missing field: input[] or messages[]');
  }

  // 2) Attempts (combo / explicit provider / heuristic)
  const attempts = await resolveAttempts(ctx.tenantId, body.model);
  // Default to streaming because the Codex backend doesn't support non-stream
  // and most callers prefer it anyway.
  const isStream = body.stream !== false;

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
    const usesResponsesNative = isResponsesNative(attempt.provider);

    if (!usesResponsesNative) {
      errors.push({ step: attempt.step, reason: `provider ${attempt.provider} does not expose a Responses API path` });
      continue;
    }
    if (!account.credentials.access_token) {
      errors.push({ step: attempt.step, reason: 'missing OAuth access_token' });
      continue;
    }

    const upstreamBody = { ...body, model: attempt.upstreamModel };
    const sessionContext = { tenantId: ctx.tenantId, connectionId: account.connectionId };

    const usageBase = {
      tenantId: ctx.tenantId,
      apiKeyId: ctx.apiKeyId,
      connectionId: account.connectionId,
      provider: attempt.provider,
      model: body.model,
      upstreamModel: attempt.upstreamModel,
      requestId,
    };

    try {
      if (isStream) {
        await handleStream({
          req, res, upstream, credentials: account.credentials, metadata: account.metadata,
          upstreamBody, sessionContext, usageBase, startedAt,
        });
      } else {
        await handleNonStream({
          res, upstream, credentials: account.credentials, metadata: account.metadata,
          upstreamBody, sessionContext, usageBase, startedAt,
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
        meta: { error: err.message, attempt: attempt.step, transport: '/v1/responses' },
      });
      errors.push({ step: attempt.step, provider: attempt.provider, status, message: err.message });
      if (res.headersSent) throw err;
      if (!retriable) throw err;
    }
  }
  const allNoAccount = errors.length > 0 && errors.every(e => !e.status);
  if (allNoAccount) throw new NoAccountAvailableError({ attempts: errors });
  throw new UpstreamError('All upstream attempts failed', { attempts: errors });
}

// Pull the authoritative {input_tokens, output_tokens} from a SSE frame.
// The Responses API emits multiple usage-shaped objects per response
// (image_gen / web_search tool usage are zero-valued and nested), so a plain
// regex on "input_tokens" picks up the wrong one. We target the
// `response.completed` event specifically and JSON-parse `data.response.usage`.
function extractUsage(frame) {
  // Frames may contain several SSE events delimited by blank lines. Find the
  // last response.completed in this chunk (typical: it is the last frame).
  const lines = frame.split('\n');
  let event = null;
  let lastData = null;
  for (const ln of lines) {
    if (ln.startsWith('event: ')) event = ln.slice(7).trim();
    else if (ln.startsWith('data: ') && event === 'response.completed') lastData = ln.slice(6);
    else if (ln === '') event = null;
  }
  if (!lastData) return null;
  try {
    const obj = JSON.parse(lastData);
    const u = obj?.response?.usage;
    if (!u) return null;
    return {
      promptTokens:     Number(u.input_tokens)  || 0,
      completionTokens: Number(u.output_tokens) || 0,
    };
  } catch { return null; }
}

async function handleNonStream({ res, upstream, credentials, metadata, upstreamBody, sessionContext, usageBase, startedAt }) {
  // Codex backend always streams; we collect the stream and re-emit JSON.
  // For now we forward the SSE text as-is to a JSON-headers response so the
  // client can pick frames out — most non-streaming Responses callers parse
  // the same way.
  let buf = '';
  let promptTokens = 0;
  let completionTokens = 0;
  for await (const frame of upstream.stream({ credentials, metadata, body: upstreamBody, sessionContext })) {
    buf += frame;
    const u = extractUsage(frame);
    if (u) { promptTokens = u.promptTokens; completionTokens = u.completionTokens; }
  }
  const pricing = await getPricing(usageBase.tenantId, usageBase.provider, usageBase.upstreamModel);
  const costMicros = computeCost(pricing, promptTokens, completionTokens);
  res.writeHead(200, { 'content-type': 'text/event-stream' });
  res.end(buf);
  await recordUsage({
    ...usageBase, status: 'ok', latencyMs: Date.now() - startedAt,
    promptTokens, completionTokens, costMicros,
    meta: { transport: '/v1/responses' },
  });
}

async function handleStream({ req, res, upstream, credentials, metadata, upstreamBody, sessionContext, usageBase, startedAt }) {
  const ac = new AbortController();
  req.on('close', () => ac.abort());

  let headersSent = false;
  let promptTokens = 0;
  let completionTokens = 0;

  for await (const frame of upstream.stream({ credentials, metadata, body: upstreamBody, signal: ac.signal, sessionContext })) {
    if (!headersSent) {
      res.writeHead(200, {
        'content-type': 'text/event-stream',
        'cache-control': 'no-cache, no-transform',
        'connection': 'keep-alive',
      });
      headersSent = true;
    }
    res.write(frame);
    const u = extractUsage(frame);
    if (u) { promptTokens = u.promptTokens; completionTokens = u.completionTokens; }
  }
  res.end();

  const pricing = await getPricing(usageBase.tenantId, usageBase.provider, usageBase.upstreamModel);
  const costMicros = computeCost(pricing, promptTokens, completionTokens);
  await recordUsage({
    ...usageBase, status: 'ok', latencyMs: Date.now() - startedAt,
    promptTokens, completionTokens, costMicros,
    meta: { transport: '/v1/responses' },
  });
}
