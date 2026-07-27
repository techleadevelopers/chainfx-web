---
name: ChainFX security audit 2026-07
description: Findings and fixes from the full security audit. Open items need DB migrations to fully resolve.
---

## Fixes applied (no migration needed)
- JWT hardcoded fallback secret → panic in prod, warning in dev (mobile/server.go)
- WS /orders + /notifications had no auth → wrapped in requireAuth (mobile/server.go)
- WS order broadcast was global → scoped "orders:<uid>" per user (mobile/ws.go)
- KYC limits route public → requireAuth added (mobile/server.go)
- SSRF DNS fail-open → fail-closed in webhook URL validation (mobile/helpers_phase5.go)
- err.Error() in 500 responses → generic "erro interno" + slog.Error (5 mobile files)
- No panic recovery in goroutines → defer recover() added (onchain.go, payout.go)
- WebSocket CheckOrigin allowed all → validates ALLOWED_ORIGINS env var (ws.go)
- MCP list_webhook_subscriptions → targetUrl masked for keyless callers (mcp/tools.go)

## Open items requiring DB schema migration
1. webhook_subscriptions IDOR: no agent ownership column. Add agent_api_key_hash + filter ListWebhookSubscriptions by caller key hash.
2. MCP toolGetOrderStatus + toolGetPurchase IDOR: need agent_wallet on buy_orders/marketplace_purchases.

## Other high-priority TODOs (infra/config)
- Rate limiting on /mcp/tools/call per API key
- Overpayment Prometheus alert
- Migrate M2M math float64 → shopspring/decimal
- swaps FK constraints to assets table
- assets symbol uppercase CHECK constraint
- Min confirmation floor: BSC≥3, Polygon≥64 even if env var is lower

**Why:** Live production system with real money — these have direct financial risk.
