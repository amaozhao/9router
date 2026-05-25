# 9router Cloud — Multi-tenant SaaS

云端多租户版 9router。沿用 9router 的协议路由、RTK 压缩、provider executor 等无状态核心理念，新增完整的租户隔离、加密凭证、Redis 化负载均衡、独立计费等 SaaS 能力。

## 一句话说清楚架构

```
              ┌───────────────────────────┐
   client →  │  Edge Auth                 │  sk-9r-xxx → tenant
   (Claude   │  Rate Limit                │  Redis cache (<1ms)
   Code /    │  Combo / Account Picker    │  fallback + round-robin
   Codex /   │  Translator (Anthropic↔   │  Redis-backed cursors & cooldowns
   OpenCode) │   OpenAI when needed)      │
              │  Upstream call             │  per-tenant decrypted creds
              │  Usage event               │  → Postgres
              └───────────────────────────┘
                     ▲
                     │
         ┌───────────┴───────────┐
         ▼                       ▼
   ┌──────────┐           ┌───────────────┐
   │ Postgres │           │     Redis     │
   │ 11 表   │           │  hot path     │
   └──────────┘           └───────────────┘
         ▲                       ▲
         │                       │
   ┌─────┴────┐          ┌──────┴───────┐
   │  Admin   │          │   Worker     │
   │  REST    │          │  - aggregator│
   │  (JWT)   │          │  - refresher │
   └──────────┘          └──────────────┘
         ▲
         │  fetch / Bearer JWT
         │
   ┌─────┴────┐
   │ admin-ui │  Single-file React SPA (esm.sh, no build)
   └──────────┘
```

## 服务端口

| 服务 | 端口 | 进程入口 | 说明 |
|---|---|---|---|
| **Router** | 30100 | `cloud/router/src/server.js` | 客户端入口：`/v1/chat/completions`、`/v1/messages` |
| **Admin REST** | 30200 | `cloud/admin/src/server.js` | 注册/登录、API Key、Connection、Combo、Usage、OAuth |
| **Admin UI** | 30300 | `cloud/admin-ui/server.mjs` | 单文件 React SPA |
| **Worker** | (none) | `cloud/worker/src/index.js` | usage 聚合 + OAuth token 刷新 |
| Postgres | 55432 | docker | `cloud/docker-compose.yml` |
| Redis | 56379 | docker | 同上 |

## 一句话快速跑起来

```bash
# 1. 起依赖
docker compose -f cloud/docker-compose.yml up -d

# 2. 设环境变量（每个进程都要）
export DATABASE_URL='postgres://router:router_dev_pw@localhost:55432/router'
export REDIS_URL='redis://localhost:56379/0'
export CLOUD_MASTER_KEY="$(openssl rand -base64 32)"
export JWT_SECRET="$(openssl rand -hex 32)"

# 3. 初始化 schema
cd cloud/scripts && npm install
node migrate.mjs

# 4. 安装并启动四个服务（分别开终端）
( cd cloud/shared    && npm install )
( cd cloud/admin     && npm install && node src/server.js )    # :30200
( cd cloud/router    && npm install && node src/server.js )    # :30100
( cd cloud/worker    && npm install && node src/index.js  )    # background
( cd cloud/admin-ui  && node server.mjs )                       # :30300

# 5. 打开 dashboard
open http://localhost:30300
```

## 数据模型

11 张表，全部业务表都有 `tenant_id` 外键：

| 表 | 用途 | 关键字段 |
|---|---|---|
| `tenants` | 租户 | id, name, plan, status |
| `users` | 用户 | tenant_id, email, password_hash, role |
| `api_keys` | 客户端 key | tenant_id, key_hash, key_prefix, rate_limit_rpm |
| `connections` | 上游凭证 | tenant_id, provider, name, auth_type, credentials_encrypted (KMS-envelope), oauth_expires_at |
| `combos` | fallback 链 | tenant_id, slug, nodes (JSONB) |
| `aliases` | 模型别名 | tenant_id, alias → {provider, model} |
| `disabled_models` | 禁用列表 | tenant_id, provider, model |
| `pricing` | 计价表 | tenant_id (nullable=global), provider, model, *_micros_per_token |
| `tenant_settings` | k/v 配置 | tenant_id |
| `usage_events` | 用量事件流 | tenant_id, provider, model, tokens, cost_micros, status, latency_ms |
| `usage_summaries` | 小时聚合 | (tenant_id, bucket_hour, provider, model) |

## 关键能力 - 验证状态

