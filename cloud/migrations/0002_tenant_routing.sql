-- tenant_routing: per-tenant scenario→target table for auto model switching.
-- target is a free-form string consumed by router/services/combo.js#resolveAttempts:
--   - 'combo:<slug>' resolves to the tenant's combo fallback chain
--   - '<provider>:<model>' resolves to a single attempt
-- scenario is restricted to the 6 hardcoded scenarios computed by
-- router/services/scenarioClassifier.js.

CREATE TABLE tenant_routing (
  tenant_id   BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  scenario    TEXT NOT NULL
    CHECK (scenario IN ('default','think','long_context','vision','tool_use','web')),
  target      TEXT NOT NULL,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, scenario)
);

CREATE INDEX idx_tenant_routing_tenant ON tenant_routing(tenant_id);

-- routed_model: the actually-used model string after auto-classification.
-- NULL when the client sent an explicit non-auto model (i.e. classifier was bypassed).
ALTER TABLE usage_events ADD COLUMN routed_model TEXT NULL;
