import { createRoot } from 'react-dom/client';
import { useState, useEffect, createContext, useContext, useCallback } from 'react';
import htm from 'htm';
import React from 'react';

const html = htm.bind(React.createElement);
const ADMIN_API = window.localStorage.getItem('ADMIN_API') || 'http://localhost:30200';

/* ─────────────────────────── API client ─────────────────────────── */

async function api(path, opts = {}) {
  const token = localStorage.getItem('token');
  const headers = { 'content-type': 'application/json', ...(opts.headers || {}) };
  if (token) headers.authorization = `Bearer ${token}`;
  const res = await fetch(ADMIN_API + path, { ...opts, headers });
  const text = await res.text();
  const body = text ? JSON.parse(text) : {};
  if (!res.ok) throw new Error(body?.error?.message || `${res.status} ${res.statusText}`);
  return body;
}

/* ─────────────────────────── Auth context ─────────────────────────── */

const AuthCtx = createContext(null);

function AuthProvider({ children }) {
  const [session, setSession] = useState(null);
  const [loading, setLoading] = useState(true);
  // Plaintext of the default API key minted on signup — shown once via modal.
  const [signupBonus, setSignupBonus] = useState(null);

  useEffect(() => {
    const token = localStorage.getItem('token');
    if (!token) { setLoading(false); return; }
    api('/auth/me').then(setSession).catch(() => localStorage.removeItem('token')).finally(() => setLoading(false));
  }, []);

  const login = async (email, password) => {
    const r = await api('/auth/login', { method: 'POST', body: JSON.stringify({ email, password }) });
    localStorage.setItem('token', r.token);
    setSession({ user: r.user, tenant: r.tenant });
  };
  const signup = async (email, password, tenantName, inviteCode) => {
    const r = await api('/auth/signup', {
      method: 'POST',
      body: JSON.stringify({ email, password, tenantName, inviteCode }),
    });
    localStorage.setItem('token', r.token);
    setSession({ user: r.user, tenant: r.tenant });
    if (r.defaultApiKey?.key) setSignupBonus(r.defaultApiKey);
  };
  const logout = () => { localStorage.removeItem('token'); setSession(null); setSignupBonus(null); };
  const clearSignupBonus = () => setSignupBonus(null);

  return html`<${AuthCtx.Provider} value=${{ session, loading, login, signup, logout, signupBonus, clearSignupBonus }}>${children}<//>`;
}

const useAuth = () => useContext(AuthCtx);

/* ─────────────────────────── Auth page (login/signup) ─────────────────────────── */

function AuthPage() {
  const [mode, setMode] = useState('login');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [tenantName, setTenantName] = useState('');
  const [inviteCode, setInviteCode] = useState('');
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);
  const { login, signup } = useAuth();

  const submit = async (e) => {
    e.preventDefault();
    setErr(null); setBusy(true);
    try {
      if (mode === 'login') await login(email, password);
      else await signup(email, password, tenantName || undefined, inviteCode.trim() || undefined);
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  };

  return html`
    <div class="auth-page">
      <form class="auth-card" onSubmit=${submit}>
        <h1>${mode === 'login' ? '登录 LaziRouter Cloud' : '创建账户'}</h1>
        <p>${mode === 'login' ? '用 email + 密码登录' : '内测期间需要邀请码才能注册'}</p>
        <label>Email</label>
        <input type="email" required value=${email} onChange=${e => setEmail(e.target.value)} />
        <label>密码（≥ 8 位）</label>
        <input type="password" required minLength=${8} value=${password} onChange=${e => setPassword(e.target.value)} />
        ${mode === 'signup' && html`
          <label>邀请码</label>
          <input required value=${inviteCode} onChange=${e => setInviteCode(e.target.value)}
                 placeholder="向超管索取邀请码" autocomplete="off" />
          <label>租户名称（可选）</label>
          <input value=${tenantName} onChange=${e => setTenantName(e.target.value)} placeholder="My workspace" />
        `}
        ${err && html`<div class="error-text">${err}</div>`}
        <button class="btn" style=${{ width: '100%', marginTop: 16 }} disabled=${busy}>
          ${busy ? '...' : (mode === 'login' ? '登录' : '注册')}
        </button>
        <div style=${{ textAlign: 'center', marginTop: 16 }}>
          <a class="switch" onClick=${() => setMode(mode === 'login' ? 'signup' : 'login')}>
            ${mode === 'login' ? '没有账户？注册一个' : '已有账户？登录'}
          </a>
        </div>
      </form>
    </div>
  `;
}

function SignupBonusModal({ value, onClose }) {
  return html`
    <div class="modal-backdrop">
      <div class="modal">
        <h2>🎉 默认 API Key 已创建</h2>
        <p>系统已为你生成第一个 API Key（前缀 <span class="mono">${value.keyPrefix}…</span>）。
        这是 <b>唯一一次</b> 看到完整 key 的机会，请立刻复制并保存到你的客户端：</p>
        <div class="ok-text mono" style=${{ background: '#0d1117', padding: 12, borderRadius: 6, border: '1px solid var(--border)' }}>
          ${value.key}
        </div>
        <p class="muted" style=${{ marginTop: 12 }}>
          下一步：到「Provider 连接」页面添加你的订阅 token 或 API Key，
          然后把这个 key 写入 Codex/Claude Code 的环境变量，就可以走 cloud 路由了。
        </p>
        <div class="modal-actions">
          <button class="btn" onClick=${onClose}>我已保存</button>
        </div>
      </div>
    </div>
  `;
}

/* ─────────────────────────── Dashboard shell ─────────────────────────── */

