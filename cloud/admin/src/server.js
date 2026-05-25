// Admin HTTP server. Provides:
//   POST  /auth/signup
//   POST  /auth/login
//   GET   /auth/me
//   GET   /api/keys
//   POST  /api/keys
//   DELETE /api/keys/:id
//   GET   /api/connections
//   POST  /api/connections
//   PATCH /api/connections/:id
//   DELETE /api/connections/:id
//   GET   /api/usage/summary
//   GET   /api/usage/recent

import http from 'node:http';
import { config, logger, AppError, closeRedis, closeDb } from '@9router-cloud/shared';
import { matchRoute } from './lib/http.js';
import { signup, login, me } from './routes/auth.js';
import { listKeys, createKey, revokeKey } from './routes/apiKeys.js';
import { listConnections, createConnection, updateConnection, deleteConnection } from './routes/connections.js';
import { listCombos, createCombo, updateCombo, deleteCombo } from './routes/combos.js';
import { summary as usageSummary, recent as usageRecent } from './routes/usage.js';

const log = logger.child({ svc: 'admin' });

const routes = [
  { method: 'POST',   pattern: '/auth/signup',          handler: signup },
  { method: 'POST',   pattern: '/auth/login',           handler: login },
  { method: 'GET',    pattern: '/auth/me',              handler: me },

  { method: 'GET',    pattern: '/api/keys',             handler: listKeys },
  { method: 'POST',   pattern: '/api/keys',             handler: createKey },
  { method: 'DELETE', pattern: '/api/keys/:id',         handler: revokeKey },

  { method: 'GET',    pattern: '/api/connections',      handler: listConnections },
  { method: 'POST',   pattern: '/api/connections',      handler: createConnection },
  { method: 'PATCH',  pattern: '/api/connections/:id',  handler: updateConnection },
  { method: 'DELETE', pattern: '/api/connections/:id',  handler: deleteConnection },

  { method: 'GET',    pattern: '/api/combos',           handler: listCombos },
  { method: 'POST',   pattern: '/api/combos',           handler: createCombo },
  { method: 'PATCH',  pattern: '/api/combos/:slug',     handler: updateCombo },
  { method: 'DELETE', pattern: '/api/combos/:slug',     handler: deleteCombo },

  { method: 'GET',    pattern: '/api/usage/summary',    handler: usageSummary },
  { method: 'GET',    pattern: '/api/usage/recent',     handler: usageRecent },
];

const server = http.createServer(async (req, res) => {
  const start = Date.now();
  setCors(res);
  if (req.method === 'OPTIONS') { res.writeHead(204); return res.end(); }
  try {
    if (req.method === 'GET' && req.url === '/health') {
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end(JSON.stringify({ ok: true }));
    }
    const m = matchRoute(routes, req.method, req.url);
    if (!m) {
      res.writeHead(404, { 'content-type': 'application/json' });
      return res.end(JSON.stringify({ error: { code: 'not_found' } }));
    }
    await m.handler(req, res, { params: m.params });
  } catch (err) {
    return writeError(res, err);
  } finally {
    log.info({ method: req.method, url: req.url, status: res.statusCode, ms: Date.now() - start }, 'http');
  }
});

function setCors(res) {
  res.setHeader('access-control-allow-origin', '*');
  res.setHeader('access-control-allow-methods', 'GET,POST,PATCH,DELETE,OPTIONS');
  res.setHeader('access-control-allow-headers', 'authorization,content-type');
}

function writeError(res, err) {
  if (res.headersSent) { try { res.end(); } catch {} return; }
  const status = err instanceof AppError ? err.status : 500;
  const code = err instanceof AppError ? err.code : 'internal_error';
  const message = err instanceof AppError ? err.message : 'Internal server error';
  log.error({ err: err.message, code, status }, 'request error');
  res.writeHead(status, { 'content-type': 'application/json' });
  res.end(JSON.stringify({ error: { code, message, meta: err.meta } }));
}

const port = config.adminPort;
server.listen(port, () => log.info({ port }, 'admin listening'));

const shutdown = async (signal) => {
  log.info({ signal }, 'shutting down');
  server.close();
  await closeRedis(); await closeDb();
  process.exit(0);
};
process.on('SIGINT', () => shutdown('SIGINT'));
process.on('SIGTERM', () => shutdown('SIGTERM'));
