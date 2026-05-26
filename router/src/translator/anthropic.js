// Anthropic ↔ OpenAI message-format translator.
//
// Inbound:  Anthropic /v1/messages body  → OpenAI /v1/chat/completions body
// Outbound: OpenAI chat-completion response → Anthropic message response
//
// This is the minimum translation that lets Claude Code / Anthropic SDK clients
// talk to an OpenAI-compatible upstream (GLM, DeepSeek, OpenRouter, Vertex...).
// Multi-modal content blocks beyond text are passed through best-effort; tool
// use is supported as JSON. Stream-frame translation lives below.

/* ---------------- Request: Anthropic → OpenAI ---------------- */

export function anthropicToOpenaiRequest(a) {
  const messages = [];
  if (a.system) {
    const sys = Array.isArray(a.system)
      ? a.system.map(b => (b.type === 'text' ? b.text : '')).join('\n')
      : String(a.system);
    if (sys) messages.push({ role: 'system', content: sys });
  }
  for (const m of a.messages || []) {
    messages.push({ role: m.role, content: contentBlocksToOpenai(m.content) });
  }

  const openai = {
    model: a.model,
    messages,
    stream: !!a.stream,
  };
  if (a.max_tokens != null) openai.max_tokens = a.max_tokens;
  if (a.temperature != null) openai.temperature = a.temperature;
  if (a.top_p != null) openai.top_p = a.top_p;
  if (a.stop_sequences) openai.stop = a.stop_sequences;
  if (Array.isArray(a.tools) && a.tools.length) {
    openai.tools = a.tools.map(t => ({
      type: 'function',
      function: { name: t.name, description: t.description, parameters: t.input_schema },
    }));
  }
  return openai;
}

function contentBlocksToOpenai(content) {
  if (typeof content === 'string') return content;
  if (!Array.isArray(content)) return String(content ?? '');
  // Try to render to plain string when possible (most cheap upstreams expect a string)
  const allText = content.every(b => b && b.type === 'text');
  if (allText) return content.map(b => b.text).join('');
  // Fall back to OpenAI multi-part content array
  return content.map(b => {
    if (b.type === 'text') return { type: 'text', text: b.text };
    if (b.type === 'image') return {
      type: 'image_url',
      image_url: b.source?.data
        ? { url: `data:${b.source.media_type};base64,${b.source.data}` }
        : { url: b.source?.url || '' },
    };
    if (b.type === 'tool_use') return { type: 'text', text: `<tool_use:${b.name} ${JSON.stringify(b.input)}>` };
    if (b.type === 'tool_result') return { type: 'text', text: typeof b.content === 'string' ? b.content : JSON.stringify(b.content) };
    return { type: 'text', text: '' };
  });
}

/* ---------------- Response: OpenAI → Anthropic ---------------- */

export function openaiToAnthropicResponse(o, requestedModel) {
  const choice = o.choices?.[0] || {};
  const msg = choice.message || {};
  const content = [];
  if (typeof msg.content === 'string' && msg.content.length) {
    content.push({ type: 'text', text: msg.content });
  }
  if (Array.isArray(msg.tool_calls)) {
    for (const tc of msg.tool_calls) {
      let input = {};
      try { input = tc.function?.arguments ? JSON.parse(tc.function.arguments) : {}; } catch {}
      content.push({
        type: 'tool_use',
        id: tc.id || `toolu_${Math.random().toString(36).slice(2, 10)}`,
        name: tc.function?.name,
        input,
      });
    }
  }
  return {
    id: o.id ? `msg_${o.id}` : `msg_${Math.random().toString(36).slice(2, 12)}`,
    type: 'message',
    role: 'assistant',
    model: requestedModel || o.model,
    content,
    stop_reason: mapFinishReason(choice.finish_reason),
    stop_sequence: null,
    usage: {
      input_tokens:  o.usage?.prompt_tokens     ?? 0,
      output_tokens: o.usage?.completion_tokens ?? 0,
    },
  };
}

