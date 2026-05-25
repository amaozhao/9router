// Router HTTP server — minimal native http for Phase 1.
// We avoid Express/Fastify in Phase 1 to keep the runtime lean and the code
// readable; Phase 2.5 will introduce a framework if dashboard SSR demands it.

import http from 'node:http';
import { config, logger, AppError, getRedis, closeRedis, closeDb } from '@9router-cloud/shared';
import { handleChatCompletions } from './routes/chatCompletions.js';

const log = logger.child({ svc: 'router' });

const server = http.createServer(async (req, res) => {
  const start = Date.now();
  try {
    if (req.method === 'GET' && req.url === '/health') {
      return jsonOk(res, await healthCheck());
    }
    if (req.method === 'POST' && req.url === '/v1/chat/completions') {
      return await handleChatCompletions(req, res);
    }
    if (req.method === 'GET' && req.url === '/') {
      return jsonOk(res, { name: '9router-cloud', version: '0.1.0' });
    }
    res.writeHead(404, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ error: { code: 'not_found', message: 'No route' } }));
  } catch (err) {
    return writeError(res, err);
  } finally {
    log.info({ method: req.method, url: req.url, status: res.statusCode, ms: Date.now() - start }, 'http');
  }
});

async function healthCheck() {
  const redis = getRedis();
  const pong = await redis.ping();
  return { ok: true, redis: pong, ts: new Date().toISOString() };
}

function jsonOk(res, body) {
  res.writeHead(200, { 'content-type': 'application/json' });
  res.end(JSON.stringify(body));
}

function writeError(res, err) {
  if (res.headersSent) {
    log.error({ err: err.message, stack: err.stack }, 'error after headers sent');
    try { res.end(); } catch {}
    return;
  }
  const status = err instanceof AppError ? err.status : 500;
  const code = err instanceof AppError ? err.code : 'internal_error';
  const message = err instanceof AppError ? err.message : 'Internal server error';
  log.error({ err: err.message, code, status, stack: err.stack }, 'request error');
  res.writeHead(status, { 'content-type': 'application/json' });
  res.end(JSON.stringify({ error: { code, message, meta: err.meta } }));
}

const port = config.routerPort;
server.listen(port, () => {
  log.info({ port }, 'router listening');
});

const shutdown = async (signal) => {
  log.info({ signal }, 'shutting down');
  server.close(() => log.info('http closed'));
  await closeRedis();
  await closeDb();
  process.exit(0);
};
process.on('SIGINT', () => shutdown('SIGINT'));
process.on('SIGTERM', () => shutdown('SIGTERM'));
