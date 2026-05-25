// OAuth provider registry. Each entry describes the authorize-URL builder
// and the token-exchange call. Add new providers here.
//
// All flows use PKCE (S256) because every modern provider supports it and
// it lets the client_secret be optional for public clients.

import crypto from 'node:crypto';
import { request } from 'undici';
import { ValidationError, UpstreamError } from '@9router-cloud/shared';

const PROVIDERS = {
  // Test-only provider; mocks PKCE flow when MOCK_OAUTH_BASE is set.
  mock: {
    authorizeUrl:  (process.env.MOCK_OAUTH_BASE || 'http://localhost:31995') + '/authorize',
    tokenEndpoint: (process.env.MOCK_OAUTH_BASE || 'http://localhost:31995') + '/token',
    scope: 'mock',
    clientIdEnv: 'MOCK_OAUTH_CLIENT_ID',
    clientSecretEnv: 'MOCK_OAUTH_CLIENT_SECRET',
  },
  // Reference: classic OAuth2 + PKCE. Configure per deployment.
  openai: {
    authorizeUrl:  'https://auth.openai.com/authorize',
    tokenEndpoint: 'https://auth.openai.com/oauth/token',
    scope: 'openid offline_access profile email',
    clientIdEnv: 'OPENAI_OAUTH_CLIENT_ID',
    clientSecretEnv: 'OPENAI_OAUTH_CLIENT_SECRET',
  },
  // Anthropic uses an OAuth-like flow for some products.
  anthropic: {
    authorizeUrl:  'https://console.anthropic.com/oauth/authorize',
    tokenEndpoint: 'https://console.anthropic.com/v1/oauth/token',
    scope: 'org:create_api_key user:profile user:inference',
    clientIdEnv: 'ANTHROPIC_OAUTH_CLIENT_ID',
    clientSecretEnv: 'ANTHROPIC_OAUTH_CLIENT_SECRET',
  },
  github: {
    authorizeUrl:  'https://github.com/login/oauth/authorize',
    tokenEndpoint: 'https://github.com/login/oauth/access_token',
    scope: 'read:user',
    clientIdEnv: 'GITHUB_OAUTH_CLIENT_ID',
    clientSecretEnv: 'GITHUB_OAUTH_CLIENT_SECRET',
  },
};

export function listProviders() {
  return Object.keys(PROVIDERS);
}

export function getProvider(name) {
  const p = PROVIDERS[name];
  if (!p) throw new ValidationError(`Unsupported OAuth provider: ${name}`,
    { supported: Object.keys(PROVIDERS) });
  const clientId = process.env[p.clientIdEnv];
  if (!clientId) {
    throw new ValidationError(
      `Provider ${name} is not configured on this server`,
      { hint: `set env ${p.clientIdEnv} (and ${p.clientSecretEnv} if confidential client)` }
    );
  }
  return {
    name,
    authorizeUrl: p.authorizeUrl,
    tokenEndpoint: p.tokenEndpoint,
    scope: p.scope,
    clientId,
    clientSecret: process.env[p.clientSecretEnv] || null,
  };
}

/** Build a PKCE pair: code_verifier + code_challenge (S256). */
export function buildPkce() {
  const verifier = crypto.randomBytes(32).toString('base64url');
  const challenge = crypto.createHash('sha256').update(verifier).digest('base64url');
  return { verifier, challenge };
}

/** Construct the provider authorize URL the user should be redirected to. */
export function buildAuthorizeUrl(provider, { state, codeChallenge, redirectUri }) {
  const u = new URL(provider.authorizeUrl);
  u.searchParams.set('client_id', provider.clientId);
  u.searchParams.set('response_type', 'code');
  u.searchParams.set('redirect_uri', redirectUri);
  u.searchParams.set('state', state);
  u.searchParams.set('code_challenge', codeChallenge);
  u.searchParams.set('code_challenge_method', 'S256');
  if (provider.scope) u.searchParams.set('scope', provider.scope);
  return u.toString();
}

/** Exchange the authorization code for access + refresh tokens. */
export async function exchangeCode(provider, { code, codeVerifier, redirectUri }) {
  const params = new URLSearchParams();
  params.set('grant_type', 'authorization_code');
  params.set('code', code);
  params.set('redirect_uri', redirectUri);
  params.set('client_id', provider.clientId);
  params.set('code_verifier', codeVerifier);
  if (provider.clientSecret) params.set('client_secret', provider.clientSecret);

  const res = await request(provider.tokenEndpoint, {
    method: 'POST',
    headers: {
      'content-type': 'application/x-www-form-urlencoded',
      'accept': 'application/json',
    },
    body: params.toString(),
  });
  const text = await res.body.text();
  if (res.statusCode >= 400) {
    throw new UpstreamError(`Token exchange failed (${res.statusCode}): ${text.slice(0, 300)}`,
      { status_code: res.statusCode });
  }
  // Some providers (notably GitHub) return form-encoded by default
  let data;
  try { data = JSON.parse(text); }
  catch { data = Object.fromEntries(new URLSearchParams(text)); }
  return data;
}
