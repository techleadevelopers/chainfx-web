---
name: ChainFX Gas Station + PSP layer
description: Architecture notes for Gas Station (paymaster), Auto-Sweeper, and PSP abstraction — completed 2026-07.
---

## Gas Station (Paymaster)

Package `internal/paymaster/` with:
- `oracle.go` — gas price oracle, queries BSC `eth_gasPrice` via rpc.Pool
- `idempotency.go` — sig_hash-based dedup backed by `gas_relay_requests` table
- `retry.go` — exponential backoff up to 4 attempts, then DLQ
- `estimator.go` — weiToGwei uses `big.Float.Quo` (not integer div) for precision; weiToGwei signature is `func weiToGwei(wei *big.Int) *big.Float`
- `batcher.go` — batch collector, 50 ms window, max 10 per batch, sends to signer
- `paymaster.go` — top-level `Service`; imports `math/big` (required for weiToGwei)

**Why:** Gasless relay for end-users; hot wallet sponsors BNB gas, charges USDT fee deducted from relay.

## Database pattern

`database.DB` struct: `SQL *sql.DB` field (NOT embedded). All queries in `gas_station.go` use:
- `db.SQL.QueryRowContext(...)`
- `db.SQL.ExecContext(...)`
- `db.SQL.QueryContext(...)`

**Why:** DB wraps sql.DB for privacy codec and config access — it does NOT embed it, so direct method calls fail at compile time.

## Worker Manager

`NewWorkerManager(db, cfg, mailer, pool *rpc.Pool)` — 4-argument signature. Pool is nil-safe; Gas Station + Auto-Sweeper self-disable gracefully when pool is nil.

`wg.Add(9)` — 9 workers total including Auto-Sweeper + Paymaster relay loop.

`workerMgr.PaymasterService` — exported field, wired to HTTP server via `api.WithPaymaster(...)` in `cmd/api/main.go`.

## HTTP routes

6 routes under `/v1/gas/`:
- `GET  /v1/gas/status`       — public, enabled flag + oracle stats
- `POST /v1/gas/quote`        — public, fee estimate
- `POST /v1/gas/relay`        — admin auth, submit relay
- `GET  /v1/gas/relay/{id}`   — public, relay status
- `GET  /v1/gas/relays`       — admin auth, relay list with stats
- `GET  /v1/gas/sweeper/runs` — admin auth, last 50 sweeper runs

Rate limiter: `AllowN(key, max)` — 3-VU burst per address (not `AllowWithLimit` which doesn't exist).
`authorizeAdmin` returns 3 values: `(*AdminUser, chainFXAuth, bool)`.

## PSP Abstraction

`internal/psp/provider.go` — `PixProvider` interface + `Router`
`internal/psp/efi_adapter.go` — Efí (Gerencianet) adapter (OAuth token cache, strconv.ParseFloat, ParseWebhookAll for multi-entry batches)

Wired end-to-end (2026-07): `cmd/api/main.go` builds the EfiAdapter + Router
(nil when Efí creds/cert aren't configured) and calls `api.WithPSP(router)` +
sets `workerMgr.PSPRouter`. `handlePixWebhookBuy` in settlement_handlers.go
branches on `s.pspRouter != nil` — routes through `ParseWebhookAll` and applies
every batched PIX event as an independent buy-order settlement; falls back to
the legacy inline single-entry parser when no Router is wired (backward-compat,
covered by existing tests that construct `&Server{}` without a router).
`WorkerManager` runs a ticker-based `ProbeAll` health probe (1 min interval,
semaphore of 10) only when `PSPRouter != nil`.

Client-cert loading (PEM or base64 PKCS#12) was extracted to `internal/certutil`
so both the direct-charge HTTP client (`internal/server/payment_provider.go`)
and the PSP adapter construction in main.go share one decoder — do not
reintroduce a second copy of the pkcs12 logic.

## Migration

`migrations/005_gas_station.sql` — tables: `gas_relay_requests`, `auto_sweeper_runs`

Apply with:
```bash
psql $DATABASE_URL -f migrations/005_gas_station.sql
```

## k6 Stress Test

`tests/paymaster_stress.js` — 5 scenarios: `paymaster_spike` (ramping-arrival-rate 10→80 tx/s), `idempotency_collision` (20 VUs same sig → must all get 202 or 409), `rate_limit_tier` (15 VUs with sk_test_* key → must trigger 429s), `gas_quote_load`, `gas_status_probe`. Custom metrics: `infra_errors`, `idempotency_hits`, `rate_limit_hits`, `relay_accepted`. SLOs: p95<400ms, infra_errors<0.001.

## itoa fix

`paymaster_handlers.go` had `itoa(i int)` that only worked for single digits (`'0'+i%10`). Replaced with `strconv.Itoa(i)`. Add `strconv` to import.