| 能力 | 实现位置 | 验证测试 |
|---|---|---|
| **客户端 sk-9r-** → tenant 映射 | `router/middleware/edgeAuth.js` | ✅ 401 测试覆盖 |
| **每分钟 rate limit** | `router/middleware/rateLimit.js` | ✅ 第 4 个请求 429 |
| **Combo fallback** | `router/services/combo.js` + `routes/chatCompletions.js` | ✅ flaky→500→mock→200，cooldown 落 Redis |
| **多账号 round-robin** | `router/services/accountPicker.js` | ✅ A B C A B C；禁用 B 后 A C A C |
| **自动 cooldown** | `router/services/accountPicker.js` | ✅ 指数 backoff，Redis EX |
| **OpenAI /v1/chat/completions** | `router/routes/chatCompletions.js` | ✅ 流式 + 非流式 |
| **Anthropic /v1/messages** | `router/routes/messages.js` + `router/translator/anthropic.js` | ✅ 流式 + 非流式 + x-api-key |
| **KMS-style 凭证加密** | `shared/crypto.js` (HKDF + AES-256-GCM) | ✅ 跨租户解密失败 |
| **OAuth 注册流程 (PKCE)** | `admin/routes/oauth.js` | ✅ mock provider 端到端 |
| **OAuth 自动刷新** | `worker/jobs/tokenRefresher.js` | ✅ access_token 轮换 + 加密重写 |
| **Usage 小时聚合** | `worker/jobs/usageAggregator.js` | ✅ 6 events → 4 buckets, error_count 正确 |
| **Per-tenant 隔离** | 全部 SQL 都带 `WHERE tenant_id = $N` | ✅ 跨租户查询不可见 |
| **Per-tenant 计费** | `router/services/usage.js` + `getPricing()` | ✅ cost_micros = pt × $/tok + ct × $/tok |
| **Per-tenant 自动模型切换 (model=auto)** | `router/services/scenarioClassifier.js` + `scenarioRouter.js` + `admin/routes/routing.js` | ✅ `scripts/verify-auto-routing.sh` 12 个断言 |
| **管理 UI** | `admin-ui/` | ✅ HTTP + CORS |

## 客户端接入示例

### Claude Code
```
Endpoint:    http://localhost:30100/v1
Header:      x-api-key: sk-9r-xxxxxxxx
Model:       combo:claude-fallback   (or anthropic:claude-3-5-sonnet)
```

### Codex / OpenCode / Cursor / Cline
```
Endpoint:    http://localhost:30100/v1
Auth:        Authorization: Bearer sk-9r-xxxxxxxx
Model:       combo:smart   (or openai:gpt-4 / glm:glm-4.6 / ...)
```

### Combo `smart` 示例
```json
{
  "slug": "smart",
  "nodes": [
    {"provider": "anthropic", "model": "claude-3-5-sonnet-20241022"},
    {"provider": "openai",    "model": "gpt-4"},
    {"provider": "glm",       "model": "glm-4.6"}
  ]
}
```
路由顺序尝试每个 provider，遇 429/5xx 自动 cooldown + 切换。

## Auto Routing — 一次配置,按场景自动切换模型

当客户端发请求时传 `model="auto"`(或省略),路由层会按请求内容自动分类到 6 个场景之一,然后查该租户预配置的 `场景 → target` 映射。其他显式 model 取值(`combo:slug`、`provider:model`)完全不走分类器。

### 场景分类(服务端硬编码,优先级首个命中获胜)

| 顺序 | 场景 | 判定 |
|---|---|---|
| 1 | `web` | `tools` 数组中 name 匹配 `/search\|web\|browse/i` |
| 2 | `tool_use` | `tools` 数组非空 |
| 3 | `vision` | 任意 message content 是 `image` / `image_url` / `image/*` |
| 4 | `long_context` | 估算 prompt tokens > 64000 (chars/4) |
| 5 | `think` | `reasoning_effort∈{high,max}` 或 `thinking.enabled=true` |
| 6 | `default` | 兜底 |

### 配置(Admin UI 或 REST)

```bash
# 设 default 场景
curl -X PUT http://localhost:30200/api/routing/default \
  -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"target":"combo:smart"}'

# 设 long_context 场景
curl -X PUT http://localhost:30200/api/routing/long_context \
  -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"target":"anthropic:claude-3-5-sonnet-20241022"}'
```

target 必须是 `combo:<slug>` 或 `<provider>:<model>` 格式。`combo:<slug>` 在 PUT 时会校验该 combo 在本租户存在且 enabled。

### 未配置场景的兜底

- 该 scenario 未配 → 自动用该租户的 `default` 配置
- `default` 也未配 → 请求返回 400 `Tenant has no default auto-routing target. Configure it in the dashboard.`

### 审计

`usage_events.routed_model` 列在客户端传 `model=auto` 时记录"实际选中的 target",显式 model 时为 NULL。

## 安全模型

