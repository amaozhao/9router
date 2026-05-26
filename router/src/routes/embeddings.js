// POST /v1/embeddings — OpenAI-compatible embeddings entry point.
// Same pipeline as /v1/chat/completions but non-streaming only.
// Pricing for embedding models gets looked up via the same `pricing` table;
// callers that haven't seeded a row get cost=0.

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
import { embed } from '../providers/openaiCompatible.js';
import { readJson, defaultBaseUrl } from '../lib/http.js';

export async function handleEmbeddings(req, res) {
  const startedAt = Date.now();
  const requestId = crypto.randomUUID();

  const authHeader = req.headers['authorization'] || '';
  const apiKey = authHeader.startsWith('Bearer ') ? authHeader.slice(7) : '';
  if (!apiKey) throw new AuthError('Missing Authorization header');
  const ctx = await resolveApiKey(apiKey);
  enforceTenantActive(ctx);
  await enforceRateLimit(ctx);
  await enforceDailyQuota(ctx);

  const body = await readJson(req);
  if (!body.model) throw new ValidationError('Missing field: model');
  if (body.input == null) throw new ValidationError('Missing field: input');

  const attempts = await resolveAttempts(ctx.tenantId, body.model);
  const errors = [];

  for (const attempt of attempts) {
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
    const upstreamApiKey = account.credentials.api_key;
    if (!baseUrl || !upstreamApiKey) {
      errors.push({ step: attempt.step, provider: attempt.provider, reason: 'connection missing base_url or api_key' });
      continue;
    }

    const upstreamBody = { ...body, model: attempt.upstreamModel };
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
      const result = await embed({ baseUrl, apiKey: upstreamApiKey, body: upstreamBody });
      const promptTokens = result.usage?.prompt_tokens || 0;
      const pricing = await getPricing(usageBase.tenantId, usageBase.provider, usageBase.upstreamModel);
      const costMicros = computeCost(pricing, promptTokens, 0);

      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify(result));

      await recordUsage({
        ...usageBase, status: 'ok', latencyMs: Date.now() - startedAt,
        promptTokens, completionTokens: 0, costMicros,
        meta: { transport: '/v1/embeddings' },
      });
      return;
    } catch (err) {
      const status = err instanceof UpstreamError ? err.meta?.status_code : null;
      const retriable = status === 429 || (status >= 500 && status < 600) || !(err instanceof AppError);
      if (retriable && status) await markCooldown(ctx.tenantId, attempt.provider, account.connectionId);

      await recordUsage({
        ...usageBase, status: 'error',
        latencyMs: Date.now() - startedAt,
        errorCode: status ? `upstream_${status}` : (err.code || 'unknown'),
        meta: { error: err.message, transport: '/v1/embeddings' },
      });
      errors.push({ step: attempt.step, provider: attempt.provider, status, message: err.message });
      if (!retriable) throw err;
    }
  }

  const allNoAccount = errors.length > 0 && errors.every(e => !e.status);
  if (allNoAccount) throw new NoAccountAvailableError({ attempts: errors });
  throw new UpstreamError('All upstream attempts failed', { attempts: errors });
}
