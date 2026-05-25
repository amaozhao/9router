#!/usr/bin/env node
// Echo upstream that reports which instance answered, so we can prove round-robin.
// Run multiple instances with LABEL=A PORT=31901, LABEL=B PORT=31902, etc.
import http from 'node:http';
const PORT = Number(process.env.LABEL_PORT || 31901);
const LABEL = process.env.LABEL || 'A';
http.createServer(async (req, res) => {
  if (req.url === '/') { res.writeHead(200); return res.end(`labeled-up ${LABEL}`); }
  if (req.method !== 'POST') { res.writeHead(404); return res.end(); }
  res.writeHead(200, { 'content-type': 'application/json' });
  res.end(JSON.stringify({
    id: `cmpl-${LABEL}`,
    object: 'chat.completion',
    model: 'labeled',
    choices: [{ index: 0, message: { role: 'assistant', content: `served by ${LABEL}` }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
  }));
}).listen(PORT, () => console.log(`labeled-up ${LABEL} :${PORT}`));
