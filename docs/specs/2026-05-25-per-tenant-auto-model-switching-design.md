# Per-Tenant Automatic Model Switching — Design

**Date:** 2026-05-25
**Branch:** `feat/multi-tenant-saas`
**Status:** Draft (awaiting user approval)

---

## 1. 目标

让每个租户在云端可以**配一次、永久按请求内容自动切换上游模型**:客户端只需固定传 `model=auto`(或留空),服务端按请求特征(token 量、是否有图、是否带工具、reasoning 强度等)落到该租户预设的"场景 → 模型"映射,后续 fallback / 多账号轮询沿用现有能力。

## 2. 范围

**In scope (v1):**
- 6 个固定场景: `default` / `think` / `long_context` / `vision` / `tool_use` / `web`
- 1 张新表 `tenant_routing(tenant_id, scenario, target)`
- 1 个分类器 `classifyScenario(req)`,服务端硬编码规则
- 1 个解析器 `resolveScenarioTarget(tenantId, scenario)`,带未配置 → default fallback
- `/v1/chat/completions` 和 `/v1/messages` 在 `model=auto`/`空` 时启用自动判断;其它取值客户端优先
- Admin REST: `GET/PUT /api/routing`
- Admin UI: 一节 "Auto Routing",6 行可编辑表格
- 迁移: `cloud/migrations/0002_tenant_routing.sql`
- 测试覆盖:分类器单测、解析器单测、集成测试(含跨租户隔离)

**Out of scope (v1):**
- 可调阈值(留到 v2 走"C 路径")
- `aliases` 表启用(独立工作)
- `disabled_models` 强制(独立工作)
- 基于成本/配额的预算驱动切换
- 流式优化、token 真实计数器(v1 用 `chars/4` 近似)

## 3. 架构

```
                 ┌────────────────────────┐
请求(model=auto) │ chatCompletions /      │
─────────────────▶│ messages handler       │
                 └──────────┬─────────────┘
                            │ if modelStr in (null,'','auto'):
                            ▼
                 ┌────────────────────────┐
                 │ classifyScenario(req)  │  ← 服务端硬编码
                 │  → 'long_context'      │
                 └──────────┬─────────────┘
                            ▼
                 ┌────────────────────────┐
                 │ resolveScenarioTarget  │  SELECT FROM tenant_routing
                 │  (tenantId, scenario)  │  未命中 → 递归到 default
                 │  → 'combo:long-rich'   │
                 └──────────┬─────────────┘
                            ▼
                 ┌────────────────────────┐
                 │ resolveAttempts(...)    │  ← 已存在,不改
                 └──────────┬─────────────┘
                            ▼
                  (现有 fallback + accountPicker + cooldown + usage)
```

## 4. 数据模型

### 新表 `tenant_routing`

```sql
-- cloud/migrations/0002_tenant_routing.sql
CREATE TABLE tenant_routing (
  tenant_id   BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  scenario    TEXT NOT NULL
    CHECK (scenario IN ('default','think','long_context','vision','tool_use','web')),
  target      TEXT NOT NULL,                  -- 'combo:<slug>' 或 '<provider>:<model>'
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, scenario)
);
```

设计取舍:
- **不复用 `aliases` 表**:`aliases.target` 是 `{provider, model}` JSONB,无法表达 `combo:<slug>`;且 alias 是"用户取的名字",scenario 是"系统固定的桶",混在一张表上语义会乱。
- **不强制每个 tenant 必须有所有 6 行**:未配置走 default fallback。但 `default` 没配会硬失败(后述)。
- **target 用字符串而非 JSONB**:因为下游 `resolveAttempts(tenantId, modelStr)` 已经吃字符串,新表只是给它喂入参。

### 不动既有 schema

`combos`、`connections`、`aliases`、`disabled_models` 都不改。

## 5. 分类器 (`classifyScenario`)

文件: `cloud/router/src/services/scenarioClassifier.js`

签名: `classifyScenario(requestBody, protocol) → scenario` (同步,纯函数)

**优先级链(首个命中获胜)**:

