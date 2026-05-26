-- 0001_init.sql — multi-tenant schema for cloud lazirouter
-- All business tables carry a tenant_id foreign key. Postgres-only.

BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ============================================================
-- Tenants & Users
-- ============================================================

CREATE TABLE tenants (
  id           BIGSERIAL PRIMARY KEY,
  name         TEXT NOT NULL,
  plan         TEXT NOT NULL DEFAULT 'free',
  status       TEXT NOT NULL DEFAULT 'active', -- active | suspended | deleted
  meta         JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
  id              BIGSERIAL PRIMARY KEY,
  tenant_id       BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  email           TEXT NOT NULL,
  password_hash   TEXT,
  role            TEXT NOT NULL DEFAULT 'member', -- owner | admin | member
  status          TEXT NOT NULL DEFAULT 'active',
  meta            JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_lower_unique ON users (LOWER(email));
CREATE INDEX users_tenant_idx ON users (tenant_id);

-- ============================================================
-- API Keys (client-facing; sk-lr-xxx → tenant_id)
-- ============================================================

CREATE TABLE api_keys (
  id              BIGSERIAL PRIMARY KEY,
  tenant_id       BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  created_by      BIGINT REFERENCES users(id) ON DELETE SET NULL,
  key_prefix      TEXT NOT NULL,           -- e.g. sk-lr-abcd
  key_hash        TEXT NOT NULL UNIQUE,    -- sha256(key)
  name            TEXT,
  scopes          JSONB NOT NULL DEFAULT '{}'::jsonb,
  rate_limit_rpm  INT,                     -- per-key override
  last_used_at    TIMESTAMPTZ,
  revoked_at      TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX api_keys_tenant_idx ON api_keys (tenant_id);
CREATE INDEX api_keys_active_idx ON api_keys (key_hash) WHERE revoked_at IS NULL;

-- ============================================================
-- Provider Connections (per-tenant upstream credentials)
-- ============================================================

CREATE TABLE connections (
  id                       BIGSERIAL PRIMARY KEY,
  tenant_id                BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  provider                 TEXT NOT NULL,      -- openai | anthropic | gemini | glm | minimax | ...
  name                     TEXT NOT NULL,      -- human label (e.g. "my-glm-acc-1")
  auth_type                TEXT NOT NULL,      -- api_key | oauth | passthrough
  credentials_encrypted    BYTEA NOT NULL,     -- envelope-encrypted JSON blob
  metadata                 JSONB NOT NULL DEFAULT '{}'::jsonb,
  enabled                  BOOLEAN NOT NULL DEFAULT TRUE,
  weight                   INT NOT NULL DEFAULT 1,
  oauth_expires_at         TIMESTAMPTZ,        -- for refresher worker
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, provider, name)
);
CREATE INDEX connections_tenant_provider_idx ON connections (tenant_id, provider);
CREATE INDEX connections_refresh_due_idx ON connections (oauth_expires_at) WHERE auth_type = 'oauth' AND enabled;

-- ============================================================
-- Combos (per-tenant fallback chains)
-- ============================================================

CREATE TABLE combos (
  id          BIGSERIAL PRIMARY KEY,
  tenant_id   BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  slug        TEXT NOT NULL,                   -- exposed as model="combo:<slug>"
  name        TEXT,
  nodes       JSONB NOT NULL,                  -- [{provider, model, connection_id?, weight}, ...]
  enabled     BOOLEAN NOT NULL DEFAULT TRUE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, slug)
);

-- ============================================================
-- Model aliases / disabled list / pricing
-- ============================================================

CREATE TABLE aliases (
  id          BIGSERIAL PRIMARY KEY,
  tenant_id   BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  alias       TEXT NOT NULL,
  target      JSONB NOT NULL,                  -- {provider, model}
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, alias)
);

CREATE TABLE disabled_models (
  id          BIGSERIAL PRIMARY KEY,
  tenant_id   BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  provider    TEXT NOT NULL,
  model       TEXT NOT NULL,
  reason      TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, provider, model)
);

CREATE TABLE pricing (
  id              BIGSERIAL PRIMARY KEY,
  tenant_id       BIGINT REFERENCES tenants(id) ON DELETE CASCADE, -- NULL = global default
  provider        TEXT NOT NULL,
  model           TEXT NOT NULL,
  prompt_price_micros_per_token       BIGINT NOT NULL,  -- 1e-6 USD per token
  completion_price_micros_per_token   BIGINT NOT NULL,
  effective_from  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, provider, model, effective_from)
);

-- ============================================================
-- Tenant settings (free-form k/v)
-- ============================================================

CREATE TABLE tenant_settings (
  tenant_id   BIGINT PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  data        JSONB NOT NULL DEFAULT '{}'::jsonb,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============================================================
-- Usage events (high-write; aggregated later)
-- ============================================================

CREATE TABLE usage_events (
  id                  BIGSERIAL PRIMARY KEY,
  tenant_id           BIGINT NOT NULL,
  api_key_id          BIGINT,
  connection_id       BIGINT,
  ts                  TIMESTAMPTZ NOT NULL DEFAULT now(),
  provider            TEXT NOT NULL,
  model               TEXT NOT NULL,
  upstream_model      TEXT,
  prompt_tokens       INT NOT NULL DEFAULT 0,
  completion_tokens   INT NOT NULL DEFAULT 0,
  total_tokens        INT NOT NULL DEFAULT 0,
  cost_micros         BIGINT NOT NULL DEFAULT 0,
  status              TEXT NOT NULL,           -- ok | error | cooldown_fallback
  latency_ms          INT,
  request_id          TEXT,
  error_code          TEXT,
  meta                JSONB
);
CREATE INDEX usage_events_tenant_ts_idx ON usage_events (tenant_id, ts DESC);
CREATE INDEX usage_events_request_idx ON usage_events (request_id);

-- ============================================================
-- Usage summaries (rollups for billing & dashboard)
-- ============================================================

CREATE TABLE usage_summaries (
  tenant_id          BIGINT NOT NULL,
  bucket_hour        TIMESTAMPTZ NOT NULL,
  provider           TEXT NOT NULL,
  model              TEXT NOT NULL,
  request_count      BIGINT NOT NULL DEFAULT 0,
  prompt_tokens      BIGINT NOT NULL DEFAULT 0,
  completion_tokens  BIGINT NOT NULL DEFAULT 0,
  cost_micros        BIGINT NOT NULL DEFAULT 0,
  error_count        BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, bucket_hour, provider, model)
);
CREATE INDEX usage_summaries_tenant_idx ON usage_summaries (tenant_id, bucket_hour DESC);

-- Note: schema_migrations is created and the row inserted by the migration runner.

COMMIT;
