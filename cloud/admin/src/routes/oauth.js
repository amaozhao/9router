// /api/oauth/{provider}/start  — issues an authorize URL the user opens in a browser
// /api/oauth/callback           — provider redirects back here with code + state
//
// Flow state (verifier + tenant + connection-name) is kept in Redis under the
// state token so the callback can complete the exchange.

import {
  query, encryptForTenant, getRedis, ValidationError, NotFoundError, UpstreamError,
} from '@9router-cloud/shared';
import crypto from 'node:crypto';
import { readJson, ok } from '../lib/http.js';
import { requireSession, requireRole } from '../middleware/sessionAuth.js';
import {
  getProvider, buildPkce, buildAuthorizeUrl, exchangeCode, listProviders,
} from '../lib/oauthProviders.js';

const STATE_TTL_SEC = 600;

export async function listOauthProviders(req, res) {
  requireSession(req);
  // List configured providers (those with env-set client_id)
  const items = listProviders().map(name => {
    let ready = true;
    try { getProvider(name); } catch { ready = false; }
    return { provider: name, ready };
  });
  ok(res, { items });
}

export async function startOauth(req, res, { params }) {
  const session = requireSession(req);
  requireRole(session, 'owner', 'admin');
  const providerName = params.provider;
  const body = await readJson(req);
  const connectionName = (body.connectionName || `${providerName}-oauth`).trim();
  const redirectUri = body.redirectUri || defaultRedirectUri(req);

  const provider = getProvider(providerName);
  const { verifier, challenge } = buildPkce();
  const state = crypto.randomBytes(24).toString('base64url');

  await getRedis().set(
    `oauth:state:${state}`,
    JSON.stringify({
      tenantId: session.tenantId,
      userId: session.userId,
      provider: providerName,
      connectionName,
      verifier,
      redirectUri,
    }),
    'EX', STATE_TTL_SEC,
  );

  const authorizeUrl = buildAuthorizeUrl(provider, { state, codeChallenge: challenge, redirectUri });
  ok(res, { authorizeUrl, state, expiresInSec: STATE_TTL_SEC });
}

export async function oauthCallback(req, res) {
  const url = new URL(req.url, 'http://x');
  const code = url.searchParams.get('code');
  const state = url.searchParams.get('state');
  const error = url.searchParams.get('error');
  if (error) return renderResult(res, 400, { error, error_description: url.searchParams.get('error_description') });
  if (!code || !state) throw new ValidationError('Missing code or state');

  const redis = getRedis();
  const raw = await redis.get(`oauth:state:${state}`);
  if (!raw) throw new NotFoundError('OAuth state expired or unknown');
  await redis.del(`oauth:state:${state}`);

  const flow = JSON.parse(raw);
  const provider = getProvider(flow.provider);

  let tokens;
  try {
    tokens = await exchangeCode(provider, {
      code, codeVerifier: flow.verifier, redirectUri: flow.redirectUri, state,
    });
  } catch (e) {
    if (e instanceof UpstreamError) return renderResult(res, 502, { error: 'exchange_failed', detail: e.message });
    throw e;
  }

  if (!tokens.access_token) {
    return renderResult(res, 400, { error: 'no_access_token', token_response: tokens });
  }

  const credentials = {
    access_token: tokens.access_token,
    refresh_token: tokens.refresh_token || null,
    token_endpoint: provider.tokenEndpoint,
    client_id: provider.clientId,
    client_secret: provider.clientSecret || undefined,
    obtained_at: new Date().toISOString(),
    scope: tokens.scope || provider.scope,
  };
  const expiresInSec = Number(tokens.expires_in) || 3600;
  const oauthExpiresAt = new Date(Date.now() + expiresInSec * 1000).toISOString();

  const blob = encryptForTenant(flow.tenantId, credentials);

  try {
    await query(`
      INSERT INTO connections
        (tenant_id, provider, name, auth_type, credentials_encrypted, oauth_expires_at, metadata, enabled, weight)
      VALUES ($1, $2, $3, 'oauth', $4, $5, $6, TRUE, 1)
      ON CONFLICT (tenant_id, provider, name) DO UPDATE SET
        credentials_encrypted = EXCLUDED.credentials_encrypted,
        oauth_expires_at      = EXCLUDED.oauth_expires_at,
        auth_type             = 'oauth',
        enabled               = TRUE,
        updated_at            = now()
    `, [
      flow.tenantId, flow.provider, flow.connectionName,
      blob, oauthExpiresAt, JSON.stringify({ via: 'oauth', flow_state: state }),
    ]);
  } catch (e) {
    throw new UpstreamError('Failed to persist connection: ' + e.message);
  }

  // Invalidate router cache so the new connection is picked up immediately.
  await redis.del(`conn_cache:${flow.tenantId}:${flow.provider}`);

  renderResult(res, 200, {
    ok: true,
    provider: flow.provider,
    name: flow.connectionName,
    expires_at: oauthExpiresAt,
  });
}