| 顺序 | 场景 | 判定条件 |
|---|---|---|
| 1 | `web` | `tools` 数组中存在 name 匹配 `/search\|web|browse/i` 的工具 |
| 2 | `tool_use` | `tools` 数组非空(否则会被上一条吃掉) |
| 3 | `vision` | 任意 message 的 content 数组里出现 `type='image'`、`type='image_url'`、或包含 `image/*` data-uri |
| 4 | `long_context` | 估算 prompt tokens > 64000(算法:`Σ JSON.stringify(content).length / 4`,跨 OpenAI/Anthropic 两种 shape 都能跑) |
| 5 | `think` | `reasoning_effort ∈ {'high','max'}` 或 `thinking.enabled === true`(分类器只会在 `model=auto`/空时运行,所以不需要看 model 字符串) |
| 6 | `default` | 兜底 |

阈值 64000 是常量,定义在文件顶部,后续可移到 `tenant_settings.data.routing.thresholds`(v2)。

**协议适配**:Anthropic body 和 OpenAI body 形状不同,分类器接收 `protocol: 'openai' | 'anthropic'` 参数走两个轻量适配器读取相同字段。

## 6. 解析器 (`resolveScenarioTarget`)

文件: `cloud/router/src/services/scenarioRouter.js`

```js
export async function resolveScenarioTarget(tenantId, scenario) {
  const row = await db.queryOne(
    'SELECT target FROM tenant_routing WHERE tenant_id = $1 AND scenario = $2',
    [tenantId, scenario]
  );
  if (row) return row.target;
  if (scenario === 'default') {
    throw new ValidationError('Tenant has no default auto-routing target. Configure it in the dashboard.');
  }
  return resolveScenarioTarget(tenantId, 'default');   // 递归一次
}
```

**缓存策略**:Redis `routing:{tenantId}` 存整张表 JSON,TTL 30s。PUT/DELETE 时主动失效。

## 7. 集成到请求路径

### `cloud/router/src/routes/chatCompletions.js`

在 `resolveApiKey` / `enforceRateLimit` 之后,`resolveAttempts` 之前插入:

```js
const rawModel = body.model;
let effectiveModel = rawModel;
if (!rawModel || rawModel === 'auto') {
  const scenario = classifyScenario(body, 'openai');
  effectiveModel = await resolveScenarioTarget(ctx.tenantId, scenario);
  log.info('ROUTING', `auto → scenario=${scenario} → ${effectiveModel}`);
}
const attempts = await resolveAttempts(ctx.tenantId, effectiveModel);
```

### `cloud/router/src/routes/messages.js`

完全对称地改一处。`protocol='anthropic'`。`anthropicBody.model` 进来是 'auto' 时同样替换。

**Usage 记录**:`recordUsage` 里 `model` 字段记 *客户端传入的原值*(`rawModel`,例如 'auto'),新增 `routed_model` 字段记录 `effectiveModel`,便于审计。这需要扩 `usage_events` 表加一列 `routed_model TEXT NULL`(在 0002 migration 一起加)。

## 8. Admin 控制面

### REST 端点 `cloud/admin/src/routes/routing.js`

| 端点 | 行为 |
|---|---|
| `GET /api/routing` | 返回 `[{scenario, target}, ...]` 6 行,缺失的填 `target: null` |
| `PUT /api/routing/:scenario` | body `{target}`,upsert;校验 scenario 合法、target 是 `combo:xxx` 或 `provider:model`、且引用的 combo/connection 在本 tenant 真实存在 |
| `DELETE /api/routing/:scenario` | 删除映射;`default` 不允许 DELETE |

所有端点都要 `requireSession()` + JWT 里取 `tid`,SQL 必带 `WHERE tenant_id = $1`。

### Admin UI (`cloud/admin-ui/app.js`)

加一节 "Auto Routing",表格:

| Scenario | Description | Target | Save |
|---|---|---|---|
| default | 兜底 | [combo dropdown / 自由输入] | [✓] |
| think | 高推理 | ... | |
| long_context | >64k tokens | ... | |
| vision | 含图片 | ... | |
| tool_use | 带工具 | ... | |
| web | 搜索类工具 | ... | |

下拉项 = 本租户的 `combos.slug` 列表 + 自由输入兜底。

## 9. 错误处理

