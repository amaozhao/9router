-- 0004_invite_codes.sql — invite-code gate for self-service signup.
-- Super-admins mint codes; signup must consume an unused, unexpired code.

CREATE TABLE invite_codes (
  id          BIGSERIAL PRIMARY KEY,
  code        TEXT NOT NULL UNIQUE,
  max_uses    INT NOT NULL DEFAULT 1,
  used_count  INT NOT NULL DEFAULT 0,
  expires_at  TIMESTAMPTZ,
  note        TEXT,
  created_by  BIGINT REFERENCES users(id) ON DELETE SET NULL,
  enabled     BOOLEAN NOT NULL DEFAULT TRUE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Partial index for the hot path: lookup active codes by value.
CREATE INDEX invite_codes_active_idx
  ON invite_codes (code)
  WHERE enabled AND used_count < max_uses;
