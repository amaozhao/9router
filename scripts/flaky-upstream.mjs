#!/usr/bin/env node
// Always-500 upstream — used to verify combo fallback semantics.
import http from 'node:http';
const PORT = Number(process.env.FLAKY_UPSTREAM_PORT || 31998);
http.createServer((req, res) => {
  if (req.url === '/') { res.writeHead(200); return res.end('flaky-up'); }
  res.writeHead(500, { 'content-type': 'application/json' });
  res.end(JSON.stringify({ error: 'simulated upstream failure', endpoint: req.url }));
}).listen(PORT, () => console.log(`flaky-upstream :${PORT}`));
