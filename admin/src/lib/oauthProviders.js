// OAuth provider registry. Each entry describes the authorize-URL builder
// and the token-exchange call. Add new providers here.
//
// All flows use PKCE (S256) because every modern provider supports it and
// it lets the client_secret be optional for public clients.

import crypto from 'node:crypto';
import { request } from 'undici';
import { ValidationError, UpstreamError } from '@lazirouter-cloud/shared';

const PROVIDERS = {
  // Test-only provider; mocks PKCE flow when MOCK_OAUTH_BASE is set.
  mock: {
    authorizeUrl:  (process.env.MOCK_OAUTH_BASE || 'http://localhost:31995') + '/authorize',
    tokenEndpoint: (process.env.MOCK_OAUTH_BASE || 'http://localhost:31995') + '/token',
    scope: 'mock',
    clientIdEnv: 'MOCK_OAUTH_CLIENT_ID',
    clientSecretEnv: 'MOCK_OAUTH_CLIENT_SECRET',
  },

  // ── Claude Code subscription (claude.ai OAuth) ─────────────────────
  // Uses the public claude-cli client_id. Token exchange is JSON, not
  // form-urlencoded; authorize page expects code=true and a literal
  // claude.ai domain. The resulting access_token can call
  // https://api.anthropic.com/v1/messages directly as Bearer auth.
  claude: {
    authorizeUrl:  'https://claude.ai/oauth/authorize',
    tokenEndpoint: 'https://api.anthropic.com/v1/oauth/token',
    scope: 'org:create_api_key user:profile user:inference',
    // The claude-cli's client_id is public, hardcoded — no env var override
    // needed for it to be "ready".
    clientId: '9d1c250a-e61b-44d9-88ed-5944d1962f5e',
    tokenExchangeFormat: 'json',
    authorizeExtraParams: { code: 'true' },
  },

  // ── ChatGPT/Codex subscription (auth.openai.com OAuth) ─────────────
  // Uses the public codex-cli client_id. Standard PKCE with extras the
  // OpenAI Codex CLI sends. Resulting access_token calls
  // https://chatgpt.com/backend-api/codex/responses (Responses API).
  codex: {
    authorizeUrl:  'https://auth.openai.com/oauth/authorize',
    tokenEndpoint: 'https://auth.openai.com/oauth/token',
    scope: 'openid profile email offline_access',
    clientId: 'app_EMoamEEZ73f0CkXaXp7hrann',
    tokenExchangeFormat: 'form',
    authorizeExtraParams: {
      id_token_add_organizations: 'true',
      codex_cli_simplified_flow: 'true',
      originator: 'codex_cli_rs',
    },
  },

  // Configurable providers (env-gated)
  openai: {
    authorizeUrl:  'https://auth.openai.com/authorize',
    tokenEndpoint: 'https://auth.openai.com/oauth/token',
    scope: 'openid offline_access profile email',
    clientIdEnv: 'OPENAI_OAUTH_CLIENT_ID',
    clientSecretEnv: 'OPENAI_OAUTH_CLIENT_SECRET',
  },
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

  // Hardcoded clientId (subscription providers) vs env-gated
  const clientId = p.clientId || (p.clientIdEnv ? process.env[p.clientIdEnv] : null);
  if (!clientId) {
    throw new ValidationError(
      `Provider ${name} is not configured on this server`,
      { hint: `set env ${p.clientIdEnv}` }
    );
  }
  return {
    name,
    authorizeUrl: p.authorizeUrl,
    tokenEndpoint: p.tokenEndpoint,
    scope: p.scope,
    clientId,
    clientSecret: p.clientSecret || (p.clientSecretEnv ? process.env[p.clientSecretEnv] : null),
    tokenExchangeFormat: p.tokenExchangeFormat || 'form',
    authorizeExtraParams: p.authorizeExtraParams || {},
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
  for (const [k, v] of Object.entries(provider.authorizeExtraParams || {})) {
    u.searchParams.set(k, String(v));
  }
  return u.toString();
}

/**
 * Exchange the authorization code for access + refresh tokens.
 * Some providers (notably claude.ai) require JSON body; pass
 * provider.tokenExchangeFormat = 'json' to use it.
 */
export async function exchangeCode(provider, { code, codeVerifier, redirectUri, state }) {
  // Claude embeds state in the code as `code#state`; strip it back out so
  // we send only the authorization code (the canonical reference behavior).
  let authCode = code;
  let codeState = state;
  if (authCode.includes('#')) {
    const parts = authCode.split('#');
    authCode = parts[0];
    codeState = parts[1] || state;
  }

  let body, contentType;
  if (provider.tokenExchangeFormat === 'json') {
    const payload = {
      grant_type: 'authorization_code',
      code: authCode,
      state: codeState,
      redirect_uri: redirectUri,
      client_id: provider.clientId,
      code_verifier: codeVerifier,
    };
    if (provider.clientSecret) payload.client_secret = provider.clientSecret;
    body = JSON.stringify(payload);
    contentType = 'application/json';
  } else {
    const params = new URLSearchParams();
    params.set('grant_type', 'authorization_code');
    params.set('code', authCode);
    params.set('redirect_uri', redirectUri);
    params.set('client_id', provider.clientId);
    params.set('code_verifier', codeVerifier);
    if (provider.clientSecret) params.set('client_secret', provider.clientSecret);
    body = params.toString();
    contentType = 'application/x-www-form-urlencoded';
  }

  const res = await request(provider.tokenEndpoint, {
    method: 'POST',
    headers: { 'content-type': contentType, 'accept': 'application/json' },
    body,
  });
  const text = await res.body.text();
  if (res.statusCode >= 400) {
    throw new UpstreamError(`Token exchange failed (${res.statusCode}): ${text.slice(0, 300)}`,
      { status_code: res.statusCode });
  }
  let data;
  try { data = JSON.parse(text); }
  catch { data = Object.fromEntries(new URLSearchParams(text)); }
  return data;
}
