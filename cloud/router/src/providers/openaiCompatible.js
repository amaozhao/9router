// Minimal OpenAI-compatible upstream client. Targets any provider that speaks
// the OpenAI Chat Completions API: GLM (zhipuai), DeepSeek, MiniMax,
// official OpenAI, OpenRouter, Together, Groq, etc.
//
// The full lazirouter open-sse executor catalog (13 subclasses, Claude/Cursor/
// Codex/Antigravity/Gemini etc.) plugs in here in Phase 3; for Phase 1 we keep
// the surface deliberately small so we can prove the end-to-end loop.

import { request as undiciRequest } from 'undici';
import { UpstreamError } from '@lazirouter-cloud/shared';

/**
 * Stream a chat completion from an OpenAI-compatible upstream.
 * Yields raw SSE event strings (already prefixed with "data: ...\n\n") so the
 * server route can forward them verbatim.
 * Resolves with usage info collected from the stream tail.
 */
export async function* streamChatCompletion({ baseUrl, apiKey, body, signal }) {
  const url = trimSlash(baseUrl) + '/v1/chat/completions';
  const headers = {
    'authorization': `Bearer ${apiKey}`,
    'content-type': 'application/json',
    'accept': 'text/event-stream',
  };
  const payload = { ...body, stream: true };
  const res = await undiciRequest(url, {
    method: 'POST',
    headers,
    body: JSON.stringify(payload),
    signal,
  });
  if (res.statusCode >= 400) {
    const text = await res.body.text();
    throw new UpstreamError(`Upstream ${res.statusCode}: ${text.slice(0, 500)}`,
      { status_code: res.statusCode, body: text });
  }
  let buf = '';
  for await (const chunk of res.body) {
    buf += chunk.toString('utf8');
    // SSE events are separated by blank lines
    let idx;
    while ((idx = buf.indexOf('\n\n')) !== -1) {
      const frame = buf.slice(0, idx + 2);
      buf = buf.slice(idx + 2);
      yield frame;
    }
  }
  if (buf.length) yield buf;
}

/**
 * Non-stream chat completion. Returns the full JSON response.
 */
export async function chatCompletion({ baseUrl, apiKey, body, signal }) {
  const url = trimSlash(baseUrl) + '/v1/chat/completions';
  const headers = {
    'authorization': `Bearer ${apiKey}`,
    'content-type': 'application/json',
  };
  const payload = { ...body, stream: false };
  const res = await undiciRequest(url, {
    method: 'POST',
    headers,
    body: JSON.stringify(payload),
    signal,
  });
  const text = await res.body.text();
  if (res.statusCode >= 400) {
    throw new UpstreamError(`Upstream ${res.statusCode}: ${text.slice(0, 500)}`,
      { status_code: res.statusCode, body: text });
  }
  try {
    return JSON.parse(text);
  } catch (e) {
    throw new UpstreamError('Upstream returned non-JSON', { body: text.slice(0, 500) });
  }
}

/**
 * Embeddings — POST {baseUrl}/v1/embeddings with OpenAI-shape body.
 * Returns { data: [{embedding, index}], model, usage } verbatim.
 */
export async function embed({ baseUrl, apiKey, body, signal }) {
  const url = trimSlash(baseUrl) + '/v1/embeddings';
  const res = await undiciRequest(url, {
    method: 'POST',
    headers: { 'authorization': `Bearer ${apiKey}`, 'content-type': 'application/json' },
    body: JSON.stringify(body),
    signal,
  });
  const text = await res.body.text();
  if (res.statusCode >= 400) {
    throw new UpstreamError(`Upstream ${res.statusCode}: ${text.slice(0, 500)}`,
      { status_code: res.statusCode, body: text });
  }
  try { return JSON.parse(text); }
  catch { throw new UpstreamError('Upstream returned non-JSON', { body: text.slice(0, 500) }); }
}

function trimSlash(s) { return s.endsWith('/') ? s.slice(0, -1) : s; }
