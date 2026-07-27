---
name: ChainFX layered adversarial audit
description: Real (non-mocked) adversarial test suite covering Human Rail (Pix), Machine Layer (MCP), Custody Guard (EIP-7702/paymaster), and On-chain floor; plus a security fix found while building it.
---

## What exists
A real adversarial engine (not mocked) attacks each layer through its actual
code path:
- `internal/adversarial/` — boots the real `server.New(...)` + `workers.WorkerManager`
  against the project's live Postgres (`DATABASE_URL`) behind a real
  `httptest.Server`; concurrently attacks `POST /api/pix/webhook/buy`
  (duplicate-settlement race, forged-signature race) and `POST /mcp/tools/call`
  (anonymous-tier flood, malformed/adversarial JSON bodies). Skips (not fails)
  if Postgres is unreachable.
- `internal/workers/onchain_adversarial_test.go` — white-box tests the
  confirmation-floor clamp in `NewOnchainWorker` against attacker-controlled
  config (misconfigured/zero confirmations) — no RPC/DB needed.
- `signer/custody_guard_adversarial_test.go` — fuzzes `ExtractEIP7702Delegate`
  against malformed bytecode and races `CustodyGuard.inspectTransaction`
  under concurrent SetCode transactions to confirm lockdown can't be missed.
- `internal/adversarial/custody_guard_test.go` — races `paymaster.InMemorySigLock`
  under concurrent identical-signature submission.

## Real bug found and fixed
`handlePixWebhookBuy` only required a Pix webhook signature/hmac when
`s.cfg.IsProduction()` was true — staging/dev/test deployments (which can
hold real Efí credentials and mutate real settlement state) accepted a
**fully unauthenticated** forged settlement webhook. Fixed: the "no
signature and no hmac param at all" rejection now applies whenever a
webhook secret is configured, in every environment, not just production.
**Why:** environment name is not a security boundary — only "is a secret
configured" is. **How to apply:** if adding new webhook-style auth gates,
never key the "require signature" check off `IsProduction()`; key it off
whether a secret is configured.

## Testing pattern notes
- `signer/` is a *separate* Go module (own `go.mod`, `package main`) even
  though it declares the same module path as root — cannot be imported from
  `internal/adversarial`; adversarial tests for it must live inside `signer/`.
- `database.ConnectPostgres` already runs `InitSchema`/`EnsureBootstrapAdmin`,
  so adversarial tests needing a live DB can just call it directly against
  `DATABASE_URL` rather than hand-rolling schema setup.
- `rpc.NewPool` never dials on construction — a dummy/unreachable RPC URL is
  enough to get a network registered in `OnchainWorker.networks` for
  floor-clamp assertions without needing a real chain endpoint.
