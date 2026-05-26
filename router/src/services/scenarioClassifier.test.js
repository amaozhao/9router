import { test } from 'node:test';
import assert from 'node:assert/strict';
import { classifyScenario, SCENARIOS } from './scenarioClassifier.js';

test('web: tool with name matching /search|web|browse/i wins', () => {
  const body = { tools: [{ type: 'function', function: { name: 'web_search' } }], messages: [] };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.WEB);
});

test('web: anthropic-shape tool with name browse_url wins', () => {
  const body = { tools: [{ name: 'browse_url' }], messages: [] };
  assert.equal(classifyScenario(body, 'anthropic'), SCENARIOS.WEB);
});

test('tool_use: non-empty tools without web names', () => {
  const body = { tools: [{ type: 'function', function: { name: 'get_weather' } }], messages: [] };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.TOOL_USE);
});

test('vision: openai image_url part', () => {
  const body = {
    messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: 'http://x' } }] }],
  };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.VISION);
});

test('vision: anthropic image part with image/png media_type', () => {
  const body = {
    messages: [{ role: 'user', content: [
      { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'xxx' } },
    ] }],
  };
  assert.equal(classifyScenario(body, 'anthropic'), SCENARIOS.VISION);
});

test('long_context: char count >256000 (≈64k tokens)', () => {
  const big = 'a'.repeat(300_000);
  const body = { messages: [{ role: 'user', content: big }] };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.LONG_CONTEXT);
});

test('long_context: anthropic system prompt counted too', () => {
  const body = { system: 'a'.repeat(300_000), messages: [{ role: 'user', content: 'hi' }] };
  assert.equal(classifyScenario(body, 'anthropic'), SCENARIOS.LONG_CONTEXT);
});

test('think: reasoning_effort=high', () => {
  const body = { reasoning_effort: 'high', messages: [{ role: 'user', content: 'solve' }] };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.THINK);
});

test('think: thinking.enabled=true', () => {
  const body = { thinking: { enabled: true }, messages: [{ role: 'user', content: 'solve' }] };
  assert.equal(classifyScenario(body, 'anthropic'), SCENARIOS.THINK);
});

test('default: plain short text', () => {
  const body = { messages: [{ role: 'user', content: 'hi' }] };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.DEFAULT);
});

test('web beats tool_use when both present', () => {
  const body = {
    tools: [
      { type: 'function', function: { name: 'get_weather' } },
      { type: 'function', function: { name: 'web_search' } },
    ],
    messages: [],
  };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.WEB);
});

test('tool_use beats vision when both present', () => {
  const body = {
    tools: [{ type: 'function', function: { name: 'get_weather' } }],
    messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: 'x' } }] }],
  };
  assert.equal(classifyScenario(body, 'openai'), SCENARIOS.TOOL_USE);
});
