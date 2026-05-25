// Reference token-refresher plugin: OpenAI / OAuth2 standard refresh_token flow.
//
// Expected credentials shape:
//   {
//     access_token: 'sk-...',
//     refresh_token: '...',
//     token_endpoint: 'https://...',  // provider-specific
//     client_id: '...',
//     client_secret?: '...',          // public clients omit this
//   }
//
// The same plugin shape works for Codex, Cursor, GitHub Copilot, etc. — just
// register the provider key to point at this implementation, parameterized
// by the token_endpoint in credentials.

import { request } from 'undici';

export async function refreshOpenAiOAuth(credentials, { tenantId, connectionId }) {
  if (!credentials.refresh_token) throw new Error('missing refresh_token');
  if (!credentials.token_endpoint) throw new Error('missing token_endpoint');

  const params = new URLSearchParams();
  params.set('grant_type', 'refresh_token');
  params.set('refresh_token', credentials.refresh_token);
  if (credentials.client_id) params.set('client_id', credentials.client_id);
  if (credentials.client_secret) params.set('client_secret', credentials.client_secret);

  const res = await request(credentials.token_endpoint, {
    method: 'POST',
    headers: { 'content-type': 'application/x-www-form-urlencoded' },
    body: params.toString(),
  });
  const text = await res.body.text();
  if (res.statusCode >= 400) {
    throw new Error(`refresh ${res.statusCode}: ${text.slice(0, 200)}`);
  }
  const data = JSON.parse(text);
  const expiresInSec = Number(data.expires_in) || 3600;
  const expiresAt = new Date(Date.now() + expiresInSec * 1000).toISOString();
  return {
    credentials: {
      ...credentials,
      access_token: data.access_token,
      // Many IdPs rotate the refresh token; keep the new one if returned.
      refresh_token: data.refresh_token || credentials.refresh_token,
    },
    expiresAt,
    meta: { last_refresh_at: new Date().toISOString(), last_refresh_status: 'ok' },
  };
}
