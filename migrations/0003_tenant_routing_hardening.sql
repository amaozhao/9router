-- 0003: tighten tenant_routing.
--  (a) Drop the redundant single-column index on tenant_id — the composite
--      PK (tenant_id, scenario) already serves WHERE tenant_id = $1 queries
--      via its B-tree leading column.
--  (b) Forbid empty/whitespace-only target values at the schema layer
--      (the admin PUT route already trims and rejects empty strings, but
--      defense-in-depth keeps direct DB writes consistent).

DROP INDEX IF EXISTS idx_tenant_routing_tenant;

ALTER TABLE tenant_routing
  ADD CONSTRAINT tenant_routing_target_nonempty
  CHECK (length(trim(target)) > 0);
