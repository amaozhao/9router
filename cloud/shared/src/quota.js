// Per-tenant daily token/request quota enforcement.
//
// Counters live in Redis under
//   quota:t:<tenantId>:<YYYY-MM-DD>:requests
//   quota:t:<tenantId>:<YYYY-MM-DD>:tokens
// with a 48h TTL so a slow midnight wrap-around can never lose data.
//
// Day boundary is computed in UTC+8 (Asia/Shanghai) to match user expectations
// for a domestic-China product. Tokens are counted post-success via
// incrementTokenUsage(); requests are counted pre-attempt so failed/aborted
// requests still consume quota.

import { getRedis } from './redis.js';
import { query } from './db.js';
import { RateLimitError } from './errors.js';

const PLAN_QUOTAS = {
  free:       { tokens: 100_000,     requests: 1_000 },
  starter:    { tokens: 1_000_000,   requests: 10_000 },
  pro:        { tokens: 10_000_000,  requests: 100_000 },
  enterprise: { tokens: null,        requests: null },     // unlimited
};

const QUOTA_CFG_CACHE_TTL_SEC = 300;
const COUNTER_TTL_SEC = 48 * 3600;

function todayKeyCn() {
  // ISO date in Asia/Shanghai (UTC+8). No external dep — just shift then slice.
  const cn = new Date(Date.now() + 8 * 3600 * 1000);
  return cn.toISOString().slice(0, 10);
}

async function fetchLimits(tenantId, plan) {
  const redis = getRedis();
  const cacheKey = `quota:cfg:${tenantId}`;
  const cached = await redis.get(cacheKey);
  if (cached) return JSON.parse(cached);

  const { rows } = await query(
    `SELECT daily_token_limit, daily_request_limit FROM tenant_quotas WHERE tenant_id = $1`,
    [tenantId]
  );
  const defaults = PLAN_QUOTAS[plan] || PLAN_QUOTAS.free;
  const limits = {
    tokens:   rows[0]?.daily_token_limit   != null ? Number(rows[0].daily_token_limit)   : defaults.tokens,
    requests: rows[0]?.daily_request_limit != null ? Number(rows[0].daily_request_limit) : defaults.requests,
  };
  await redis.set(cacheKey, JSON.stringify(limits), 'EX', QUOTA_CFG_CACHE_TTL_SEC);
  return limits;
}

export async function enforceDailyQuota(ctx) {
  const limits = await fetchLimits(ctx.tenantId, ctx.plan);
  if (limits.requests == null && limits.tokens == null) return;

  const redis = getRedis();
  const date = todayKeyCn();
  const reqKey = `quota:t:${ctx.tenantId}:${date}:requests`;
  const tokKey = `quota:t:${ctx.tenantId}:${date}:tokens`;

  // Pre-check tokens: we don't know how many this request will consume,
  // so we reject if we're already at/over the limit.
  if (limits.tokens != null) {
    const used = Number(await redis.get(tokKey)) || 0;
    if (used >= limits.tokens) {
      throw new RateLimitError('Daily token quota exceeded', {
        scope: 'daily_tokens', limit: limits.tokens, used, window: 'today_cn_8',
      });
    }
  }

  if (limits.requests != null) {
    const pipe = redis.pipeline();
    pipe.incr(reqKey);
    pipe.expire(reqKey, COUNTER_TTL_SEC);
    const results = await pipe.exec();
    const count = results[0][1];
    if (count > limits.requests) {
      throw new RateLimitError('Daily request quota exceeded', {
        scope: 'daily_requests', limit: limits.requests, observed: count, window: 'today_cn_8',
      });
    }
  }
}

export async function incrementTokenUsage(tenantId, totalTokens) {
  if (!totalTokens || totalTokens <= 0) return;
  const redis = getRedis();
  const date = todayKeyCn();
  const tokKey = `quota:t:${tenantId}:${date}:tokens`;
  const pipe = redis.pipeline();
  pipe.incrby(tokKey, Math.floor(totalTokens));
  pipe.expire(tokKey, COUNTER_TTL_SEC);
  await pipe.exec();
}

export async function invalidateQuotaCache(tenantId) {
  await getRedis().del(`quota:cfg:${tenantId}`);
}

export async function readQuotaUsage(tenantId, plan) {
  const limits = await fetchLimits(tenantId, plan);
  const redis = getRedis();
  const date = todayKeyCn();
  const [reqUsed, tokUsed] = await Promise.all([
    redis.get(`quota:t:${tenantId}:${date}:requests`),
    redis.get(`quota:t:${tenantId}:${date}:tokens`),
  ]);
  return {
    date,
    requests: { used: Number(reqUsed) || 0, limit: limits.requests },
    tokens:   { used: Number(tokUsed) || 0, limit: limits.tokens },
  };
}

export { PLAN_QUOTAS };
