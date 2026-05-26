// Lightweight HTTP helpers: JSON body parsing, response writers, route table.

import { ValidationError } from '@lazirouter-cloud/shared';

export async function readJson(req, maxBytes = 1 * 1024 * 1024) {
  return new Promise((resolve, reject) => {
    let buf = '';
    req.on('data', c => {
      buf += c;
      if (buf.length > maxBytes) { req.destroy(); reject(new ValidationError('Body too large')); }
    });
    req.on('end', () => {
      if (!buf) return resolve({});
      try { resolve(JSON.parse(buf)); }
      catch { reject(new ValidationError('Invalid JSON body')); }
    });
    req.on('error', reject);
  });
}

export function ok(res, body, status = 200) {
  res.writeHead(status, { 'content-type': 'application/json' });
  res.end(JSON.stringify(body));
}

export function noContent(res) {
  res.writeHead(204);
  res.end();
}

/**
 * Simple route table. Routes are { method, pattern: '/api/foo/:id', handler }.
 * Returns { match, params } or null.
 */
export function matchRoute(routes, method, url) {
  const path = url.split('?')[0];
  for (const r of routes) {
    if (r.method !== method) continue;
    const parts = r.pattern.split('/').filter(Boolean);
    const got = path.split('/').filter(Boolean);
    if (parts.length !== got.length) continue;
    const params = {};
    let okMatch = true;
    for (let i = 0; i < parts.length; i++) {
      if (parts[i].startsWith(':')) params[parts[i].slice(1)] = decodeURIComponent(got[i]);
      else if (parts[i] !== got[i]) { okMatch = false; break; }
    }
    if (okMatch) return { handler: r.handler, params };
  }
  return null;
}

export function requireField(obj, field, type = 'string') {
  if (obj[field] === undefined || obj[field] === null || obj[field] === '') {
    throw new ValidationError(`Missing field: ${field}`);
  }
  if (type && typeof obj[field] !== type) {
    throw new ValidationError(`Field ${field} must be a ${type}`);
  }
  return obj[field];
}
