// Shared Redis client — one connection per process.
// Uses ioredis for richer Lua/pipeline support than node-redis.

import IORedis from 'ioredis';
import { config } from './config.js';
import { logger } from './logger.js';

let _client = null;

export function getRedis() {
  if (!_client) {
    _client = new IORedis(config.redisUrl, {
      lazyConnect: false,
      maxRetriesPerRequest: 3,
      enableReadyCheck: true,
    });
    _client.on('error', (err) => logger.error({ err: err.message }, 'redis error'));
  }
  return _client;
}

export async function closeRedis() {
  if (_client) {
    await _client.quit();
    _client = null;
  }
}

export function tenantRoutingKey(tenantId) {
  return `routing:${tenantId}`;
}

export async function invalidateTenantRouting(tenantId) {
  await getRedis().del(tenantRoutingKey(tenantId));
}
