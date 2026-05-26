#!/usr/bin/env node
// Mock OAuth2 + PKCE provider for integration tests.
//   GET  /authorize  → 302 to redirect_uri with code=...&state=...
//   POST /token      → exchanges code for tokens
//
// Stores pending codes in memory so we can validate code_verifier against the
// original code_challenge.

import http from 'node:http';
import crypto from 'node:crypto';
import { URL } from 'node:url';

const PORT = Number(process.env.MOCK_OAUTH_PORT || 31995);
const pending = new Map(); // code → { challenge, redirect_uri, client_id }

http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://localhost:${PORT}`);

  if (req.method === 'GET' && url.pathname === '/authorize') {
    const challenge = url.searchParams.get('code_challenge');
    const method = url.searchParams.get('code_challenge_method');
    const state = url.searchParams.get('state');
    const redirectUri = url.searchParams.get('redirect_uri');
    const clientId = url.searchParams.get('client_id');
    if (!challenge || method !== 'S256') {
      res.writeHead(400); return res.end('expected PKCE S256');
    }
    const code = crypto.randomBytes(12).toString('hex');
    pending.set(code, { challenge, redirect_uri: redirectUri, client_id: clientId });
    const next = new URL(redirectUri);
    next.searchParams.set('code', code);
    next.searchParams.set('state', state);
    res.writeHead(302, { location: next.toString() });
    return res.end();
  }

  if (req.method === 'POST' && url.pathname === '/token') {
    let body = ''; for await (const c of req) body += c;
    const params = new URLSearchParams(body);
    const grant = params.get('grant_type');
    if (grant === 'authorization_code') {
      const code = params.get('code');
      const verifier = params.get('code_verifier');
      const meta = pending.get(code);
      if (!meta) { res.writeHead(400); return res.end(JSON.stringify({ error: 'invalid_code' })); }
      pending.delete(code);
      const expected = crypto.createHash('sha256').update(verifier).digest('base64url');
      if (expected !== meta.challenge) {
        res.writeHead(400); return res.end(JSON.stringify({ error: 'pkce_mismatch' }));
      }
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end(JSON.stringify({
        access_token: 'mock-access-' + Date.now(),
        refresh_token: 'mock-refresh-' + Date.now(),
        expires_in: 3600,
        token_type: 'Bearer',
        scope: 'mock',
      }));
    }
    if (grant === 'refresh_token') {
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end(JSON.stringify({
        access_token: 'mock-rotated-' + Date.now(),
        refresh_token: params.get('refresh_token'),
        expires_in: 3600,
        token_type: 'Bearer',
      }));
    }
    res.writeHead(400);
    return res.end(JSON.stringify({ error: 'unsupported_grant_type' }));
  }

  res.writeHead(404); res.end();
}).listen(PORT, () => console.log(`mock-oauth-provider :${PORT}`));
