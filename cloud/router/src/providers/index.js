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

export function getUpstream(provider) {
  switch (provider) {
    case 'claude':
      return {
        kind: 'anthropic',
        async chat({ credentials, body, signal }) {
          return claudeChat({ credentials, body, signal });
        },
        async *stream({ credentials, body, signal }) {
          for await (const f of claudeStream({ credentials, body, signal })) yield f;
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
