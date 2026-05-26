-- 0005_tenant_quotas.sql — per-tenant daily token / request caps.
-- NULL columns mean "fall back to the plan default" hardcoded in
-- router/middleware/quota.js (PLAN_QUOTAS).

CREATE TABLE tenant_quotas (
  tenant_id            BIGINT PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  daily_token_limit    BIGINT,
  daily_request_limit  INT,
  note                 TEXT,
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
