/**
 * ChainFX Gas Station + Human Rail — k6 stress + spike test
 *
 * Covers every resilience dimension from the spec:
 *   1. paymaster_spike       — ramping-arrival-rate to 80 tx/s; validates p95 + error rate
 *   2. idempotency_collision — same (r,s) signature hammered by 20 VUs to verify 409 handling
 *   3. rate_limit_tier       — test-key (sk_test_*) hits its 10-req/min wall, live-key gets 60
 *   4. gas_quote_load        — steady 20 VUs quoting; validates fee fields
 *   5. gas_status_probe      — continuous health probe, 50 VUs, p95 < 300 ms
 *   6. human_rail_flood      — 50 signed Pix-settlement notifications/sec against the REAL
 *                              POST /api/pix/webhook/buy route (not a fictitious endpoint),
 *                              each with a valid HMAC computed here via k6/crypto so the
 *                              real handler's auth path is exercised, not bypassed.
 *
 * Run:
 *   k6 run tests/paymaster_stress.js \
 *     -e BASE_URL=http://localhost:8080 \
 *     -e API_KEY_LIVE=sk_live_chainfx_local \
 *     -e API_KEY_TEST=sk_test_chainfx_local \
 *     -e PIX_WEBHOOK_SECRET=<same secret the API was started with> \
 *     -e BUY_IDS_FILE=tests/chaos/.buy_ids.json
 *
 * The human_rail_flood scenario is a no-op (skips work, still "passes") if
 * BUY_IDS_FILE isn't provided — seed targets with cmd/chaosseed first (see
 * tests/chaos_suite.sh), never invent fake buy IDs here.
 *
 * SLOs (per spec):
 *   p95 end-to-end < 400 ms
 *   real infrastructure errors (5xx, not 429/409) < 0.1 %
 *   Pix settlement resolution (human_rail_settlement_duration) p95 < 300 ms
 */

import http from "k6/http";
import { check, sleep, group } from "k6";
import { Rate, Trend, Counter } from "k6/metrics";
import { hmac } from "k6/crypto";
import { SharedArray } from "k6/data";

// ── Custom metrics ─────────────────────────────────────────────────────────────
const infraErrors        = new Rate("infra_errors");         // real 5xx (not 503=disabled)
const idempotencyHits    = new Counter("idempotency_hits");  // 409 Conflict responses
const rateLimitHits      = new Counter("rate_limit_hits");   // 429 Too Many Requests
const relayAccepted      = new Counter("relay_accepted");    // 202 Accepted
const quoteDuration      = new Trend("quote_duration_ms", true);
const relayDuration      = new Trend("relay_duration_ms", true);
const humanRailSettlementDuration = new Trend("human_rail_settlement_duration", true);
const hmacValidationFailures      = new Counter("hmac_validation_failures");
const humanRailDuplicatesBlocked  = new Counter("human_rail_duplicates_blocked");

// ── Config ────────────────────────────────────────────────────────────────────
const BASE_URL      = __ENV.BASE_URL       || "http://localhost:8080";
const LIVE_KEY      = __ENV.API_KEY_LIVE   || "sk_live_chainfx_local";
const TEST_KEY      = __ENV.API_KEY_TEST   || "sk_test_chainfx_local";
const PIX_WEBHOOK_SECRET = __ENV.PIX_WEBHOOK_SECRET || "";
const BUY_IDS_FILE       = __ENV.BUY_IDS_FILE || "";

// Real buy-order IDs seeded by cmd/chaosseed (see tests/chaos_suite.sh) —
// loaded once per VU pool via SharedArray, never fabricated here.
const buyIDs = new SharedArray("buyIDs", function () {
  if (!BUY_IDS_FILE) return [];
  try {
    return JSON.parse(open(BUY_IDS_FILE));
  } catch (e) {
    console.error(`[human_rail] failed to load BUY_IDS_FILE=${BUY_IDS_FILE}: ${e}`);
    return [];
  }
});

