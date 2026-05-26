// Per-tenant + per-key rate limiting using a fixed-window counter in Redis.
//
// Strategy: INCR(key) — if it returns 1 we just created the window, so we set
// EXPIRE to make sure it resets. The window length is 60 seconds.
//
// Two limits are enforced (whichever trips first):
//   * api_keys.rate_limit_rpm  (per individual key)
//   * tenant plan default      (per whole tenant)
//
// Tenant defaults are mapped from the `plan` column:
//   free     → 60 rpm
//   starter  → 600 rpm
//   pro      → 6000 rpm
// Customize in PLAN_LIMITS as needed.

import { getRedis, RateLimitError } from '@lazirouter-cloud/shared';

const PLAN_LIMITS = {
  free: 60,
  starter: 600,
  pro: 6000,
  enterprise: null, // null = unlimited
};

export async function enforceRateLimit(ctx) {
  const redis = getRedis();
  const minute = Math.floor(Date.now() / 60_000);

  // Per-key limit (only if set)
  if (Number.isInteger(ctx.rateLimitRpm) && ctx.rateLimitRpm > 0) {
    await tick(redis, `rl:k:${ctx.apiKeyId}:${minute}`, ctx.rateLimitRpm, 'api_key');
  }

  // Per-tenant plan limit
  const tenantLimit = PLAN_LIMITS[ctx.plan];
  if (tenantLimit) {
    await tick(redis, `rl:t:${ctx.tenantId}:${minute}`, tenantLimit, 'tenant');
  }
}

async function tick(redis, key, limit, scope) {
  // Atomic: INCR + (if first) EXPIRE
  const pipeline = redis.pipeline();
  pipeline.incr(key);
  pipeline.expire(key, 60);
  const results = await pipeline.exec();
  const [incrErr, count] = results[0];
  if (incrErr) throw incrErr;
  if (count > limit) {
    throw new RateLimitError(`Rate limit exceeded (${scope}: ${limit}/min)`,
      { scope, limit, observed: count, window_sec: 60 });
  }
}
