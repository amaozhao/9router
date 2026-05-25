# 9router Cloud — Multi-tenant SaaS

云上运行的多租户 9router 改造版。沿用 9router 原项目的 `open-sse/` 引擎做核心代理路由与协议转换；新增以下能力：

| 能力 | 实现位置 |
|---|---|
| **租户隔离**（所有数据按 `tenant_id` 隔离） | Postgres schema + Repository 层 |
| **API Key 鉴权**（`sk-tenant-xxx`） | `cloud/router/src/middleware/edgeAuth.ts` |
| **多账号 round-robin + cooldown**（Redis 共享状态） | `cloud/router/src/services/accountPicker.ts` |
| **凭证 KMS 加密**（envelope encryption） | `cloud/shared/src/crypto.ts` |
| **用量事件 + 计费** | `cloud/router/src/services/usage.ts` |
| **Token Refresher worker** | `cloud/worker/src/tokenRefresher.ts`（Phase 4） |
| **Admin Dashboard** | `cloud/admin/`（Phase 2.5+） |

## 目录结构

```
cloud/
├── docker-compose.yml          本地 Postgres + Redis
├── migrations/                 SQL 迁移脚本
├── shared/                     共享库（db pool、redis、crypto、types）
├── router/                     代理路由服务（核心运行时）
├── admin/                      Admin Dashboard（Next.js）
├── worker/                     后台 worker（token 刷新、用量聚合）
└── scripts/                    种子数据、运维脚本
```

## 快速开始

```bash
# 1. 起本地依赖
docker compose -f cloud/docker-compose.yml up -d

# 2. 运行迁移
cd cloud/shared && npm install
node cloud/scripts/migrate.mjs

# 3. 种子数据
node cloud/scripts/seed.mjs

# 4. 启动 router
cd cloud/router && npm install && npm run dev
# 默认监听 http://localhost:30100

# 5. 测试请求
curl http://localhost:30100/v1/chat/completions \
  -H "Authorization: Bearer sk-9r-seed-test-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-4.6","messages":[{"role":"user","content":"hello"}]}'
```

## 设计要点

详见 [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md)。