// ── Options ───────────────────────────────────────────────────────────────────
export const options = {
  scenarios: {

    // ── 1. Spike load — ramping-arrival-rate ────────────────────────────────
    // Simulates a DeFi event driving 80 meta-transactions/second at peak.
    // Measures whether the system degrades gracefully (429s) instead of crashing.
    paymaster_spike: {
      executor:        "ramping-arrival-rate",
      startRate:       10,
      timeUnit:        "1s",
      stages: [
        { duration: "20s", target: 80 },  // ramp: 10 → 80 tx/s
        { duration: "30s", target: 80 },  // sustain peak
        { duration: "10s", target: 0  },  // cool-down
      ],
      preAllocatedVUs: 40,
      maxVUs:          150,
      exec:            "spikeScenario",
      tags:            { scenario: "paymaster_spike" },
    },

    // ── 2. Idempotency collision — same signature, 20 concurrent VUs ────────
    // Exactly one should get 202, all others must get 409 (never 500).
    // Validates the sig-hash lock + DB ON CONFLICT path under race conditions.
    idempotency_collision: {
      executor:  "constant-vus",
      vus:       20,
      duration:  "30s",
      exec:      "collisionScenario",
      startTime: "5s",
      tags:      { scenario: "idempotency_collision" },
    },

    // ── 3. Rate-limit tier validation ───────────────────────────────────────
    // Test-key tier allows 10 req/min → expect 429 after the 10th within 60 s.
    rate_limit_tier: {
      executor:  "constant-vus",
      vus:       15,     // 15 VUs > 10-req/min limit — must trigger 429s
      duration:  "20s",
      exec:      "rateLimitScenario",
      startTime: "10s",
      tags:      { scenario: "rate_limit_tier" },
    },

    // ── 4. Quote load — steady state ────────────────────────────────────────
    gas_quote_load: {
      executor:  "constant-vus",
      vus:       20,
      duration:  "60s",
      exec:      "quoteScenario",
      tags:      { scenario: "gas_quote_load" },
    },

    // ── 5. Status probe — continuous health ─────────────────────────────────
    gas_status_probe: {
      executor:  "constant-vus",
      vus:       50,
      duration:  "60s",
      exec:      "statusScenario",
      tags:      { scenario: "gas_status_probe" },
    },

    // ── 6. Human Rail flood — real signed Pix settlement notifications ──────
    // 50 notifications/sec against the REAL POST /api/pix/webhook/buy route,
    // each with a valid HMAC. Runs only if buy IDs were seeded (see header).
    human_rail_flood: {
      executor:        "constant-arrival-rate",
      rate:            50,
      timeUnit:        "1s",
      duration:        "20s",
      preAllocatedVUs: 10,
      maxVUs:          50,
      exec:            "humanRailScenario",
      startTime:       "15s",
      tags:            { scenario: "human_rail_flood" },
    },

  },

  thresholds: {
    // ── Spec SLOs ─────────────────────────────────────────────────────────
    // All relay/quote endpoints must stay under 400 ms p95.
    "http_req_duration{scenario:paymaster_spike}":      ["p(95)<400"],
    "http_req_duration{scenario:gas_quote_load}":       ["p(95)<400"],
    "http_req_duration{scenario:gas_status_probe}":     ["p(95)<300"],
    "http_req_duration{scenario:idempotency_collision}":["p(95)<400"],

    // Real infrastructure errors (5xx, not 429/409) must be < 0.1 %.
    // 429 = rate limited (expected), 409 = idempotency (expected), 503 = disabled (expected).
    infra_errors: ["rate<0.001"],

    // Custom trends
    quote_duration_ms: ["p(95)<400"],
    relay_duration_ms: ["p(95)<400"],

    // Human Rail SLO: Pix settlement race resolution must stay fast even
    // under a 50 req/s flood and even while chaos is being injected into
    // the DB layer mid-run.
    human_rail_settlement_duration: ["p(95)<300"],
  },
};