function mapFinishReason(r) {
  switch (r) {
    case 'stop':         return 'end_turn';
    case 'length':       return 'max_tokens';
    case 'tool_calls':   return 'tool_use';
    case 'content_filter': return 'stop_sequence';
    default:             return r ? 'end_turn' : null;
  }
}

/* ---------------- Streaming: OpenAI SSE → Anthropic SSE ---------------- */

/**
 * Stateful translator. Construct one per request; feed each upstream SSE frame
 * to `feed()`, and the generator yields zero or more Anthropic-formatted frames.
 *
 * Anthropic stream protocol:
 *   event: message_start          {message: {...empty content...}}
 *   event: content_block_start    {index: 0, content_block: {type:'text', text:''}}
 *   event: content_block_delta    {index: 0, delta: {type:'text_delta', text: '...'}}
 *   event: content_block_stop     {index: 0}
 *   event: message_delta          {delta: {stop_reason, ...}, usage}
 *   event: message_stop
 */
export function createOpenaiToAnthropicStream(requestedModel) {
  const messageId = `msg_${Math.random().toString(36).slice(2, 12)}`;
  let started = false;
  let blockOpen = false;
  let lastUsage = { input_tokens: 0, output_tokens: 0 };

  return {
    *feed(rawFrame) {
      // Each rawFrame is `data: ...\n\n` (or multiple). Process line-by-line.
      const lines = rawFrame.split(/\r?\n/);
      for (const line of lines) {
        if (!line.startsWith('data:')) continue;
        const payload = line.slice(5).trim();
        if (!payload || payload === '[DONE]') continue;
        let obj;
        try { obj = JSON.parse(payload); } catch { continue; }

        if (!started) {
          started = true;
          yield sse('message_start', {
            type: 'message_start',
            message: {
              id: messageId, type: 'message', role: 'assistant',
              content: [], model: requestedModel || obj.model || '',
              stop_reason: null, stop_sequence: null,
              usage: { input_tokens: 0, output_tokens: 0 },
            },
          });
        }

        const choice = obj.choices?.[0];
        const delta = choice?.delta || {};
        if (delta.content) {
          if (!blockOpen) {
            yield sse('content_block_start', {
              type: 'content_block_start',
              index: 0,
              content_block: { type: 'text', text: '' },
            });
            blockOpen = true;
          }
          yield sse('content_block_delta', {
            type: 'content_block_delta',
            index: 0,
            delta: { type: 'text_delta', text: delta.content },
          });
        }
        if (obj.usage) {
          lastUsage = {
            input_tokens:  obj.usage.prompt_tokens     ?? lastUsage.input_tokens,
            output_tokens: obj.usage.completion_tokens ?? lastUsage.output_tokens,
          };
        }
        if (choice?.finish_reason) {
          if (blockOpen) {
            yield sse('content_block_stop', { type: 'content_block_stop', index: 0 });
            blockOpen = false;
          }
          yield sse('message_delta', {
            type: 'message_delta',
            delta: { stop_reason: mapFinishReason(choice.finish_reason), stop_sequence: null },
            usage: lastUsage,
          });
          yield sse('message_stop', { type: 'message_stop' });
        }
      }
    },

    *flush() {
      if (blockOpen) {
        yield sse('content_block_stop', { type: 'content_block_stop', index: 0 });
        blockOpen = false;
      }
      // If upstream ended without finish_reason we still close cleanly
      if (started) {
        yield sse('message_delta', {
          type: 'message_delta',
          delta: { stop_reason: 'end_turn', stop_sequence: null },
          usage: lastUsage,
        });
        yield sse('message_stop', { type: 'message_stop' });
      }
    },

    getUsage() { return lastUsage; },
  };
}

function sse(event, payload) {
  return `event: ${event}\ndata: ${JSON.stringify(payload)}\n\n`;
}