function Shell({ children, current, onNav }) {
  const { session, logout } = useAuth();
  const items = [
    { key: 'overview',    label: '概览' },
    { key: 'api-keys',    label: 'API Keys' },
    { key: 'connections', label: 'Provider 连接' },
    { key: 'combos',      label: 'Combo 链' },
    { key: 'routing',     label: 'Auto Routing' },
    { key: 'usage',       label: '用量' },
  ];
  const adminItems = [
    { key: 'invites', label: '邀请码' },
    { key: 'tenants', label: '租户管理' },
  ];
  const showAdmin = session.user.isSuperAdmin;
  return html`
    <div class="app">
      <aside class="sidebar">
        <div class="brand">lazirouter · cloud</div>
        <nav>
          ${items.map(i => html`
            <a key=${i.key} class=${current === i.key ? 'active' : ''} onClick=${() => onNav(i.key)}>${i.label}</a>
          `)}
          ${showAdmin && html`
            <div class="section-divider" style=${{ marginTop: 12, paddingTop: 12 }}>
              <div class="muted" style=${{ fontSize: 11, textTransform: 'uppercase', letterSpacing: '0.5px', padding: '0 12px 6px' }}>
                超管
              </div>
              ${adminItems.map(i => html`
                <a key=${i.key} class=${current === i.key ? 'active' : ''} onClick=${() => onNav(i.key)}>${i.label}</a>
              `)}
            </div>
          `}
        </nav>
        <div class="me">
          <div class="email">${session.user.email}${session.user.isSuperAdmin && html` <span class="badge warn">超管</span>`}</div>
          <div>租户：${session.tenant.name} · plan: ${session.tenant.plan}</div>
          <button onClick=${logout} style=${{ marginTop: 8 }}>登出</button>
        </div>
      </aside>
      <main class="main">${children}</main>
    </div>
  `;
}

/* ─────────────────────────── Overview ─────────────────────────── */

function QuotaBar({ used, limit }) {
  if (limit == null) return html`<div class="quota-row"><span>${used.toLocaleString()}</span><b>无限制</b></div>`;
  const ratio = limit > 0 ? Math.min(1, used / limit) : 0;
  const cls = ratio > 0.95 ? 'err' : ratio > 0.7 ? 'warn' : '';
  return html`
    <div class="quota-row">
      <span>${used.toLocaleString()} / ${limit.toLocaleString()}</span>
      <b>${Math.round(ratio * 100)}%</b>
    </div>
    <div class="progress"><div class=${`bar ${cls}`} style=${{ width: `${ratio * 100}%` }}></div></div>
  `;
}

function Overview() {
  const [data, setData] = useState(null);
  const [quota, setQuota] = useState(null);
  useEffect(() => {
    api('/api/usage/summary?hours=24').then(setData).catch(console.error);
    api('/api/me/quota').then(setQuota).catch(console.error);
  }, []);
  const totals = (data?.items || []).reduce((a, r) => ({
    req: a.req + r.requestCount,
    err: a.err + r.errorCount,
    pt:  a.pt  + r.promptTokens,
    ct:  a.ct  + r.completionTokens,
    cost: a.cost + r.costUsd,
  }), { req: 0, err: 0, pt: 0, ct: 0, cost: 0 });

  return html`
    <h1>过去 24 小时</h1>
    ${quota && html`
      <div class="card">
        <h3 style=${{ margin: '0 0 12px' }}>今日额度（${quota.date} · 北京时区）</h3>
        <div style=${{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 24 }}>
          <div>
            <div class="muted" style=${{ fontSize: 12 }}>请求数</div>
            <${QuotaBar} used=${quota.requests.used} limit=${quota.requests.limit} />
          </div>
          <div>
            <div class="muted" style=${{ fontSize: 12 }}>Token 总数</div>
            <${QuotaBar} used=${quota.tokens.used} limit=${quota.tokens.limit} />
          </div>
        </div>
        <p class="muted" style=${{ fontSize: 12, marginTop: 12, marginBottom: 0 }}>
          额度超过上限后所有请求会返回 429，次日 00:00（北京时间）自动重置。
        </p>
      </div>
    `}
    <div class="kpi">
      <div class="stat"><div class="label">请求数</div><div class="value">${totals.req}</div></div>
      <div class="stat"><div class="label">错误</div><div class="value">${totals.err}</div></div>
      <div class="stat"><div class="label">输入 token</div><div class="value">${totals.pt}</div></div>
      <div class="stat"><div class="label">输出 token</div><div class="value">${totals.ct}</div></div>
      <div class="stat"><div class="label">费用 USD</div><div class="value">$${totals.cost.toFixed(4)}</div></div>
    </div>
    <div class="card">
      <h3 style=${{ margin: '0 0 12px' }}>按 provider / model 拆分</h3>
      <table>
        <thead><tr>
          <th>Provider</th><th>Model</th><th>请求</th><th>错误</th>
          <th>输入</th><th>输出</th><th>费用</th>
        </tr></thead>
        <tbody>
          ${(data?.items || []).length === 0 ? html`<tr><td colSpan=${7} class="empty">还没有用量数据</td></tr>`
            : (data?.items || []).map((r, i) => html`
            <tr key=${i}>
              <td>${r.provider}</td>
              <td class="mono">${r.model}</td>
              <td>${r.requestCount}</td>
              <td>${r.errorCount > 0 ? html`<span class="badge err">${r.errorCount}</span>` : '0'}</td>
              <td>${r.promptTokens}</td>
              <td>${r.completionTokens}</td>
              <td class="mono">$${r.costUsd.toFixed(6)}</td>
            </tr>
          `)}
        </tbody>
      </table>
    </div>
  `;
}

/* ─────────────────────────── API Keys ─────────────────────────── */