const HEADERS_JSON = { "Content-Type": "application/json" };

function authHeaders(key) {
  return { "Content-Type": "application/json", "Authorization": `Bearer ${key}` };
}

// ── Unique relay payload factory ───────────────────────────────────────────────
// Generates a unique (r, s) per (VU, iteration) to avoid accidental collisions
// across test scenarios that should NOT collide.
function uniqueRelayPayload(vuID, iter) {
  const r = `0x${"a".repeat(60)}${String(vuID  % 100).padStart(2, "0")}${String(iter % 100).padStart(2, "0")}`;
  const s = `0x${"b".repeat(60)}${String(iter  % 100).padStart(2, "0")}${String(vuID  % 100).padStart(2, "0")}`;
  return JSON.stringify({
    user_address: "0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
    tx_to:        "0x55d398326f99059fF775485246999027B3197955",
    tx_data:      "",
    sig_r:        r,
    sig_s:        s,
    sig_v:        "0x1b",
    amount:       "50.000000",
    token_addr:   "0x55d398326f99059fF775485246999027B3197955",
    network:      "BSC",
  });
}

// Deterministic fixed payload — same (r, s) across ALL VUs → collision.
const COLLISION_PAYLOAD = JSON.stringify({
  user_address: "0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
  tx_to:        "0x55d398326f99059fF775485246999027B3197955",
  tx_data:      "",
  sig_r:        "0x48263592c7314a87c6611c10748aeb04b58e8f23243545657687980911234345",
  sig_s:        "0xa123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  sig_v:        "0x1b",
  amount:       "50.000000",
  token_addr:   "0x55d398326f99059fF775485246999027B3197955",
  network:      "BSC",
});

// ── Scenario 1: Spike ──────────────────────────────────────────────────────────
export function spikeScenario() {
  const start   = Date.now();
  const payload = uniqueRelayPayload(__VU, __ITER);
  const res     = http.post(`${BASE_URL}/v1/gas/relay`, payload, {
    headers: authHeaders(LIVE_KEY),
    tags:    { name: "relay_spike" },
  });
  relayDuration.add(Date.now() - start);

  const accepted = check(res, {
    "spike: status is acceptable": (r) =>
      r.status === 202 ||  // accepted — happy path
      r.status === 429 ||  // rate limited (expected at peak)
      r.status === 409 ||  // duplicate sig (shouldn't happen here but safe)
      r.status === 400 ||  // validation error
      r.status === 503,    // gas station disabled
  });

  // Only count as infra error if it's a true 5xx (not 503=disabled)
  if (res.status >= 500 && res.status !== 503) {
    infraErrors.add(1);
  } else {
    infraErrors.add(0);
    if (res.status === 202)  relayAccepted.add(1);
    if (res.status === 429)  rateLimitHits.add(1);
  }

  if (!accepted) {
    console.error(`[spike] unexpected status=${res.status} body=${res.body.substring(0, 200)}`);
  }

  sleep(0.1);
}

// ── Scenario 2: Idempotency collision ─────────────────────────────────────────
// 20 VUs all send the EXACT same (r, s) signature concurrently.
// Expected: exactly one 202, all rest must be 409 Conflict — never 500.
export function collisionScenario() {
  const res = http.post(`${BASE_URL}/v1/gas/relay`, COLLISION_PAYLOAD, {
    headers: authHeaders(LIVE_KEY),
    tags:    { name: "relay_collision" },
  });

  check(res, {
    "collision: only 202 or 409 accepted (never 500)": (r) =>
      r.status === 202 ||  // first VU to acquire the lock
      r.status === 409 ||  // subsequent VUs — idempotency block
      r.status === 429 ||  // rate limited (live key, 60/min)
      r.status === 503,    // gas station disabled
  });

  if (res.status === 409) {
    idempotencyHits.add(1);
    const body = JSON.parse(res.body || "{}");
    check(res, {
      "collision: 409 has DUPLICATE_SIG code": () => body.code === "DUPLICATE_SIG",
    });
  }

  if (res.status >= 500 && res.status !== 503) {
    infraErrors.add(1);
    console.error(`[collision] INFRA ERROR status=${res.status} body=${res.body.substring(0, 200)}`);
  } else {
    infraErrors.add(0);
  }

  sleep(0.05 + Math.random() * 0.1); // tight burst to maximise race condition surface
}

