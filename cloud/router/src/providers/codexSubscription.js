// Codex subscription executor: pass an OpenAI Responses-API request through
// to ChatGPT's backend using a chatgpt.com OAuth access_token, with the
// spoof headers + session_id that identify the caller as the official
// codex-cli. Without these, the backend either 401s or returns a stripped
// response.

import crypto from 'node:crypto';
import { ProxyAgent } from 'undici';
import { UpstreamError } from '@9router-cloud/shared';

const BASE_URL = 'https://chatgpt.com/backend-api/codex/responses';

const CODEX_SPOOF_HEADERS = {
  originator: 'codex_cli_rs',
  'user-agent': 'codex-cli/0.133.0 (linux; x64)',
  // OpenAI requires these to recognise the ChatGPT-subscription billing path.
  'openai-beta': 'responses=experimental',
};

// Stable session_id per (tenant, connection) — Codex backend keys conversation
// state and rate limit buckets on this. The first assistant turn establishes
// it; subsequent turns must reuse it. We derive deterministically so the
// router process is stateless.
function deriveSessionId({ tenantId, connectionId }) {
  const h = crypto.createHash('sha256')
    .update(`9r:${tenantId}:${connectionId}`)
    .digest('hex');
  return `sess_${h.slice(0, 12)}`;
}

const dispatcherCache = new Map();
function dispatcherForProxy(proxyUrl) {
  if (!dispatcherCache.has(proxyUrl)) {
    dispatcherCache.set(proxyUrl, new ProxyAgent({ uri: proxyUrl }));
  }
  return dispatcherCache.get(proxyUrl);
}
function resolveDispatcher(connectionMetadata) {
  const cfgProxy = connectionMetadata?.proxy_url;
  if (cfgProxy) return dispatcherForProxy(cfgProxy);
  if (process.env.DISALLOW_ENV_PROXY === '1') return null;
  const env = process.env.HTTPS_PROXY || process.env.https_proxy
    || process.env.ALL_PROXY || process.env.all_proxy;
  return env ? dispatcherForProxy(env) : null;
}

/**
 * Body cloaking required by ChatGPT-subscription tokens calling the codex
 * Responses endpoint. We:
 *   - Force `store: false` (sub tier does not allow stored responses).
 *   - Force `stream: true` (backend only emits SSE).
 *   - Strip OpenAI-Chat-Completions-only fields the backend rejects
 *     (temperature, top_p, max_tokens, etc.) — but only if they're present.
 *   - Inject a default `reasoning` envelope when caller didn't ask.
 */
function cloakBody(body) {
  const out = { ...body, store: false, stream: true };
  // Strip fields not supported by codex backend
  for (const k of [
    'temperature', 'top_p', 'frequency_penalty', 'presence_penalty',
    'logprobs', 'top_logprobs', 'n', 'seed',
    'max_tokens', 'max_completion_tokens', 'max_output_tokens',
    'user', 'metadata', 'stream_options', 'prompt_cache_retention',
    'safety_identifier',
  ]) {
    if (k in out) delete out[k];
  }
  if (!out.reasoning) {
    out.reasoning = { effort: 'low', summary: 'auto' };
  } else if (!out.reasoning.summary) {
    out.reasoning.summary = 'auto';
  }
  if (out.reasoning?.effort && out.reasoning.effort !== 'none' && !out.include) {
    out.include = ['reasoning.encrypted_content'];
  }
  return out;
}

function buildOpts({ metadata, accessToken, sessionId, body, signal, accept }) {
  const dispatcher = resolveDispatcher(metadata);
  return {
    method: 'POST',
    headers: {
      ...CODEX_SPOOF_HEADERS,
      session_id: sessionId,
      authorization: `Bearer ${accessToken}`,
      'content-type': 'application/json',
      ...(accept ? { accept } : {}),
    },
    body,
    signal,
    ...(dispatcher ? { dispatcher } : {}),
  };
}

/**
 * Non-streaming call. Codex backend doesn't support stream=false; we set
 * stream=true on the wire and aggregate frames into a single response shape
 * the caller can serialize. For our /v1/responses route, callers nearly
 * always want stream; this path exists for completeness.
 */
export async function responsesCall({ credentials, metadata, body, signal, sessionContext }) {
  const sessionId = deriveSessionId(sessionContext);
  const finalBody = JSON.stringify(cloakBody(body));
  const res = await fetch(BASE_URL,
    buildOpts({ metadata, accessToken: credentials.access_token, sessionId, body: finalBody, signal }));
  const text = await res.text();
  if (!res.ok) {
    throw new UpstreamError(`Codex ${res.status}: ${text.slice(0, 500)}`,
      { status_code: res.status, body: text });
  }
  return text;
}

/** Streaming call — yields raw SSE frames suitable for forwarding. */
export async function* streamResponsesCall({ credentials, metadata, body, signal, sessionContext }) {
  const sessionId = deriveSessionId(sessionContext);
  const finalBody = JSON.stringify(cloakBody(body));
  const res = await fetch(BASE_URL,
    buildOpts({
      metadata, accessToken: credentials.access_token, sessionId,
      body: finalBody, signal, accept: 'text/event-stream',
    }));
  if (!res.ok) {
    const text = await res.text();
    throw new UpstreamError(`Codex ${res.status}: ${text.slice(0, 500)}`,
      { status_code: res.status, body: text });
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let idx;
    while ((idx = buf.indexOf('\n\n')) !== -1) {
      yield buf.slice(0, idx + 2);
      buf = buf.slice(idx + 2);
    }
  }
  if (buf.length) yield buf;
}
