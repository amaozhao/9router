#!/usr/bin/env node
// Mock OpenAI-compatible upstream for integration tests. Listens on :31999.
// Responds to /v1/chat/completions with a deterministic answer (stream + non-stream).

import http from 'node:http';

const PORT = Number(process.env.FAKE_UPSTREAM_PORT || 31999);

const server = http.createServer(async (req, res) => {
  if (req.method === 'POST' && req.url === '/v1/chat/completions') {
    const body = await readJson(req);
    const auth = req.headers['authorization'] || '';
    if (!auth.startsWith('Bearer fake-upstream-key')) {
      res.writeHead(401, { 'content-type': 'application/json' });
      return res.end(JSON.stringify({ error: 'bad upstream key' }));
    }
    const userMsg = body.messages?.[body.messages.length - 1]?.content || '';
    const replyText = `pong: ${typeof userMsg === 'string' ? userMsg.slice(0, 50) : 'hi'}`;
    if (body.stream) {
      res.writeHead(200, {
        'content-type': 'text/event-stream',
        'cache-control': 'no-cache',
      });
      const chunks = [
        chunk(0, { role: 'assistant' }),
        chunk(0, { content: replyText.slice(0, 5) }),
        chunk(0, { content: replyText.slice(5) }),
        finalChunk(),
      ];
      for (const c of chunks) {
        res.write(`data: ${JSON.stringify(c)}\n\n`);
        await sleep(15);
      }
      // Final usage frame
      res.write(`data: ${JSON.stringify({
        id: 'cmpl-fake',
        object: 'chat.completion.chunk',
        usage: { prompt_tokens: 4, completion_tokens: 6, total_tokens: 10 },
      })}\n\n`);
      res.write('data: [DONE]\n\n');
      return res.end();
    }
    res.writeHead(200, { 'content-type': 'application/json' });
    return res.end(JSON.stringify({
      id: 'cmpl-fake',
      object: 'chat.completion',
      model: body.model || 'mock-model-1',
      choices: [{ index: 0, message: { role: 'assistant', content: replyText }, finish_reason: 'stop' }],
      usage: { prompt_tokens: 4, completion_tokens: 6, total_tokens: 10 },
    }));
  }
  if (req.method === 'GET' && req.url === '/') {
    res.writeHead(200, { 'content-type': 'application/json' });
    return res.end(JSON.stringify({ ok: true, name: 'fake-upstream' }));
  }
  res.writeHead(404); res.end();
});

server.listen(PORT, () => console.log(`fake-upstream :${PORT}`));

function chunk(i, delta) {
  return { id: 'cmpl-fake', object: 'chat.completion.chunk', choices: [{ index: i, delta, finish_reason: null }] };
}
function finalChunk() {
  return { id: 'cmpl-fake', object: 'chat.completion.chunk', choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] };
}
function readJson(req) {
  return new Promise((resolve, reject) => {
    let buf = '';
    req.on('data', c => buf += c);
    req.on('end', () => { try { resolve(buf ? JSON.parse(buf) : {}); } catch (e) { reject(e); } });
    req.on('error', reject);
  });
}
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
