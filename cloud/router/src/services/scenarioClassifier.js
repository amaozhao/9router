// Pure-function request classifier. No I/O. Identical contract for both
// OpenAI-shape (chat/completions) and Anthropic-shape (messages) bodies.
//
// Priority order (first match wins):
//   1. web        - any tool name matches /search|web|browse/i
//   2. tool_use   - any tools entry at all
//   3. vision     - any message content part is an image
//   4. long_context - rough estimate prompt_tokens > 64000 (chars/4)
//   5. think      - reasoning_effort high/max OR thinking.enabled
//   6. default    - fallback

export const SCENARIOS = Object.freeze({
  DEFAULT: 'default',
  THINK: 'think',
  LONG_CONTEXT: 'long_context',
  VISION: 'vision',
  TOOL_USE: 'tool_use',
  WEB: 'web',
});

const LONG_CONTEXT_CHAR_THRESHOLD = 64_000 * 4; // ~64k tokens via chars/4 estimate
const WEB_TOOL_NAME_RE = /search|web|browse/i;

export function classifyScenario(body, protocol) {
  if (hasWebTool(body)) return SCENARIOS.WEB;
  if (hasTools(body)) return SCENARIOS.TOOL_USE;
  if (hasImage(body)) return SCENARIOS.VISION;
  if (estimateChars(body) > LONG_CONTEXT_CHAR_THRESHOLD) return SCENARIOS.LONG_CONTEXT;
  if (isThink(body)) return SCENARIOS.THINK;
  return SCENARIOS.DEFAULT;
}

function hasTools(body) {
  return Array.isArray(body?.tools) && body.tools.length > 0;
}

function hasWebTool(body) {
  if (!Array.isArray(body?.tools)) return false;
  return body.tools.some(t => {
    if (!t) return false;
    const name = t?.function?.name ?? t?.name;
    return typeof name === 'string' && WEB_TOOL_NAME_RE.test(name);
  });
}

function hasImage(body) {
  const messages = body?.messages;
  if (!Array.isArray(messages)) return false;
  for (const msg of messages) {
    const content = msg?.content;
    if (!Array.isArray(content)) continue;
    for (const part of content) {
      if (!part) continue;
      if (part.type === 'image' || part.type === 'image_url') return true;
      const mt = part?.source?.media_type;
      if (typeof mt === 'string' && mt.startsWith('image/')) return true;
    }
  }
  return false;
}

function estimateChars(body) {
  let total = 0;
  if (typeof body?.system === 'string') total += body.system.length;
  const messages = body?.messages;
  if (Array.isArray(messages)) {
    for (const m of messages) {
      const c = m?.content;
      if (typeof c === 'string') total += c.length;
      else total += JSON.stringify(c ?? '').length;
    }
  }
  return total;
}

function isThink(body) {
  if (body?.reasoning_effort === 'high' || body?.reasoning_effort === 'max') return true;
  if (body?.thinking && body.thinking.enabled === true) return true;
  return false;
}