| 情况 | 行为 |
|---|---|
| `model=auto`、tenant 没配 default | 400 `Tenant has no default auto-routing target. Configure it in the dashboard.` |
| `model=auto`、scenario 命中但该 scenario 没配 | 自动回退到 default(透明) |
| PUT 时 target 引用不存在的 combo | 400 `combo 'xxx' does not exist for this tenant` |
| PUT 时 target 语法非法 | 400 `target must be 'combo:<slug>' or '<provider>:<model>'` |
| DELETE default | 400 `default scenario cannot be deleted; update it instead` |
| 资源耗尽下游全挂(已有逻辑) | 504,已存在 |

## 10. 跨租户隔离

每条 SQL 都带 `WHERE tenant_id = $1`;Redis 缓存 key `routing:{tenantId}`;现有 `resolveAttempts` 已经按 tenant 隔离。无新增隔离面。

## 11. 测试

文件: `cloud/router/test/scenarioClassifier.test.js`、`scenarioRouter.test.js`、`cloud/admin/test/routing.test.js`,以及一组端到端脚本扩 `cloud/scripts/verify-*.mjs`。

**Unit (classifier)**:
- web 工具命中(name=`web_search`)
- 普通 tool_use 命中
- vision: OpenAI image_url / Anthropic image base64 各一例
- long_context: 超 64k 字符的 prompt
- think: `reasoning_effort=high`、`thinking.enabled=true`、model 含 `o1`
- default: 普通短文本

**Unit (resolver)**:
- 命中映射 → 返回 target
- scenario 未配 → 返回 default 的 target
- default 未配 → 抛 ValidationError
- 缓存命中/失效场景

**Integration**:
- POST `/v1/messages` model=auto + 长 prompt → 进入 long_context 桶 → 上游收到 tenant 为 long_context 配的 model
- POST `/v1/chat/completions` model=`combo:smart` → 不走分类器
- 跨租户:tenant A 的 long_context 配 anthropic,tenant B 的 long_context 配 openai → 同一长 prompt 走不同上游
- PUT 改 target → 因为 PUT 路径主动失效缓存,下一次请求立即生效
- DELETE default → 400

## 12. 迁移与回滚

**Migration `0002_tenant_routing.sql`** 包含:
1. `CREATE TABLE tenant_routing ...`
2. `ALTER TABLE usage_events ADD COLUMN routed_model TEXT NULL;`
3. (可选)给现有 tenant 自动种一行 default:`INSERT INTO tenant_routing (tenant_id, scenario, target) SELECT id, 'default', 'combo:' || (SELECT slug FROM combos c WHERE c.tenant_id = t.id AND c.enabled LIMIT 1) FROM tenants t WHERE EXISTS (SELECT 1 FROM combos WHERE tenant_id = t.id);` — 只对有 combo 的租户种。

**回滚**:`DROP TABLE tenant_routing; ALTER TABLE usage_events DROP COLUMN routed_model;` 即可。代码层移除 classifier/resolver 调用后回退到老行为(只支持 `combo:slug`/`provider:model` 显式)。

## 13. 实现单元拆分(给后续 plan 用)

每一项可独立改动、独立提交:

| # | 单元 | 文件 |
|---|---|---|
| 1 | DB migration + verify | `cloud/migrations/0002_tenant_routing.sql` |
| 2 | Classifier 纯函数 + 单测 | `router/src/services/scenarioClassifier.js` + test |
| 3 | Resolver + Redis 缓存 + 单测 | `router/src/services/scenarioRouter.js` + test |
| 4 | chatCompletions.js 集成 | `router/src/routes/chatCompletions.js` |
| 5 | messages.js 集成 + usage routed_model | `router/src/routes/messages.js` + `services/usage.js` |
| 6 | Admin REST routing CRUD | `admin/src/routes/routing.js` + 接入 server.js |
| 7 | Admin UI section | `admin-ui/app.js` |
| 8 | 集成验证脚本 | `cloud/scripts/verify-auto-routing.mjs` |
| 9 | README 更新 | `cloud/README.md` 加一节 |

## 14. 完成定义 (Definition of Done)

- 9 个单元全部提交
- 所有新增/相关测试在 CI 绿
- `node cloud/scripts/verify-auto-routing.mjs` 端到端跑通(创建 tenant → 配 6 个 scenario → 发 6 种特征请求 → 命中正确 scenario → routed_model 落库)
- Admin UI 能可视化配置 6 个 scenario
- README 新增 "Auto Routing" 一节,含使用示例
