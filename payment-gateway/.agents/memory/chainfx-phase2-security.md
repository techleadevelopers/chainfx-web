---
name: ChainFX phase-2 security hardening
description: All 8 production security fixes applied in phase 2 (2026-07); architecture decisions worth preserving across sessions.
---

## What was implemented

### 1. MCP per-API-key rate limiter (`/mcp/tools/call`)
- Sliding-window limiter in `internal/mcp/server.go` (`mcpRateLimiter` struct)
- `handleToolsCall` in `internal/mcp/tools.go` enforces it, returns 429 with `Retry-After`
- Tiers: live keys (sk_live_*) → 120/min; test keys (sk_test_*) → 40/min; anon → 5/min
- `X-RateLimit-Limit/Remaining/Reset` headers on every response
- **Why:** MCP tools invoke AI, sign transactions, and query the DB — uncapped they're DoS vectors

### 2. Prometheus-compatible metrics (`/metrics`)
- `internal/metrics/metrics.go` — stdlib only, no external deps; Prometheus text format
- Counters: `chainfx_m2m_overpayment_total`, `chainfx_m2m_overpayment_usdt_total`, `chainfx_mcp_tools_call_total{status}`, `chainfx_mcp_rate_limited_total`, `chainfx_webhook_delivery_failure_total`
- Gauge: `chainfx_onchain_confirmation_floor{network}`
- `GET /metrics` route added to `internal/server/router.go`, protected by `authorizeAdmin`
- Handler at `internal/server/metrics_handler.go`
- **Why:** No external prometheus client dep needed; critical for overpayment alerting

### 3. Float64 → fixed-point decimal for M2M calculations
- `toolCreateM2MPaymentIntent` in `internal/mcp/tools.go` now uses `internal/money` package
- `money.ParseMoney(amountBRLStr)` → `MoneyMinor` (int64); `money.RateFromFloat(usdtRate)` → `RateDecimal` (int64)
- `money.TokensFromFiat()` + `money.TokenFeeBps()` for exact integer arithmetic
- Results converted back to float64 only for DB persistence (NUMERIC(28,8))
- `stringFromMap()` alias added (was referenced but undefined — alias for `stringArg`)
- **Why:** `grossUSDT = amountBRL / usdtRate` in float64 accumulates cents of error per transaction

### 4. On-chain confirmation floor
- `internal/workers/onchain.go`: BSC floor = 3, Polygon floor = 64 (constants `minSafeBSCConfirmations`, `minSafePolygonConfirmations`)
- Enforced AFTER reading env config — env vars cannot go below the floor
- `metrics.SetOnchainConfirmationFloor()` called at startup to export to Prometheus
- **Why:** Config file typo (BSC_MIN_CONFIRMATIONS=1) could enable double-spend via reorgs

### 5. Webhook IDOR fix — full implementation
- `internal/database/webhooks.go`: `CreateWebhookSubscription` now takes `agentKeyHash, createdBy string`
- New `scanWebhookSubscriptionFull` for 15-column result (adds `agent_api_key_hash`, `created_by`)
- New `ListWebhookSubscriptionsByAgent(ctx, agentKeyHash)` — scoped query for MCP
- `internal/mcp/tools.go`: `toolCreateWebhookSubscription` stores `fullMCPSecretHash(apiKey)` at creation
- `list_webhook_subscriptions` case uses `ListWebhookSubscriptionsByAgent` — no cross-agent leakage
- API key injected into context via `mcpAPIKeyCtxKey{}` in `handleToolsCall` for access inside tools
- Non-MCP callers (web UI, registry): pass `"", "web"` — still works, no scoping
- **Why:** Before this, any MCP agent could enumerate ALL webhook target URLs via list

### 6. DB migration `migrations/004_security_idor.sql`
- `webhook_subscriptions`: adds `agent_api_key_hash VARCHAR(64)`, `created_by VARCHAR(64)`, two indexes
- `swaps`: FK constraints `fk_swaps_from_asset` and `fk_swaps_to_asset` → `assets(symbol)` PRIMARY KEY
  - **IMPORTANT**: assets PK is `symbol VARCHAR(16)`, NOT an id column — use `REFERENCES assets(symbol)`
- `assets`: `CHECK (symbol = UPPER(symbol))`
- `kyc_requests`: VARCHAR(2048) caps on URL fields
- `webhook_subscriptions.target_url`: VARCHAR(2048) cap
- Fully idempotent (IF NOT EXISTS guards everywhere), wrapped in BEGIN/COMMIT

### 7. k6 stress test — `tests/stress_production.js`
- Two scenarios: `baseline` (50 rps, 2 min) and `spike` (ramp to 200 rps)
- Hits: `/agent/v1/capabilities`, `/mcp/tools/call`, `/api/mobile/assets`, `/metrics`
- Custom metrics: `chainfx_rate_limited` (rate), `chainfx_mcp_duration` (trend), `chainfx_overpayment_alerts` (counter)
- SLOs: p95 < 300ms, error < 0.01%, rate-limited < 5%
- Writes `tests/stress_results.json` via `handleSummary`

## Key constraints for future work

- `fullMCPSecretHash` = full 64-char SHA-256 hex (for DB storage); `shortMCPSecretHash` = 16-char prefix (for logs)
- `mcpAPIKeyCtxKey{}` is the context key — always inject in the HTTP handler before calling tools
- The `money` package (`internal/money/money.go`) provides all fixed-point math needed — no shopspring/decimal required
- Migration 004 must run before any MCP webhook listing code hits production — otherwise COALESCE fallbacks handle missing columns gracefully
- `stringFromMap` is an alias for `stringArg` — keep them in sync
