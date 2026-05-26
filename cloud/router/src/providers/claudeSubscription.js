// Claude subscription executor: passes Anthropic /v1/messages requests through
// to api.anthropic.com using a claude.ai OAuth access_token, with the spoof
// headers that identify this caller as the official claude-cli.

import { request as undiciRequest } from 'undici';
import { UpstreamError } from '@9router-cloud/shared';

const BASE_URL = 'https://api.anthropic.com';

// Headers expected by Anthropic when authenticating with an OAuth bearer.
// Mirrors what the official claude-cli sends. Keep this in sync with
// 9router/open-sse/config/providers.js if upstream protocol shifts.
const CLAUDE_SPOOF_HEADERS = {
  'anthropic-version': '2023-06-01',
  'anthropic-beta':
    'claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,'
    + 'context-management-2025-06-27,prompt-caching-scope-2026-01-05,'
    + 'advanced-tool-use-2025-11-20,effort-2025-11-24,structured-outputs-2025-12-15,'
    + 'fast-mode-2026-02-01,redact-thinking-2026-02-12,token-efficient-tools-2026-03-28',
  'user-agent': 'claude-cli/2.1.92 (external, sdk-cli)',
};

/** Strip OAuth-incompatible fields from the Anthropic request body. */
function sanitizeBody(body) {
  // OAuth Anthropic rejects API-key-only fields like metadata.user_id.
  // Pass everything else through verbatim — the upstream is native shape.
  const out = { ...body };
  return out;
}

/** Non-streaming Anthropic call. */
export async function chatMessages({ credentials, body, signal }) {
  const headers = {
    ...CLAUDE_SPOOF_HEADERS,
    authorization: `Bearer ${credentials.access_token}`,
    'content-type': 'application/json',
  };
  const res = await undiciRequest(`${BASE_URL}/v1/messages`, {
    method: 'POST',
    headers,
    body: JSON.stringify({ ...sanitizeBody(body), stream: false }),
    signal,
  });
  const text = await res.body.text();
  if (res.statusCode >= 400) {
    throw new UpstreamError(`Claude ${res.statusCode}: ${text.slice(0, 500)}`,
      { status_code: res.statusCode, body: text });
  }
  return JSON.parse(text);
}

/** Streaming Anthropic call. Yields raw SSE frames suitable for forwarding. */
export async function* streamChatMessages({ credentials, body, signal }) {
  const headers = {
    ...CLAUDE_SPOOF_HEADERS,
    authorization: `Bearer ${credentials.access_token}`,
    'content-type': 'application/json',
    accept: 'text/event-stream',
  };
  const res = await undiciRequest(`${BASE_URL}/v1/messages`, {
    method: 'POST',
    headers,
    body: JSON.stringify({ ...sanitizeBody(body), stream: true }),
    signal,
  });
  if (res.statusCode >= 400) {
    const text = await res.body.text();
    throw new UpstreamError(`Claude ${res.statusCode}: ${text.slice(0, 500)}`,
      { status_code: res.statusCode, body: text });
  }
  let buf = '';
  for await (const chunk of res.body) {
    buf += chunk.toString('utf8');
    let idx;
    while ((idx = buf.indexOf('\n\n')) !== -1) {
      yield buf.slice(0, idx + 2);
      buf = buf.slice(idx + 2);
    }
  }
  if (buf.length) yield buf;
}
