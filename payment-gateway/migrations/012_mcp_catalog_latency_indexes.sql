-- Reduce cold-cache latency for MCP/capability discovery endpoints.
-- These lookups are hit by GET /marketplace/capabilities/:id,
-- contract reads, and MCP list/get capability tools.

CREATE INDEX IF NOT EXISTS idx_mp_capabilities_active_id
  ON marketplace_capabilities(id)
  WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_mp_capabilities_active_slug
  ON marketplace_capabilities(slug)
  WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_mp_capability_contracts_active_capability_version
  ON marketplace_capability_contracts(capability_id, version)
  WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_mp_capability_providers_active_route
  ON marketplace_capability_providers(capability_id, routing_priority, provider_slug)
  WHERE status IN ('active', 'planned');

CREATE INDEX IF NOT EXISTS idx_mp_products_active_capability_legacy
  ON marketplace_products(capability)
  WHERE status = 'active' AND capability IS NOT NULL;