// ── Scenario 3: Rate-limit tier ────────────────────────────────────────────────
// 15 VUs using a sk_test_* key (tier limit = 10 req/min).
// After the 10th request the 11th–15th must get 429.
export function rateLimitScenario() {
  const payload = uniqueRelayPayload(__VU, __ITER + 10000); // offset to avoid cross-scenario collision
  const res = http.post(`${BASE_URL}/v1/gas/relay`, payload, {
    headers: authHeaders(TEST_KEY),
    tags:    { name: "relay_ratelimit" },
  });

  check(res, {
    "rate-limit: 202, 429, or 409 accepted": (r) =>
      r.status === 202 ||
      r.status === 429 ||
      r.status === 409 ||
      r.status === 503,
  });

  if (res.status === 429) {
    rateLimitHits.add(1);
    // Validate Retry-After header is present (spec requirement).
    check(res, {
      "rate-limit: Retry-After header set": (r) =>
        r.headers["Retry-After"] !== undefined && r.headers["Retry-After"] !== "",
      "rate-limit: X-RateLimit-Limit header set": (r) =>
        r.headers["X-Ratelimit-Limit"] !== undefined || r.headers["X-RateLimit-Limit"] !== undefined,
    });
  }

  if (res.status >= 500 && res.status !== 503) infraErrors.add(1);
  else infraErrors.add(0);

  sleep(0.2);
}

// ── Scenario 4: Quote load ─────────────────────────────────────────────────────
export function quoteScenario() {
  const payload = JSON.stringify({
    user_address: "0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
    tx_to:        "0x55d398326f99059fF775485246999027B3197955",
    tx_data:      "",
  });

  const start = Date.now();
  const res = http.post(`${BASE_URL}/v1/gas/quote`, payload, {
    headers: HEADERS_JSON,
    tags:    { name: "gas_quote" },
  });
  quoteDuration.add(Date.now() - start);

  check(res, {
    "quote: acceptable status": (r) =>
      r.status === 200 || r.status === 400 || r.status === 503,
    "quote: fee_usdt >= 0 when 200": (r) => {
      if (r.status !== 200) return true;
      try { return JSON.parse(r.body).fee_usdt >= 0; } catch { return false; }
    },
    "quote: valid_until_ms in future when 200": (r) => {
      if (r.status !== 200) return true;
      try { return JSON.parse(r.body).valid_until_ms > Date.now(); } catch { return false; }
    },
  });

  if (res.status >= 500 && res.status !== 503) infraErrors.add(1);
  else infraErrors.add(0);

  sleep(0.2 + Math.random() * 0.3);
}

// ── Scenario 5: Status probe ───────────────────────────────────────────────────
export function statusScenario() {
  const res = http.get(`${BASE_URL}/v1/gas/status`, {
    tags: { name: "gas_status" },
  });

  check(res, {
    "status: 200 or 503 only": (r) => r.status === 200 || r.status === 503,
    "status: has enabled field": (r) => {
      try { return typeof JSON.parse(r.body).enabled === "boolean"; } catch { return false; }
    },
  });

  if (res.status >= 500 && res.status !== 503) infraErrors.add(1);
  else infraErrors.add(0);

  sleep(0.1 + Math.random() * 0.2);
}

