// Claude subscription executor: passes Anthropic /v1/messages requests through
// to api.anthropic.com using a claude.ai OAuth access_token, with the spoof
// headers AND request-body cloaking that identify the caller as the
// official claude-cli. Anthropic returns 403 "Request not allowed" if the
// body looks like a non-claude-cli client.

import crypto from 'node:crypto';
import { ProxyAgent } from 'undici';
import { UpstreamError } from '@9router-cloud/shared';

// Anthropic's gateway TLS-fingerprints Node clients (JA3/JA4) and returns
// 403 "Request not allowed" for any direct Node HTTP attempt — undici,
// fetch, node:https, node:http2 all fail. curl and Python urllib succeed.
// This is an Anthropic-side decision; the upstream 9router project hits the
// same wall and resolves it the same way: route outbound through an HTTP
// forward proxy that terminates Node's TLS and reconnects with a different
// stack.
//
// Mirroring 9router's proxyAwareFetch design:
//   1. Per-connection proxy_url (set in connection.metadata.proxy_url)
//      — the production path: each tenant's connection chooses its own
//      egress, can pin region, can route via mitmproxy for audit, etc.
//   2. HTTPS_PROXY env — dev-only fallback; disable in prod by setting
//      DISALLOW_ENV_PROXY=1.

const dispatcherCache = new Map();

function dispatcherForProxy(proxyUrl) {
  if (!dispatcherCache.has(proxyUrl)) {
    dispatcherCache.set(proxyUrl, new ProxyAgent({ uri: proxyUrl }));
  }
  return dispatcherCache.get(proxyUrl);
}

/**
 * Pick the right dispatcher for a connection.
 *   connection.metadata.proxy_url wins (per-tenant config),
 *   then HTTPS_PROXY env (dev fallback, unless DISALLOW_ENV_PROXY=1).
 */
function resolveDispatcher(connectionMetadata) {
  const cfgProxy = connectionMetadata?.proxy_url;
  if (cfgProxy) return dispatcherForProxy(cfgProxy);
  if (process.env.DISALLOW_ENV_PROXY === '1') return null;
  const envProxy = process.env.HTTPS_PROXY || process.env.https_proxy
    || process.env.ALL_PROXY || process.env.all_proxy;
  return envProxy ? dispatcherForProxy(envProxy) : null;
}

const BASE_URL = 'https://api.anthropic.com';
const CLAUDE_VERSION = '2.1.92';
const CC_ENTRYPOINT = 'sdk-cli';

const CLAUDE_SPOOF_HEADERS = {
  'anthropic-version': '2023-06-01',
  'anthropic-beta':
    'claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,'
    + 'context-management-2025-06-27,prompt-caching-scope-2026-01-05,'
    + 'advanced-tool-use-2025-11-20,effort-2025-11-24,structured-outputs-2025-12-15,'
    + 'fast-mode-2026-02-01,redact-thinking-2026-02-12,token-efficient-tools-2026-03-28',
  'user-agent': 'claude-cli/2.1.92 (external, sdk-cli)',
  // Anthropic's gateway fingerprints the request shape; without these two
  // headers undici's outbound looks suspicious and we get 403 'Request not
  // allowed' even when the body cloaking is correct.
  'accept': 'application/json',
  'accept-language': '*',
};

function billingHeader(body) {
  const content = JSON.stringify(body);
  const cch = crypto.createHash('sha256').update(content).digest('hex').slice(0, 5);
  const buildHash = crypto.randomBytes(2).toString('hex').slice(0, 3);
  return `x-anthropic-billing-header: cc_version=${CLAUDE_VERSION}.${buildHash}; cc_entrypoint=${CC_ENTRYPOINT}; cch=${cch};`;
}

function fakeUserId() {
  const device = crypto.randomBytes(32).toString('hex');
  const acct   = crypto.randomUUID();
  const sess   = crypto.randomUUID();
  return `{"device_id":"${device}","account_uuid":"${acct}","session_id":"${sess}"}`;
}

/**
 * Body cloaking required by sk-ant-oat-* tokens:
 *   1. Prepend a billing-header text block to `system`.
 *   2. Inject metadata.user_id with the device/account/session triple.
 * No-op for non-OAuth tokens (e.g. sk-ant-api03-* personal API keys).
 */
function cloakBody(body, accessToken) {
  if (!accessToken || !accessToken.includes('sk-ant-oat')) return body;
  const out = { ...body };
  const billingBlock = { type: 'text', text: billingHeader(body) };
  if (Array.isArray(out.system)) {
    if (!out.system[0]?.text?.startsWith('x-anthropic-billing-header:')) {
      out.system = [billingBlock, ...out.system];
    }
  } else if (typeof out.system === 'string') {
    out.system = [billingBlock, { type: 'text', text: out.system }];
  } else {
    out.system = [billingBlock];
  }
  if (!out.metadata?.user_id) {
    out.metadata = { ...(out.metadata || {}), user_id: fakeUserId() };
  }
  return out;
}

function buildOpts(connectionMetadata, headers, body, signal, accept) {
  const dispatcher = resolveDispatcher(connectionMetadata);
  const finalHeaders = { ...CLAUDE_SPOOF_HEADERS, ...headers };
  if (accept) finalHeaders.Accept = accept;
  return {
    method: 'POST', headers: finalHeaders, body, signal,
    ...(dispatcher ? { dispatcher } : {}),
  };
}

/** Non-streaming Anthropic call. */
export async function chatMessages({ credentials, metadata, body, signal }) {
  const cloaked = cloakBody(body, credentials.access_token);
  const finalBody = JSON.stringify({ ...cloaked, stream: false });
  const headers = {
    Authorization: `Bearer ${credentials.access_token}`,
    'Content-Type': 'application/json',
  };
  const res = await fetch(`${BASE_URL}/v1/messages`,
    buildOpts(metadata, headers, finalBody, signal));
  const text = await res.text();
  if (!res.ok) {
    throw new UpstreamError(`Claude ${res.status}: ${text.slice(0, 500)}`,
      { status_code: res.status, body: text });
  }
  return JSON.parse(text);
}

/** Streaming Anthropic call. Yields raw SSE frames suitable for forwarding. */
export async function* streamChatMessages({ credentials, metadata, body, signal }) {
  const cloaked = cloakBody(body, credentials.access_token);
  const finalBody = JSON.stringify({ ...cloaked, stream: true });
  const headers = {
    Authorization: `Bearer ${credentials.access_token}`,
    'Content-Type': 'application/json',
  };
  const res = await fetch(`${BASE_URL}/v1/messages`,
    buildOpts(metadata, headers, finalBody, signal, 'text/event-stream'));
  if (!res.ok) {
    const text = await res.text();
    throw new UpstreamError(`Claude ${res.status}: ${text.slice(0, 500)}`,
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