| 资产 | 保护方式 |
|---|---|
| 用户密码 | scrypt N=16384 |
| Dashboard 会话 | HS256 JWT，12h TTL |
| 客户端 API key | 仅存 sha256，明文只在创建时返回一次 |
| 上游 OAuth/api_key | KMS envelope encryption（HKDF over `CLOUD_MASTER_KEY` 派生 per-tenant DEK），AES-256-GCM |
| 租户隔离 | 所有 SQL `WHERE tenant_id` + 解密用 tenant-specific DEK（跨租户解密会 GCM 校验失败） |
| OAuth state | Redis 单次性 token，10 分钟 TTL，用过即删 |
| 速率限制 | Redis 固定窗口（per-key + per-tenant plan） |

## 生产化清单（已具备 / 缺失）

✅ 已具备：
- 多租户数据隔离
- KMS-style 凭证加密
- 完整 OAuth 流程（PKCE）
- 后台 token 刷新
- 用量记录 + 小时聚合
- Rate limit + 自动 cooldown
- Combo fallback 链
- Anthropic + OpenAI 双协议

🔧 上生产前还需补：
- 把 `CLOUD_MASTER_KEY` 换成真 AWS/GCP KMS（`shared/crypto.js` 接口已抽象好）
- 把 `usage_events` 切到 Kafka/SQS + 独立 aggregator（接口契约已就位）
- Postgres connection pool 调优 + read replica
- TimescaleDB hypertable 给 `usage_events`（migration 已留 hook 位置）
- Prometheus / OpenTelemetry 埋点
- Stripe billing 集成（结构上：从 usage_summaries → invoice）
- 多区域部署 + 跨区灾备
- SSO / SAML（admin 端可加）
- PII 清洗 / prompt 审计（如客户合规要求）

## 目录结构

```
cloud/
├── docker-compose.yml             # Postgres + Redis 本地依赖
├── .env.example                   # 所需环境变量
├── migrations/                    # SQL 迁移
│   └── 0001_init.sql
├── scripts/                       # 一次性 + 测试脚本
│   ├── migrate.mjs                # 幂等 runner
│   ├── seed.mjs                   # 1 个 dev tenant
│   ├── fake-upstream.mjs          # OpenAI 兼容 mock
│   ├── flaky-upstream.mjs         # 总是 500，用于测 fallback
│   ├── labeled-upstream.mjs       # 多实例，用于测 round-robin
│   └── mock-oauth-provider.mjs    # PKCE mock provider
├── shared/                        # 共享库（db / redis / crypto / logger / errors / config）
├── router/                        # 客户端入口服务 (30100)
│   ├── middleware/
│   │   ├── edgeAuth.js
│   │   └── rateLimit.js
│   ├── services/
│   │   ├── accountPicker.js       # Redis-based round-robin + cooldown
│   │   ├── combo.js               # combo:<slug> 解析
│   │   └── usage.js               # usage 事件 + 计费
│   ├── providers/
│   │   └── openaiCompatible.js    # undici 流式 + 非流式
│   ├── translator/
│   │   └── anthropic.js           # Anthropic ↔ OpenAI 互转
│   └── routes/
│       ├── chatCompletions.js
│       └── messages.js
├── admin/                         # 管理 REST (30200)
│   ├── lib/
│   │   ├── password.js            # scrypt
│   │   ├── jwt.js                 # HS256
│   │   ├── http.js                # router helpers
│   │   └── oauthProviders.js      # OAuth provider registry
│   ├── middleware/
│   │   └── sessionAuth.js
│   └── routes/
│       ├── auth.js                # signup/login/me
│       ├── apiKeys.js
│       ├── connections.js
│       ├── combos.js
│       ├── usage.js
│       └── oauth.js               # /api/oauth/{provider}/start + callback
├── admin-ui/                      # 静态 SPA (30300)
│   ├── index.html
│   ├── app.js                     # React + htm，无 build
│   ├── styles.css
│   └── server.mjs
└── worker/                        # 后台 jobs
    └── src/
        ├── index.js
        └── jobs/
            ├── usageAggregator.js
            ├── tokenRefresher.js
            └── refreshers/
                └── openaiOAuth.js
```

## 与原 9router 的对比

| 维度 | 原 9router (单租户本地) | 9router cloud (多租户云端) |
|---|---|---|
| 存储 | SQLite (4 driver) | Postgres |
| 凭证 | 明文 JSON | KMS-envelope 加密 |
| 鉴权 | 单 dashboard JWT | 用户系统 + API key + tenant 隔离 |
| 多账号轮询 | 进程内内存 | Redis 共享 |
| Cooldown | 进程内 | Redis EX (跨实例) |
| Rate limit | 无 | 每 key + 每 plan |
| 计费 | 无 | usage_events + 小时聚合 + pricing 表 |
| Token 刷新 | 请求时懒刷 | 后台 worker + 锁 |
| MITM | 有（src/mitm/） | 砍掉（无意义） |
| Tunnel | 有 (Cloudflared/Tailscale) | 砍掉 |
| 文档站 | 同仓库 gitbook/ | 独立部署 |
| 部署 | 单进程 | 4 微服务 (router / admin / worker / ui) |

## License

继承上游 9router 项目的开源 license。