/**
 * POST /api/oauth/{provider}/import — bring-your-own-token for subscription
 * providers (claude, codex, etc.). Lets a tenant paste an access_token they
 * already obtained out-of-band (e.g. via their local CLI install) directly
 * into a connection, bypassing the interactive OAuth dance.
 *
 * Body:
 *   {
 *     connectionName: string,
 *     accessToken:    string,
 *     refreshToken?:  string,
 *     expiresAt?:     ISO string | epoch ms | epoch seconds,
 *     scopes?:        string[]
 *   }
 */
export async function importOauth(req, res, { params }) {
  const session = requireSession(req);
  requireRole(session, 'owner', 'admin');
  const providerName = params.provider;
  const body = await readJson(req);
  const accessToken = String(body.accessToken || '').trim();
  if (!accessToken) throw new ValidationError('Missing accessToken');
  const connectionName = (body.connectionName || `${providerName}-import`).trim();

  // We don't strictly need a configured provider entry to import — but if
  // there is one, use its token_endpoint so the refresher can rotate later.
  let tokenEndpoint = null, clientId = null;
  try {
    const provider = getProvider(providerName);
    tokenEndpoint = provider.tokenEndpoint;
    clientId = provider.clientId;
  } catch { /* unknown provider is fine */ }

  // Normalise expiresAt
  let oauthExpiresAt = null;
  if (body.expiresAt != null) {
    if (typeof body.expiresAt === 'number') {
      const ms = body.expiresAt > 1e12 ? body.expiresAt : body.expiresAt * 1000;
      oauthExpiresAt = new Date(ms).toISOString();
    } else {
      const d = new Date(body.expiresAt);
      if (Number.isNaN(d.getTime())) throw new ValidationError('Invalid expiresAt');
      oauthExpiresAt = d.toISOString();
    }
  }

  const credentials = {
    access_token: accessToken,
    refresh_token: body.refreshToken || null,
    token_endpoint: tokenEndpoint,
    client_id: clientId,
    scope: Array.isArray(body.scopes) ? body.scopes.join(' ') : (body.scope || null),
    imported_at: new Date().toISOString(),
  };
  const blob = encryptForTenant(session.tenantId, credentials);

  await query(`
    INSERT INTO connections
      (tenant_id, provider, name, auth_type, credentials_encrypted, oauth_expires_at, metadata, enabled, weight)
    VALUES ($1, $2, $3, 'oauth', $4, $5, $6, TRUE, 1)
    ON CONFLICT (tenant_id, provider, name) DO UPDATE SET
      credentials_encrypted = EXCLUDED.credentials_encrypted,
      oauth_expires_at      = EXCLUDED.oauth_expires_at,
      auth_type             = 'oauth',
      enabled               = TRUE,
      updated_at            = now()
  `, [
    session.tenantId, providerName, connectionName,
    blob, oauthExpiresAt, JSON.stringify({ via: 'import' }),
  ]);

  // Hot-reload accountPicker view of this tenant's connections
  const { getRedis: _gr } = await import('@9router-cloud/shared');
  await _gr().del(`conn_cache:${session.tenantId}:${providerName}`);

  ok(res, {
    ok: true,
    provider: providerName,
    name: connectionName,
    expires_at: oauthExpiresAt,
  }, 201);
}

function defaultRedirectUri(req) {
  const host = req.headers['x-forwarded-host'] || req.headers['host'];
  const proto = req.headers['x-forwarded-proto'] || 'http';
  return `${proto}://${host}/api/oauth/callback`;
}

function renderResult(res, status, payload) {
  res.writeHead(status, { 'content-type': 'text/html; charset=utf-8' });
  res.end(`<!doctype html>
<html><head><meta charset="utf-8"><title>9router OAuth</title>
<style>body{font-family:system-ui;background:#0d1117;color:#c9d1d9;padding:48px;text-align:center}
pre{display:inline-block;text-align:left;background:#161b22;padding:16px;border-radius:8px;border:1px solid #30363d}</style>
</head><body>
<h1>${status === 200 ? '✓ Connection added' : '✗ OAuth failed'}</h1>
<pre>${escapeHtml(JSON.stringify(payload, null, 2))}</pre>
<p>You can close this window and return to the dashboard.</p>
</body></html>`);
}

function escapeHtml(s) {
  return s.replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
  })[c]);
}
