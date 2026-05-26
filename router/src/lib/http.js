// Shared helpers for router HTTP routes.

import { ValidationError } from '@lazirouter-cloud/shared';

const MAX_BODY_BYTES = 5 * 1024 * 1024;

/**
 * Read a JSON body from a node:http request. Resolves with {} on empty body,
 * rejects with ValidationError on parse failure or body > 5 MiB.
 */
export function readJson(req) {
  return new Promise((resolve, reject) => {
    let buf = '';
    req.on('data', c => {
      buf += c;
      if (buf.length > MAX_BODY_BYTES) {
        req.destroy();
        reject(new ValidationError('Body too large'));
      }
    });
    req.on('end', () => {
      if (!buf) return resolve({});
      try { resolve(JSON.parse(buf)); }
      catch { reject(new ValidationError('Invalid JSON body')); }
    });
    req.on('error', reject);
  });
}

/**
 * Default upstream base URL per provider, used when a connection's metadata
 * does not override it. Returns null for providers without a public default.
 */
export function defaultBaseUrl(provider) {
  switch (provider) {
    case 'openai':    return 'https://api.openai.com';
    case 'gemini':    return 'https://generativelanguage.googleapis.com/v1beta/openai';
    case 'glm':       return 'https://open.bigmodel.cn/api/paas/v4';
    case 'deepseek':  return 'https://api.deepseek.com';
    case 'minimax':   return 'https://api.minimaxi.com';
    case 'anthropic': return 'https://api.anthropic.com';
    default: return null;
  }
}
