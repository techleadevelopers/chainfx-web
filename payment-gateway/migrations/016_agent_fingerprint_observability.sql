ALTER TABLE api_request_logs
  ADD COLUMN IF NOT EXISTS agent_id TEXT,
  ADD COLUMN IF NOT EXISTS agent_signature_hash TEXT;

ALTER TABLE mcp_tool_logs
  ADD COLUMN IF NOT EXISTS agent_id TEXT,
  ADD COLUMN IF NOT EXISTS agent_signature_hash TEXT;

CREATE INDEX IF NOT EXISTS idx_api_request_logs_agent
  ON api_request_logs(agent_id, created_at DESC)
  WHERE agent_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_mcp_tool_logs_agent
  ON mcp_tool_logs(agent_id, created_at DESC)
  WHERE agent_id IS NOT NULL;