function ApiKeys() {
  const [items, setItems] = useState([]);
  const [show, setShow] = useState(false);
  const [created, setCreated] = useState(null);

  const reload = useCallback(() => api('/api/keys').then(r => setItems(r.items)), []);
  useEffect(() => { reload(); }, [reload]);

  return html`
    <h1>API Keys</h1>
    <div class="toolbar">
      <button class="btn" onClick=${() => setShow(true)}>+ 新建 API Key</button>
    </div>
    <div class="card">
      <table>
        <thead><tr>
          <th>名称</th><th>前缀</th><th>限速 (rpm)</th><th>最近使用</th><th>状态</th><th></th>
        </tr></thead>
        <tbody>
          ${items.length === 0 ? html`<tr><td colSpan=${6} class="empty">还没有 key</td></tr>`
            : items.map(k => html`
            <tr key=${k.id}>
              <td>${k.name}</td>
              <td class="mono">${k.keyPrefix}…</td>
              <td>${k.rateLimitRpm ?? html`<span class="muted">plan 默认</span>`}</td>
              <td class="muted">${k.lastUsedAt ? new Date(k.lastUsedAt).toLocaleString() : '从未'}</td>
              <td>${k.revokedAt
                ? html`<span class="badge err">已撤销</span>`
                : html`<span class="badge ok">活跃</span>`}</td>
              <td>${!k.revokedAt && html`
                <button class="btn danger" onClick=${async () => {
                  if (!confirm('撤销后所有用此 key 的客户端立即 401。继续？')) return;
                  await api('/api/keys/' + k.id, { method: 'DELETE' });
                  await reload();
                }}>撤销</button>`}
              </td>
            </tr>
          `)}
        </tbody>
      </table>
    </div>
    ${show && html`<${NewKeyModal} onClose=${() => setShow(false)} onCreated=${(k) => { setCreated(k); setShow(false); reload(); }} />`}
    ${created && html`<${ShowKeyModal} value=${created} onClose=${() => setCreated(null)} />`}
  `;
}