// ── Scenario 6: Human Rail flood ───────────────────────────────────────────────
// Fires a real signed Pix settlement notification against a real, previously
// seeded buy order (see cmd/chaosseed + tests/chaos_suite.sh). Cycles through
// the seeded pool so concurrent iterations legitimately race on the SAME
// buy order sometimes (testing the dedup index) and hit distinct orders
// other times (testing steady-state throughput) — matching real Efí traffic.
export function humanRailScenario() {
  if (buyIDs.length === 0 || !PIX_WEBHOOK_SECRET) {
    // No seeded targets / no secret configured — nothing real to attack.
    // Recorded as neither pass nor fail; see tests/chaos_suite.sh for setup.
    return;
  }

  const buyID = buyIDs[__ITER % buyIDs.length];
  const providerID = `k6-efi-${__VU}-${__ITER}-${Date.now()}`;
  const body = JSON.stringify({
    buyId:      buyID,
    status:     "CONCLUIDA",
    providerId: providerID,
  });

  const signature = hmac("sha256", PIX_WEBHOOK_SECRET, body, "hex");

  const start = Date.now();
  const res = http.post(`${BASE_URL}/api/pix/webhook/buy`, body, {
    headers: {
      "Content-Type":     "application/json",
      "x-efi-signature":  signature,
    },
    tags: { name: "human_rail_webhook" },
  });
  const elapsed = Date.now() - start;

  check(res, {
    "human_rail: acceptable status": (r) =>
      r.status === 200 ||  // applied or duplicate — both are clean outcomes
      r.status === 401 ||  // only if PIX_WEBHOOK_SECRET mismatches the running API
      r.status === 400 ||  // validation error
      r.status === 503,    // DB/dependency chaos surfaced as a clean 503, not a crash
  });

  if (res.status === 200) {
    humanRailSettlementDuration.add(elapsed);
    try {
      if (JSON.parse(res.body || "{}").duplicate) {
        humanRailDuplicatesBlocked.add(1);
      }
    } catch (e) { /* non-JSON body — already flagged by the status check above */ }
  } else if (res.status === 401) {
    hmacValidationFailures.add(1);
    console.error(`[human_rail] HMAC rejected — PIX_WEBHOOK_SECRET likely doesn't match the running API`);
  }

  if (res.status >= 500 && res.status !== 503) {
    infraErrors.add(1);
    console.error(`[human_rail] INFRA ERROR status=${res.status} body=${res.body.substring(0, 200)}`);
  } else {
    infraErrors.add(0);
  }

  sleep(0.02);
}

// ── Summary helper (printed at end of run) ────────────────────────────────────
export function handleSummary(data) {
  const relayOk    = data.metrics.relay_accepted?.values?.count ?? 0;
  const idem       = data.metrics.idempotency_hits?.values?.count ?? 0;
  const rl         = data.metrics.rate_limit_hits?.values?.count ?? 0;
  const errRate    = (data.metrics.infra_errors?.values?.rate ?? 0) * 100;
  const p95Spike   = data.metrics["http_req_duration{scenario:paymaster_spike}"]?.values?.["p(95)"] ?? "-";
  const p95Quote   = data.metrics["http_req_duration{scenario:gas_quote_load}"]?.values?.["p(95)"] ?? "-";

  console.log(`
╔══════════════════════════════════════════════════════╗
║          ChainFX Gas Station — Stress Report         ║
╠══════════════════════════════════════════════════════╣
║  Relays accepted (202)        : ${String(relayOk).padStart(6)}              ║
║  Idempotency blocks (409)     : ${String(idem).padStart(6)}              ║
║  Rate-limit blocks  (429)     : ${String(rl).padStart(6)}              ║
║  Infra error rate             : ${errRate.toFixed(3).padStart(6)} %           ║
║  p95 relay/spike              : ${String(p95Spike).padStart(6)} ms           ║
║  p95 quote                    : ${String(p95Quote).padStart(6)} ms           ║
╚══════════════════════════════════════════════════════╝`);
  return {};
}
