// claude.ai OAuth refresher. claude.ai/Anthropic accept a JSON body on the
// token endpoint (not form-urlencoded). Credentials shape mirrors what the
// admin OAuth callback persisted.

import { request } from 'undici';

const CLAUDE_TOKEN_URL  = 'https://api.anthropic.com/v1/oauth/token';
const CLAUDE_CLIENT_ID  = '9d1c250a-e61b-44d9-88ed-5944d1962f5e';

export async function refreshClaudeOAuth(credentials) {
  if (!credentials.refresh_token) throw new Error('missing refresh_token');
  const payload = {
    grant_type: 'refresh_token',
    refresh_token: credentials.refresh_token,
    client_id: credentials.client_id || CLAUDE_CLIENT_ID,
  };
  const res = await request(CLAUDE_TOKEN_URL, {
    method: 'POST',
    headers: { 'content-type': 'application/json', accept: 'application/json' },
    body: JSON.stringify(payload),
  });
  const text = await res.body.text();
  if (res.statusCode >= 400) {
    throw new Error(`refresh ${res.statusCode}: ${text.slice(0, 200)}`);
  }
  const data = JSON.parse(text);
  const expiresInSec = Number(data.expires_in) || 3600;
  return {
    credentials: {
      ...credentials,
      access_token: data.access_token,
      refresh_token: data.refresh_token || credentials.refresh_token,
      token_endpoint: CLAUDE_TOKEN_URL,
      client_id: CLAUDE_CLIENT_ID,
    },
    expiresAt: new Date(Date.now() + expiresInSec * 1000).toISOString(),
    meta: { last_refresh_at: new Date().toISOString(), last_refresh_status: 'ok' },
  };
}
