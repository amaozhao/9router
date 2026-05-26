// Provider dispatcher. Given a `provider` string from a tenant connection,
// returns the right upstream client. Each client exposes:
//   { kind: 'openai' | 'anthropic', chat(opts), stream(opts) }
// where `chat` is non-streaming and `stream` is an async generator yielding
// raw SSE frames in the provider's native protocol.
//
// The router's protocol handlers (chatCompletions / messages) consult `kind`
// to decide whether translation is needed.

import { chatCompletion as openaiChat, streamChatCompletion as openaiStream } from './openaiCompatible.js';
import { chatMessages as claudeChat, streamChatMessages as claudeStream } from './claudeSubscription.js';
import { responsesCall as codexCall, streamResponsesCall as codexStream } from './codexSubscription.js';

export function getUpstream(provider) {
  switch (provider) {
    case 'claude':
      return {
        kind: 'anthropic',
        async chat({ credentials, metadata, body, signal }) {
          return claudeChat({ credentials, metadata, body, signal });
        },
        async *stream({ credentials, metadata, body, signal }) {
          for await (const f of claudeStream({ credentials, metadata, body, signal })) yield f;
        },
      };
    case 'codex':
      return {
        kind: 'responses',
        async chat({ credentials, metadata, body, signal, sessionContext }) {
          return codexCall({ credentials, metadata, body, signal, sessionContext });
        },
        async *stream({ credentials, metadata, body, signal, sessionContext }) {
          for await (const f of codexStream({ credentials, metadata, body, signal, sessionContext })) yield f;
        },
      };
    // Everything else speaks OpenAI Chat Completions; base_url + api_key on
    // the connection.metadata / credentials select the actual upstream.
    default:
      return {
        kind: 'openai',
        async chat({ credentials, baseUrl, body, signal }) {
          return openaiChat({ baseUrl, apiKey: credentials.api_key, body, signal });
        },
        async *stream({ credentials, baseUrl, body, signal }) {
          for await (const f of openaiStream({ baseUrl, apiKey: credentials.api_key, body, signal })) yield f;
        },
      };
  }
}

/** Whether the upstream expects Anthropic shape natively (no translation). */
export function isAnthropicNative(provider) {
  return provider === 'claude';
}

/** Whether the upstream expects OpenAI Responses-API shape natively. */
export function isResponsesNative(provider) {
  return provider === 'codex';
}
