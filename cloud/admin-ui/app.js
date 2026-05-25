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
  const signup = async (email, password, tenantName) => {
    const r = await api('/auth/signup', { method: 'POST', body: JSON.stringify({ email, password, tenantName }) });
    localStorage.setItem('token', r.token);
    setSession({ user: r.user, tenant: r.tenant });
  };
  const logout = () => { localStorage.removeItem('token'); setSession(null); };

  return html`<${AuthCtx.Provider} value=${{ session, loading, login, signup, logout }}>${children}<//>`;
}

const useAuth = () => useContext(AuthCtx);

/* ─────────────────────────── Auth page (login/signup) ─────────────────────────── */

function AuthPage() {
  const [mode, setMode] = useState('login');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [tenantName, setTenantName] = useState('');
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);
  const { login, signup } = useAuth();

  const submit = async (e) => {
    e.preventDefault();
    setErr(null); setBusy(true);
    try {
      if (mode === 'login') await login(email, password);
      else await signup(email, password, tenantName || undefined);
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  };

  return html`
    <div class="auth-page">
      <form class="auth-card" onSubmit=${submit}>
        <h1>${mode === 'login' ? '登录 9Router Cloud' : '创建账户'}</h1>
        <p>${mode === 'login' ? '用 email + 密码登录' : '注册即自动创建你的租户 workspace'}</p>
        <label>Email</label>
        <input type="email" required value=${email} onChange=${e => setEmail(e.target.value)} />
        <label>密码（≥ 8 位）</label>
        <input type="password" required minLength=${8} value=${password} onChange=${e => setPassword(e.target.value)} />
        ${mode === 'signup' && html`
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

/* ─────────────────────────── Dashboard shell ─────────────────────────── */

function Shell({ children, current, onNav }) {
  const { session, logout } = useAuth();
  const items = [
    { key: 'overview',    label: '概览' },
    { key: 'api-keys',    label: 'API Keys' },
    { key: 'connections', label: 'Provider 连接' },
    { key: 'combos',      label: 'Combo 链' },
    { key: 'usage',       label: '用量' },
  ];
  return html`
    <div class="app">
      <aside class="sidebar">
        <div class="brand">9router · cloud</div>
        <nav>
          ${items.map(i => html`
            <a key=${i.key} class=${current === i.key ? 'active' : ''} onClick=${() => onNav(i.key)}>${i.label}</a>
          `)}
        </nav>
        <div class="me">
          <div class="email">${session.user.email}</div>
          <div>租户：${session.tenant.name} · plan: ${session.tenant.plan}</div>
          <button onClick=${logout} style=${{ marginTop: 8 }}>登出</button>
        </div>
      </aside>
      <main class="main">${children}</main>
    </div>
  `;
}

/* ─────────────────────────── Overview ─────────────────────────── */

function Overview() {
  const [data, setData] = useState(null);
  useEffect(() => { api('/api/usage/summary?hours=24').then(setData).catch(console.error); }, []);
  const totals = (data?.items || []).reduce((a, r) => ({
    req: a.req + r.requestCount,
    err: a.err + r.errorCount,
    pt:  a.pt  + r.promptTokens,
    ct:  a.ct  + r.completionTokens,
    cost: a.cost + r.costUsd,
  }), { req: 0, err: 0, pt: 0, ct: 0, cost: 0 });

  return html`
    <h1>过去 24 小时</h1>
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
          <th>Provider</th><th>名称</th><th>认证</th><th>Base URL</th><th>权重</th><th>状态</th><th></th>
        </tr></thead>
        <tbody>
          ${items.length === 0 ? html`<tr><td colSpan=${7} class="empty">还没有 provider 连接</td></tr>`
            : items.map(c => html`
            <tr key=${c.id}>
              <td><b>${c.provider}</b></td>
              <td>${c.name}</td>
              <td><span class="badge ${c.authType === 'oauth' ? 'warn' : 'ok'}">${c.authType}</span></td>
              <td class="mono muted">${c.metadata?.base_url || '(provider 默认)'}</td>
              <td>${c.weight}</td>
              <td>${c.enabled
                ? html`<span class="badge ok">启用</span>`
                : html`<span class="badge err">禁用</span>`}</td>
              <td>
                <button class="btn ghost" onClick=${async () => {
                  await api('/api/connections/' + c.id, { method: 'PATCH', body: JSON.stringify({ enabled: !c.enabled }) });
                  reload();
                }}>${c.enabled ? '禁用' : '启用'}</button>
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
  `;
}

function NewConnModal({ onClose, onCreated }) {
  const [provider, setProvider] = useState('openai');
  const [name, setName] = useState('');
  const [baseUrl, setBaseUrl] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [weight, setWeight] = useState(1);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const submit = async (e) => {
    e.preventDefault(); setBusy(true); setErr(null);
    try {
      await api('/api/connections', {
        method: 'POST',
        body: JSON.stringify({
          provider, name, authType: 'api_key',
          credentials: { api_key: apiKey },
          metadata: baseUrl ? { base_url: baseUrl } : {},
          weight: Number(weight) || 1,
        }),
      });
      onCreated();
    } catch (e) { setErr(e.message); }
    finally { setBusy(false); }
  };
  return html`
    <div class="modal-backdrop">
      <form class="modal" onSubmit=${submit}>
        <h2>添加 Provider 连接</h2>
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

/* ─────────────────────────── App root ─────────────────────────── */

function App() {
  const { session, loading } = useAuth();
  const [page, setPage] = useState('overview');
  if (loading) return html`<div class="auth-page"><div class="muted">加载中...</div></div>`;
  if (!session) return html`<${AuthPage}/>`;
  let body;
  switch (page) {
    case 'api-keys':    body = html`<${ApiKeys}/>`; break;
    case 'connections': body = html`<${Connections}/>`; break;
    case 'combos':      body = html`<${Combos}/>`; break;
    case 'usage':       body = html`<${Usage}/>`; break;
    default:            body = html`<${Overview}/>`; break;
  }
  return html`<${Shell} current=${page} onNav=${setPage}>${body}<//>`;
}

createRoot(document.getElementById('root')).render(html`<${AuthProvider}><${App}/><//>`);