function NewKeyModal({ onClose, onCreated }) {
  const [name, setName] = useState('');
  const [rpm, setRpm] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const submit = async (e) => {
    e.preventDefault(); setBusy(true); setErr(null);
    try {
      const body = { name: name || 'unnamed' };
      if (rpm) body.rateLimitRpm = Number(rpm);
      const r = await api('/api/keys', { method: 'POST', body: JSON.stringify(body) });
      onCreated(r);
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  };
  return html`
    <div class="modal-backdrop">
      <form class="modal" onSubmit=${submit}>
        <h2>新建 API Key</h2>
        <label>名称</label>
        <input required value=${name} onChange=${e => setName(e.target.value)} placeholder="e.g. claude-code-laptop" />
        <label>每分钟请求上限（rpm，可选；留空走 plan 默认）</label>
        <input type="number" min="1" value=${rpm} onChange=${e => setRpm(e.target.value)} />
        ${err && html`<div class="error-text">${err}</div>`}
        <div class="modal-actions">
          <button type="button" class="btn ghost" onClick=${onClose}>取消</button>
          <button class="btn" disabled=${busy}>${busy ? '...' : '创建'}</button>
        </div>
      </form>
    </div>
  `;
}

function ShowKeyModal({ value, onClose }) {
  return html`
    <div class="modal-backdrop">
      <div class="modal">
        <h2>Key 创建成功</h2>
        <p>这是 <b>唯一一次</b> 看到完整 key 的机会，请立刻复制并保存到客户端配置：</p>
        <div class="ok-text mono" style=${{ background: '#0d1117', padding: 12, borderRadius: 6, border: '1px solid var(--border)' }}>
          ${value.key}
        </div>
        <p class="muted" style=${{ marginTop: 12 }}>
          将上面的 key 配置到你的 CLI 工具：<br/>
          • Claude Code：endpoint <span class="mono">http://localhost:30100/v1</span>，header <span class="mono">x-api-key</span><br/>
          • Codex / OpenCode / Cursor：endpoint <span class="mono">http://localhost:30100/v1</span>，<span class="mono">Authorization: Bearer ...</span>
        </p>
        <div class="modal-actions">
          <button class="btn" onClick=${onClose}>我已保存</button>
        </div>
      </div>
    </div>
  `;
}

/* ─────────────────────────── Connections ─────────────────────────── */

function Connections() {
  const [items, setItems] = useState([]);
  const [show, setShow] = useState(false);
  const [editing, setEditing] = useState(null);
  const reload = useCallback(() => api('/api/connections').then(r => setItems(r.items)), []);
  useEffect(() => { reload(); }, [reload]);

  return html`
    <h1>Provider 连接</h1>
    <div class="toolbar">
      <button class="btn" onClick=${() => setShow(true)}>+ 添加 Provider 连接</button>
    </div>
    <div class="card">
      <table>
        <thead><tr>
          <th>Provider</th><th>名称</th><th>认证</th><th>Base URL</th><th>代理</th><th>权重</th><th>状态</th><th></th>
        </tr></thead>
        <tbody>
          ${items.length === 0 ? html`<tr><td colSpan=${8} class="empty">还没有 provider 连接</td></tr>`
            : items.map(c => html`
            <tr key=${c.id}>
              <td><b>${c.provider}</b></td>
              <td>${c.name}</td>
              <td><span class="badge ${c.authType === 'oauth' ? 'warn' : 'ok'}">${c.authType}</span></td>
              <td class="mono muted">${c.metadata?.base_url || '(provider 默认)'}</td>
              <td class="mono muted">${c.metadata?.proxy_url || '(直连)'}</td>
              <td>${c.weight}</td>
              <td>${c.enabled
                ? html`<span class="badge ok">启用</span>`
                : html`<span class="badge err">禁用</span>`}</td>
              <td>
                <button class="btn ghost" onClick=${() => setEditing(c)}>编辑</button>
                <button class="btn danger" onClick=${async () => {
                  if (!confirm('删除该连接？')) return;
                  await api('/api/connections/' + c.id, { method: 'DELETE' });
                  reload();
                }}>删除</button>
              </td>
            </tr>
          `)}
        </tbody>
      </table>
    </div>
    ${show && html`<${NewConnModal} onClose=${() => setShow(false)} onCreated=${() => { setShow(false); reload(); }} />`}
    ${editing && html`<${EditConnModal} value=${editing} onClose=${() => setEditing(null)}
                                        onSaved=${() => { setEditing(null); reload(); }} />`}
  `;
}

function NewConnModal({ onClose, onCreated }) {
  const [tab, setTab] = useState('api_key');
  return html`
    <div class="modal-backdrop">
      <div class="modal" style=${{ minWidth: 540 }}>
        <h2>添加 Provider 连接</h2>
        <div class="tabs">
          <button class=${tab === 'api_key'   ? 'active' : ''} onClick=${() => setTab('api_key')}>API Key</button>
          <button class=${tab === 'subscribe' ? 'active' : ''} onClick=${() => setTab('subscribe')}>导入订阅 Token</button>
        </div>
        ${tab === 'api_key'
          ? html`<${ApiKeyConnForm}   onClose=${onClose} onCreated=${onCreated} />`
          : html`<${SubscribeConnForm} onClose=${onClose} onCreated=${onCreated} />`}
      </div>
    </div>
  `;
}

function ApiKeyConnForm({ onClose, onCreated }) {
  const [provider, setProvider] = useState('openai');
  const [name, setName] = useState('');
  const [baseUrl, setBaseUrl] = useState('');
  const [proxyUrl, setProxyUrl] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [weight, setWeight] = useState(1);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const submit = async (e) => {
    e.preventDefault(); setBusy(true); setErr(null);
    try {
      const metadata = {};
      if (baseUrl.trim())  metadata.base_url  = baseUrl.trim();
      if (proxyUrl.trim()) metadata.proxy_url = proxyUrl.trim();
      await api('/api/connections', {
        method: 'POST',
        body: JSON.stringify({
          provider, name, authType: 'api_key',
          credentials: { api_key: apiKey },
          metadata,
          weight: Number(weight) || 1,
        }),
      });
      onCreated();
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  };
  return html`
    <form onSubmit=${submit}>
      <label>Provider</label>
      <select value=${provider} onChange=${e => setProvider(e.target.value)}>
        <option value="openai">openai</option>
        <option value="anthropic">anthropic</option>
        <option value="gemini">gemini</option>
        <option value="glm">glm</option>
        <option value="deepseek">deepseek</option>
        <option value="minimax">minimax</option>
        <option value="openrouter">openrouter</option>
        <option value="mock">mock (test)</option>
      </select>
      <label>连接名称（同 provider 可以多个，用于多账号轮询）</label>
      <input required value=${name} onChange=${e => setName(e.target.value)} placeholder="e.g. account-1" />
      <label>Base URL（留空走 provider 默认 endpoint）</label>
      <input value=${baseUrl} onChange=${e => setBaseUrl(e.target.value)} placeholder="https://..." />
      <label>代理地址（可选，国内访问 Anthropic/OpenAI 等海外端常需配置）</label>
      <input value=${proxyUrl} onChange=${e => setProxyUrl(e.target.value)} placeholder="http://127.0.0.1:7899" />
      <label>API Key</label>
      <input required value=${apiKey} onChange=${e => setApiKey(e.target.value)} placeholder="sk-..." />
      <label>权重（轮询时按权重分配）</label>
      <input type="number" min="1" value=${weight} onChange=${e => setWeight(e.target.value)} />
      ${err && html`<div class="error-text">${err}</div>`}
      <div class="modal-actions">
        <button type="button" class="btn ghost" onClick=${onClose}>取消</button>
        <button class="btn" disabled=${busy}>${busy ? '...' : '创建'}</button>
      </div>
    </form>
  `;
}

function SubscribeConnForm({ onClose, onCreated }) {
  const [provider, setProvider] = useState('claude');
  const [name, setName] = useState('');
  const [tokenJson, setTokenJson] = useState('');
  const [proxyUrl, setProxyUrl] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);

  function parseLocalToken(provider, text) {
    // Best-effort: accept either the exact ~/.claude or ~/.codex JSON file,
    // a raw OAuth token-response JSON, or just the access_token string.
    const trimmed = text.trim();
    if (!trimmed) throw new Error('请粘贴 token JSON 或文本');
    if (!trimmed.startsWith('{')) {
      return { accessToken: trimmed };
    }
    let obj;
    try { obj = JSON.parse(trimmed); }
    catch { throw new Error('不是合法的 JSON'); }
    // ~/.claude/.credentials.json shape: { claudeAiOauth: { accessToken, refreshToken, expiresAt, scopes } }
    if (obj.claudeAiOauth) {
      const o = obj.claudeAiOauth;
      return {
        accessToken: o.accessToken,
        refreshToken: o.refreshToken,
        expiresAt: o.expiresAt,
        scopes: o.scopes,
      };
    }
    // ~/.codex/auth.json shape: { OPENAI_API_KEY?, tokens: { id_token, access_token, refresh_token, account_id, ... }, last_refresh }
    if (obj.tokens) {
      const t = obj.tokens;
      return {
        accessToken: t.access_token || t.id_token,
        refreshToken: t.refresh_token,
        scopes: t.scope ? t.scope.split(/\s+/) : undefined,
      };
    }
    // Generic OAuth token-response shape.
    if (obj.access_token || obj.accessToken) {
      return {
        accessToken: obj.access_token || obj.accessToken,
        refreshToken: obj.refresh_token || obj.refreshToken,
        expiresAt: obj.expires_at || obj.expiresAt,
        scopes: obj.scope ? obj.scope.split(/\s+/) : obj.scopes,
      };
    }
    throw new Error('JSON 里找不到 access_token；请粘贴 ~/.claude/.credentials.json 或 ~/.codex/auth.json 全文');
  }

  const submit = async (e) => {
    e.preventDefault(); setBusy(true); setErr(null);
    try {
      const parsed = parseLocalToken(provider, tokenJson);
      const importBody = {
        connectionName: name.trim() || `${provider}-sub`,
        accessToken: parsed.accessToken,
        refreshToken: parsed.refreshToken,
        expiresAt: parsed.expiresAt,
        scopes: parsed.scopes,
      };
      const r = await api(`/api/oauth/${provider}/import`, { method: 'POST', body: JSON.stringify(importBody) });
      // After import, optionally set proxy_url on the resulting connection by
      // patching it. We need the id — fetch list and find it.
      if (proxyUrl.trim()) {
        const all = await api('/api/connections');
        const c = (all.items || []).find(x => x.provider === provider && x.name === r.name);
        if (c) {
          await api('/api/connections/' + c.id, {
            method: 'PATCH',
            body: JSON.stringify({ metadata: { ...c.metadata, proxy_url: proxyUrl.trim() } }),
          });
        }
      }
      onCreated();
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  };

  return html`
    <form onSubmit=${submit}>
      <p class="muted" style=${{ marginTop: 0 }}>
        把你本机 CLI 的订阅 token 文件全文粘贴进来，cloud router 会用 KMS 加密保存，
        然后帮你用伪装头调用上游订阅端点。<b>token 不会再次明文呈现</b>。
      </p>
      <label>Provider</label>
      <select value=${provider} onChange=${e => setProvider(e.target.value)}>
        <option value="claude">claude（订阅）— ~/.claude/.credentials.json</option>
        <option value="codex">codex（ChatGPT 订阅）— ~/.codex/auth.json</option>
      </select>
      <label>连接名称（同 provider 多个账号可以分别命名）</label>
      <input value=${name} onChange=${e => setName(e.target.value)} placeholder="默认 ${provider}-sub" />
      <label>Token 内容（粘贴 JSON 文件全文或单独的 access_token）</label>
      <textarea required rows="6" value=${tokenJson} onChange=${e => setTokenJson(e.target.value)}
                placeholder='粘贴 ~/.claude/.credentials.json 或 ~/.codex/auth.json 全文…' />
      <label>代理地址（国内访问 chatgpt.com / claude.ai 通常必填）</label>
      <input value=${proxyUrl} onChange=${e => setProxyUrl(e.target.value)} placeholder="http://127.0.0.1:7899" />
      ${err && html`<div class="error-text">${err}</div>`}
      <div class="modal-actions">
        <button type="button" class="btn ghost" onClick=${onClose}>取消</button>
        <button class="btn" disabled=${busy}>${busy ? '...' : '导入'}</button>
      </div>
    </form>
  `;
}

function EditConnModal({ value, onClose, onSaved }) {
  const [name, setName] = useState(value.name);
  const [enabled, setEnabled] = useState(value.enabled);
  const [weight, setWeight] = useState(value.weight);
  const [baseUrl, setBaseUrl] = useState(value.metadata?.base_url || '');
  const [proxyUrl, setProxyUrl] = useState(value.metadata?.proxy_url || '');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);

  const submit = async (e) => {
    e.preventDefault(); setBusy(true); setErr(null);
    try {
      const nextMeta = { ...(value.metadata || {}) };
      if (baseUrl.trim()) nextMeta.base_url = baseUrl.trim(); else delete nextMeta.base_url;
      if (proxyUrl.trim()) nextMeta.proxy_url = proxyUrl.trim(); else delete nextMeta.proxy_url;
      await api('/api/connections/' + value.id, {
        method: 'PATCH',
        body: JSON.stringify({
          name: name.trim(),
          enabled,
          weight: Number(weight) || 1,
          metadata: nextMeta,
        }),
      });
      onSaved();
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  };

  return html`
    <div class="modal-backdrop">
      <form class="modal" onSubmit=${submit}>
        <h2>编辑连接</h2>
        <p class="muted" style=${{ marginTop: 0 }}>
          凭据无法在此处修改——要更换 API Key / 订阅 token，请删除该连接后重建。
        </p>
        <label>Provider</label>
        <input disabled value=${value.provider} />
        <label>名称</label>
        <input required value=${name} onChange=${e => setName(e.target.value)} />
        <label>Base URL</label>
        <input value=${baseUrl} onChange=${e => setBaseUrl(e.target.value)} placeholder="(provider 默认)" />
        <label>代理地址</label>
        <input value=${proxyUrl} onChange=${e => setProxyUrl(e.target.value)} placeholder="http://127.0.0.1:7899" />
        <label>权重</label>
        <input type="number" min="1" value=${weight} onChange=${e => setWeight(e.target.value)} />
        <label style=${{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <input type="checkbox" style=${{ width: 'auto' }} checked=${enabled}
                 onChange=${e => setEnabled(e.target.checked)} />
          <span style=${{ color: 'var(--text)' }}>启用</span>
        </label>
        ${err && html`<div class="error-text">${err}</div>`}
        <div class="modal-actions">
          <button type="button" class="btn ghost" onClick=${onClose}>取消</button>
          <button class="btn" disabled=${busy}>${busy ? '...' : '保存'}</button>
        </div>
      </form>
    </div>
  `;
}

/* ─────────────────────────── Combos ─────────────────────────── */

function Combos() {
  const [items, setItems] = useState([]);
  const [show, setShow] = useState(false);
  const reload = useCallback(() => api('/api/combos').then(r => setItems(r.items)), []);
  useEffect(() => { reload(); }, [reload]);

  return html`
    <h1>Combo 链 (fallback)</h1>
    <p class="muted">客户端用 <span class="mono">model="combo:&lt;slug&gt;"</span> 调用，路由会按顺序尝试每个节点，遇到 429/5xx 自动 fallback 到下一个。</p>
    <div class="toolbar">
      <button class="btn" onClick=${() => setShow(true)}>+ 新建 Combo</button>
    </div>
    <div class="card">
      <table>
        <thead><tr>
          <th>Slug</th><th>名称</th><th>节点顺序</th><th>启用</th><th></th>
        </tr></thead>
        <tbody>
          ${items.length === 0 ? html`<tr><td colSpan=${5} class="empty">还没有 combo</td></tr>`
            : items.map(c => html`
            <tr key=${c.id}>
              <td class="mono">combo:${c.slug}</td>
              <td>${c.name}</td>
              <td>
                ${(c.nodes || []).map((n, i) => html`
                  <div key=${i} class="mono" style=${{ fontSize: 12 }}>
                    ${i + 1}. ${n.provider} / ${n.model}
                  </div>
                `)}
              </td>
              <td>${c.enabled
                ? html`<span class="badge ok">启用</span>`
                : html`<span class="badge err">禁用</span>`}</td>
              <td>
                <button class="btn danger" onClick=${async () => {
                  if (!confirm('删除该 combo？')) return;
                  await api('/api/combos/' + c.slug, { method: 'DELETE' });
                  reload();
                }}>删除</button>
              </td>
            </tr>
          `)}
        </tbody>
      </table>
    </div>
    ${show && html`<${NewComboModal} onClose=${() => setShow(false)} onCreated=${() => { setShow(false); reload(); }} />`}
  `;
}

function NewComboModal({ onClose, onCreated }) {
  const [slug, setSlug] = useState('');
  const [name, setName] = useState('');
  const [nodes, setNodes] = useState([{ provider: 'openai', model: '' }]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const submit = async (e) => {
    e.preventDefault(); setBusy(true); setErr(null);
    try {
      await api('/api/combos', { method: 'POST', body: JSON.stringify({ slug, name: name || slug, nodes }) });
      onCreated();
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  };
  const updateNode = (i, key, val) => {
    const c = [...nodes]; c[i] = { ...c[i], [key]: val }; setNodes(c);
  };
  return html`
    <div class="modal-backdrop">
      <form class="modal" onSubmit=${submit}>
        <h2>新建 Combo</h2>
        <label>Slug（客户端用 <span class="mono">model="combo:&lt;slug&gt;"</span>）</label>
        <input required value=${slug} onChange=${e => setSlug(e.target.value)} pattern="[a-z0-9_-]{1,40}" placeholder="smart" />
        <label>显示名称</label>
        <input value=${name} onChange=${e => setName(e.target.value)} placeholder="Smart fallback" />
        <label>节点（按尝试顺序）</label>
        ${nodes.map((n, i) => html`
          <div key=${i} style=${{ display: 'flex', gap: 8, marginBottom: 6 }}>
            <input style=${{ flex: 1 }} value=${n.provider} onChange=${e => updateNode(i, 'provider', e.target.value)} placeholder="provider" />
            <input style=${{ flex: 2 }} value=${n.model} onChange=${e => updateNode(i, 'model', e.target.value)} placeholder="model" required />
            <button type="button" class="btn ghost" onClick=${() => setNodes(nodes.filter((_, j) => j !== i))}>×</button>
          </div>
        `)}
        <button type="button" class="btn ghost" style=${{ marginTop: 4 }} onClick=${() => setNodes([...nodes, { provider: '', model: '' }])}>+ 添加节点</button>
        ${err && html`<div class="error-text">${err}</div>`}
        <div class="modal-actions">
          <button type="button" class="btn ghost" onClick=${onClose}>取消</button>
          <button class="btn" disabled=${busy}>${busy ? '...' : '创建'}</button>
        </div>
      </form>
    </div>
  `;
}

/* ─────────────────────────── Auto Routing ─────────────────────────── */

const SCENARIO_META = [
  { key: 'default',      label: 'Default',      desc: 'Fallback when no other scenario fires' },
  { key: 'think',        label: 'Think',        desc: 'reasoning_effort=high or thinking.enabled' },
  { key: 'long_context', label: 'Long Context', desc: '~64k+ tokens (chars/4 estimate)' },
  { key: 'vision',       label: 'Vision',       desc: 'Any image part in messages' },
  { key: 'tool_use',     label: 'Tool Use',     desc: 'Any tools array entry' },
  { key: 'web',          label: 'Web',          desc: 'Tool name matches /search|web|browse/i' },
];

function AutoRouting() {
  const [items, setItems] = useState([]);
  const [combos, setCombos] = useState([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);

  async function load() {
    setBusy(true); setErr(null);
    try {
      const [routing, comboData] = await Promise.all([
        api('/api/routing'),
        api('/api/combos').catch(() => ({ items: [] })),
      ]);
      setItems(routing.items || []);
      setCombos(comboData.items || []);
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  }

  async function save(scenario, target) {
    setBusy(true); setErr(null);
    try {
      await api('/api/routing/' + encodeURIComponent(scenario), {
        method: 'PUT',
        body: JSON.stringify({ target }),
      });
      await load();
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  }

  async function remove(scenario) {
    if (scenario === 'default') return;
    setBusy(true); setErr(null);
    try {
      await api('/api/routing/' + encodeURIComponent(scenario), { method: 'DELETE' });
      await load();
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  }

  useEffect(() => { load(); }, []);

  return html`
    <h1>Auto Routing</h1>
    <p class="muted">当客户端发送 <span class="mono">model="auto"</span> 或省略 model 时，路由会自动分类请求并使用下面配置的目标。显式指定 model 的请求会绕过此逻辑。</p>
    <div class="card">
      ${err && html`<div class="error-text" style=${{ marginBottom: 12 }}>${err}</div>`}
      <table>
        <thead><tr><th>Scenario</th><th>Description</th><th>Target</th><th></th></tr></thead>
        <tbody>
          ${SCENARIO_META.map(meta => {
            const cur = items.find(i => i.scenario === meta.key) || { target: null };
            return html`<${RoutingRow}
              key=${meta.key}
              meta=${meta}
              current=${cur}
              combos=${combos}
              onSave=${(t) => save(meta.key, t)}
              onDelete=${meta.key === 'default' ? null : () => remove(meta.key)}
              busy=${busy} />`;
          })}
        </tbody>
      </table>
    </div>
  `;
}

function RoutingRow({ meta, current, combos, onSave, onDelete, busy }) {
  const [draft, setDraft] = useState(current.target || '');
  useEffect(() => { setDraft(current.target || ''); }, [current.target]);
  const dirty = (draft.trim() || null) !== current.target;
  return html`
    <tr>
      <td><strong>${meta.label}</strong></td>
      <td class="muted">${meta.desc}</td>
      <td>
        <input type="text" placeholder="combo:slug or provider:model"
               list=${'combo-slugs-' + meta.key}
               value=${draft} onInput=${(e) => setDraft(e.target.value)} />
        <datalist id=${'combo-slugs-' + meta.key}>
          ${(combos || []).map(c => html`<option key=${c.slug} value=${'combo:' + c.slug} />`)}
        </datalist>
      </td>
      <td style=${{ whiteSpace: 'nowrap' }}>
        <button class="btn" disabled=${busy || !dirty || !draft.trim()} onClick=${() => onSave(draft.trim())}>Save</button>
        ${onDelete && current.target && html`
          <button class="btn danger" style=${{ marginLeft: 4 }} disabled=${busy} onClick=${onDelete}>Clear</button>
        `}
      </td>
    </tr>
  `;
}

/* ─────────────────────────── Usage ─────────────────────────── */

function Usage() {
  const [items, setItems] = useState([]);
  useEffect(() => { api('/api/usage/recent?limit=100').then(r => setItems(r.items)); }, []);
  return html`
    <h1>最近 100 条请求</h1>
    <div class="card">
      <table>
        <thead><tr>
          <th>时间</th><th>Provider</th><th>Model</th><th>输入</th><th>输出</th><th>费用 USD</th><th>延迟</th><th>状态</th>
        </tr></thead>
        <tbody>
          ${items.length === 0 ? html`<tr><td colSpan=${8} class="empty">还没有用量记录</td></tr>`
            : items.map(r => html`
            <tr key=${r.id}>
              <td class="muted">${new Date(r.ts).toLocaleString()}</td>
              <td>${r.provider}</td>
              <td class="mono">${r.model}</td>
              <td>${r.promptTokens}</td>
              <td>${r.completionTokens}</td>
              <td class="mono">$${(r.costMicros / 1e6).toFixed(6)}</td>
              <td>${r.latencyMs}ms</td>
              <td>${r.status === 'ok'
                ? html`<span class="badge ok">ok</span>`
                : html`<span class="badge err">${r.errorCode || 'error'}</span>`}</td>
            </tr>
          `)}
        </tbody>
      </table>
    </div>
  `;
}

/* ─────────────────────────── Invites (super-admin) ─────────────────────────── */

function Invites() {
  const [items, setItems] = useState([]);
  const [show, setShow] = useState(false);
  const [err, setErr] = useState(null);
  const reload = useCallback(() => api('/api/admin/invites').then(r => setItems(r.items)).catch(e => setErr(e.message)), []);
  useEffect(() => { reload(); }, [reload]);

  return html`
    <h1>邀请码</h1>
    <p class="muted">内测期间所有新用户必须填入有效邀请码才能注册。超管在此处铸码并分发。</p>
    <div class="toolbar">
      <button class="btn" onClick=${() => setShow(true)}>+ 铸造邀请码</button>
    </div>
    ${err && html`<div class="error-text" style=${{ marginBottom: 12 }}>${err}</div>`}
    <div class="card">
      <table>
        <thead><tr>
          <th>Code</th><th>用量</th><th>过期时间</th><th>备注</th><th>状态</th><th></th>
        </tr></thead>
        <tbody>
          ${items.length === 0 ? html`<tr><td colSpan=${6} class="empty">还没有邀请码</td></tr>`
            : items.map(c => html`
            <tr key=${c.id}>
              <td class="mono">${c.code}</td>
              <td>${c.usedCount} / ${c.maxUses}</td>
              <td class="muted">${c.expiresAt ? new Date(c.expiresAt).toLocaleString() : '永久'}</td>
              <td class="muted">${c.note || ''}</td>
              <td>${c.enabled && c.usedCount < c.maxUses
                ? html`<span class="badge ok">可用</span>`
                : html`<span class="badge err">${!c.enabled ? '禁用' : '已用完'}</span>`}</td>
              <td>
                <button class="btn ghost" onClick=${() => {
                  navigator.clipboard.writeText(c.code);
                }}>复制</button>
                ${c.enabled && html`
                  <button class="btn danger" onClick=${async () => {
                    if (!confirm('禁用后该 code 立即不可用，确定？')) return;
                    await api('/api/admin/invites/' + encodeURIComponent(c.code), { method: 'DELETE' });
                    reload();
                  }}>禁用</button>
                `}
              </td>
            </tr>
          `)}
        </tbody>
      </table>
    </div>
    ${show && html`<${NewInviteModal} onClose=${() => setShow(false)} onCreated=${() => { setShow(false); reload(); }} />`}
  `;
}

function NewInviteModal({ onClose, onCreated }) {
  const [maxUses, setMaxUses] = useState(1);
  const [expiresAt, setExpiresAt] = useState('');
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const [result, setResult] = useState(null);
  const submit = async (e) => {
    e.preventDefault(); setBusy(true); setErr(null);
    try {
      const body = { maxUses: Number(maxUses) || 1 };
      if (expiresAt) body.expiresAt = new Date(expiresAt).toISOString();
      if (note.trim()) body.note = note.trim();
      const r = await api('/api/admin/invites', { method: 'POST', body: JSON.stringify(body) });
      setResult(r);
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  };
  if (result) {
    return html`
      <div class="modal-backdrop">
        <div class="modal">
          <h2>邀请码已生成</h2>
          <p>把这个 code 发给受邀者，让他在注册时填入即可：</p>
          <div class="ok-text mono" style=${{ background: '#0d1117', padding: 12, borderRadius: 6, border: '1px solid var(--border)' }}>
            ${result.code}
          </div>
          <div class="modal-actions">
            <button class="btn ghost" onClick=${() => { navigator.clipboard.writeText(result.code); }}>复制</button>
            <button class="btn" onClick=${onCreated}>完成</button>
          </div>
        </div>
      </div>
    `;
  }
  return html`
    <div class="modal-backdrop">
      <form class="modal" onSubmit=${submit}>
        <h2>铸造新邀请码</h2>
        <label>使用次数上限</label>
        <input type="number" min="1" value=${maxUses} onChange=${e => setMaxUses(e.target.value)} />
        <label>过期时间（可选）</label>
        <input type="datetime-local" value=${expiresAt} onChange=${e => setExpiresAt(e.target.value)} />
        <label>备注（仅自己看到）</label>
        <input value=${note} onChange=${e => setNote(e.target.value)} placeholder="e.g. for 张三" />
        ${err && html`<div class="error-text">${err}</div>`}
        <div class="modal-actions">
          <button type="button" class="btn ghost" onClick=${onClose}>取消</button>
          <button class="btn" disabled=${busy}>${busy ? '...' : '生成'}</button>
        </div>
      </form>
    </div>
  `;
}

/* ─────────────────────────── Tenants (super-admin) ─────────────────────────── */

function Tenants() {
  const [items, setItems] = useState([]);
  const [editing, setEditing] = useState(null);
  const [err, setErr] = useState(null);
  const reload = useCallback(() => api('/api/admin/tenants').then(r => setItems(r.items)).catch(e => setErr(e.message)), []);
  useEffect(() => { reload(); }, [reload]);
  return html`
    <h1>租户管理</h1>
    <p class="muted">每个注册成功的账户对应一个 tenant；这里可以查看所有 tenant 的额度使用并按需调整。</p>
    ${err && html`<div class="error-text" style=${{ marginBottom: 12 }}>${err}</div>`}
    <div class="card">
      <table>
        <thead><tr>
          <th>ID</th><th>名称</th><th>Plan</th><th>用户</th><th>API Keys</th><th>连接</th>
          <th>今日请求</th><th>今日 Tokens</th><th></th>
        </tr></thead>
        <tbody>
          ${items.length === 0 ? html`<tr><td colSpan=${9} class="empty">还没有租户</td></tr>`
            : items.map(t => html`
            <tr key=${t.id}>
              <td class="mono">${t.id}</td>
              <td>${t.name}</td>
              <td><span class="badge ok">${t.plan}</span></td>
              <td>${t.userCount}</td>
              <td>${t.activeKeys}</td>
              <td>${t.activeConnections}</td>
              <td>${t.quota.usage.requests.toLocaleString()} / ${t.quota.effective.requests?.toLocaleString() || '∞'}</td>
              <td>${t.quota.usage.tokens.toLocaleString()} / ${t.quota.effective.tokens?.toLocaleString() || '∞'}</td>
              <td><button class="btn ghost" onClick=${() => setEditing(t)}>调整额度</button></td>
            </tr>
          `)}
        </tbody>
      </table>
    </div>
    ${editing && html`<${EditQuotaModal} value=${editing} onClose=${() => setEditing(null)}
                                          onSaved=${() => { setEditing(null); reload(); }} />`}
  `;
}

function EditQuotaModal({ value, onClose, onSaved }) {
  const [tokens, setTokens] = useState(value.quota.dailyTokenLimit ?? '');
  const [requests, setRequests] = useState(value.quota.dailyRequestLimit ?? '');
  const [note, setNote] = useState(value.quota.note || '');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const submit = async (e) => {
    e.preventDefault(); setBusy(true); setErr(null);
    try {
      const body = {
        dailyTokenLimit:   tokens   === '' ? null : Number(tokens),
        dailyRequestLimit: requests === '' ? null : Number(requests),
        note: note.trim() || null,
      };
      await api('/api/admin/tenants/' + value.id + '/quota', {
        method: 'PUT', body: JSON.stringify(body),
      });
      onSaved();
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  };
  return html`
    <div class="modal-backdrop">
      <form class="modal" onSubmit=${submit}>
        <h2>调整 tenant #${value.id} 的额度</h2>
        <p class="muted" style=${{ marginTop: 0 }}>
          留空 = 走 plan「${value.plan}」默认 (tokens ${value.quota.defaults.tokens ?? '∞'} / requests ${value.quota.defaults.requests ?? '∞'})
        </p>
        <label>每日 Token 上限（留空走 plan 默认）</label>
        <input type="number" min="0" value=${tokens} onChange=${e => setTokens(e.target.value)} />
        <label>每日请求上限（留空走 plan 默认）</label>
        <input type="number" min="0" value=${requests} onChange=${e => setRequests(e.target.value)} />
        <label>备注</label>
        <input value=${note} onChange=${e => setNote(e.target.value)} placeholder="e.g. VIP" />
        ${err && html`<div class="error-text">${err}</div>`}
        <div class="modal-actions">
          <button type="button" class="btn ghost" onClick=${onClose}>取消</button>
          <button class="btn" disabled=${busy}>${busy ? '...' : '保存'}</button>
        </div>
      </form>
    </div>
  `;
}

/* ─────────────────────────── App root ─────────────────────────── */

function App() {
  const { session, loading, signupBonus, clearSignupBonus } = useAuth();
  const [page, setPage] = useState('overview');
  if (loading) return html`<div class="auth-page"><div class="muted">加载中...</div></div>`;
  if (!session) {
    return html`
      <${AuthPage}/>
      ${signupBonus && html`<${SignupBonusModal} value=${signupBonus} onClose=${clearSignupBonus} />`}
    `;
  }
  let body;
  switch (page) {
    case 'api-keys':    body = html`<${ApiKeys}/>`; break;
    case 'connections': body = html`<${Connections}/>`; break;
    case 'combos':      body = html`<${Combos}/>`; break;
    case 'routing':     body = html`<${AutoRouting}/>`; break;
    case 'usage':       body = html`<${Usage}/>`; break;
    case 'invites':     body = html`<${Invites}/>`; break;
    case 'tenants':     body = html`<${Tenants}/>`; break;
    default:            body = html`<${Overview}/>`; break;
  }
  return html`
    <${Shell} current=${page} onNav=${setPage}>${body}<//>
    ${signupBonus && html`<${SignupBonusModal} value=${signupBonus} onClose=${clearSignupBonus} />`}
  `;
}

createRoot(document.getElementById('root')).render(html`<${AuthProvider}><${App}/><//>`);
