#!/usr/bin/env node
"use strict";

/*
 * ChainFX cloud security RPA.
 *
 * Non-destructive production checks for route exposure, auth, enumeration,
 * headers, CORS, injection handling, path traversal and light abuse controls.
 *
 * Usage:
 *   node tests/security_cloud_adversarial.js
 *   $env:SECURITY_RPA_BASE_URL="https://api-production-bc748.up.railway.app"; node tests/security_cloud_adversarial.js
 *   $env:SECURITY_RPA_RATE_LIMIT_COUNT="25"; node tests/security_cloud_adversarial.js
 */

const fs = require("fs");
const path = require("path");
const crypto = require("crypto");

const BASE_URL = cleanBaseURL(
  process.env.SECURITY_RPA_BASE_URL ||
    process.env.API_BASE ||
    process.env.BASE_URL ||
    "http://127.0.0.1:8080"
);
const TIMEOUT_MS = intEnv("SECURITY_RPA_TIMEOUT_MS", 6000);
const RATE_LIMIT_COUNT = intEnv("SECURITY_RPA_RATE_LIMIT_COUNT", 0);
const WARMUP_COUNT = intEnv("SECURITY_RPA_WARMUP_COUNT", 3);
const REPORT_DIR = process.env.SECURITY_RPA_REPORT_DIR || "tests";
const EVIL_ORIGIN = process.env.SECURITY_RPA_EVIL_ORIGIN || "https://evil.example";

const startedAt = new Date();
const results = [];
const latencies = [];

main().catch((err) => {
  console.error(`FATAL security RPA failed: ${err && err.message ? err.message : err}`);
  process.exit(2);
});

async function main() {
  console.log(`ChainFX security cloud RPA`);
  console.log(`Target: ${BASE_URL}`);
  console.log(`Mode: non-destructive, warmup_count=${WARMUP_COUNT}, rate_limit_count=${RATE_LIMIT_COUNT}`);
  console.log("");

  await warmup();
  await runPublicSurface();
  await runProtectedSurface();
  await runAuthAndEnumeration();
  await runHeadersAndCORS();
  await runAttackPayloads();
  await runSensitivePathEnumeration();
  await runOptionalRateLimitProbe();

  const report = buildReport();
  writeReports(report);
  printSummary(report);

  if (report.failures > 0) process.exit(1);
}

async function warmup() {
  if (WARMUP_COUNT <= 0) return;
  section("Warmup");
  for (let i = 0; i < WARMUP_COUNT; i += 1) {
    const res = await request("GET", "/healthz", {}, null, { recordLatency: false });
    console.log(`WARM healthz ${i + 1}/${WARMUP_COUNT} status=${res.status} ${res.latencyMs}ms`);
  }
}

async function runPublicSurface() {
  section("Public surface");
  const checks = [
    ["healthz public", "GET", "/healthz", {}, null, [200]],
    ["readyz public", "GET", "/readyz", {}, null, [200, 503]],
    ["rates public", "GET", "/rates", {}, null, [200]],
    ["mobile health public", "GET", "/api/mobile/health", {}, null, [200]],
    ["mobile assets public", "GET", "/api/mobile/assets", {}, null, [200]],
    ["agent assets public", "GET", "/agent/v1/assets", {}, null, [200]],
    ["agent capabilities public", "GET", "/agent/v1/capabilities", {}, null, [200]],
    ["agent card public", "GET", "/.well-known/agent-card.json", {}, null, [200]],
    ["openapi public", "GET", "/openapi.json", {}, null, [200]],
  ];
  for (const [name, method, urlPath, headers, body, allowed] of checks) {
    const res = await request(method, urlPath, headers, body);
    addResult({
      name,
      category: "public_surface",
      status: res.status,
      latencyMs: res.latencyMs,
      pass: allowed.includes(res.status),
      severity: "medium",
      control: "PUB-001",
      expected: allowed.join("|"),
      observed: String(res.status),
      detail: `expected ${allowed.join("|")}`,
    });
  }
}

async function runProtectedSurface() {
  section("Protected surface");
  const protectedRoutes = [
    ["admin overview requires auth", "GET", "/api/admin/overview"],
    ["admin transactions requires auth", "GET", "/api/admin/transactions"],
    ["admin wallets requires auth", "GET", "/api/admin/wallets"],
    ["admin signer probe requires auth", "POST", "/api/admin/signer/probe"],
    ["metrics requires auth", "GET", "/metrics"],
    ["developer projects requires auth", "GET", "/developer/projects"],
    ["webhook subscriptions requires auth", "GET", "/api/webhooks/subscriptions"],
    ["mobile profile requires JWT", "GET", "/api/mobile/user/profile"],
    ["mobile wallet address requires JWT", "GET", "/api/mobile/wallet/address"],
    ["mobile wallet transfer requires JWT", "POST", "/api/mobile/wallet/transfer"],
    ["mobile orders requires JWT", "GET", "/api/mobile/orders"],
    ["mobile kyc limits requires JWT", "GET", "/api/mobile/kyc/limits"],
    ["mobile ws orders requires JWT", "GET", "/api/mobile/ws/orders"],
    ["gas chaos requires admin", "GET", "/v1/admin/gas/chaos-history"],
  ];
  for (const [name, method, urlPath] of protectedRoutes) {
    const res = await request(method, urlPath, jsonHeaders(), sampleBody(urlPath));
    addResult({
      name,
      category: "auth_required",
      status: res.status,
      latencyMs: res.latencyMs,
      pass: isAuthRejected(res.status),
      severity: "high",
      control: "AUTH-001",
      expected: "401|403|404|405|429",
      observed: String(res.status),
      detail: "unauthenticated request must not return 2xx",
    });
  }

  const invalidJWT = "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.invalid.signature";
  for (const urlPath of ["/api/mobile/user/profile", "/api/mobile/wallet/address", "/api/mobile/orders"]) {
    const res = await request("GET", urlPath, { Authorization: invalidJWT });
    addResult({
      name: `invalid JWT rejected ${urlPath}`,
      category: "auth_required",
      status: res.status,
      latencyMs: res.latencyMs,
      pass: [401, 403].includes(res.status),
      severity: "high",
      control: "AUTH-002",
      expected: "401|403",
      observed: String(res.status),
      detail: "tampered JWT must be rejected",
    });
  }
}

async function runAuthAndEnumeration() {
  section("Enumeration");
  const marker = randomToken(8);
  const emails = [`probe-${marker}-a@example.invalid`, `probe-${marker}-b@example.invalid`];
  const bodies = [];
  for (const email of emails) {
    bodies.push(await request("POST", "/api/mobile/auth/login", jsonHeaders(), {
      email,
      password: "not-the-password",
    }));
  }

  const normalized = bodies.map((res) => normalizeBody(res.bodyText));
  const statusUniform = bodies[0].status === bodies[1].status;
  const lengthDelta = Math.abs(normalized[0].length - normalized[1].length);
  addResult({
    name: "mobile login responses are uniform for unknown users",
    category: "enumeration",
    status: `${bodies[0].status}/${bodies[1].status}`,
    latencyMs: maxLatency(bodies),
    pass: statusUniform && lengthDelta < 200 && !looksLikeEnumerationLeak(normalized.join(" ")),
    severity: "medium",
    control: "ENUM-001",
    expected: "uniform_error",
    observed: `${bodies[0].status}/${bodies[1].status}`,
    detail: `status_uniform=${statusUniform} body_length_delta=${lengthDelta}`,
  });

  const adminRes = await request("POST", "/api/admin/login", jsonHeaders(), {
    email: "' OR 1=1 --",
    password: randomToken(16),
  });
  addResult({
    name: "admin login rejects SQLi-style credentials without token",
    category: "enumeration",
    status: adminRes.status,
    latencyMs: adminRes.latencyMs,
    pass: !is2xx(adminRes.status) && !containsToken(adminRes.bodyText) && adminRes.status < 500,
    severity: "high",
    control: "AUTH-003",
    expected: "no_token_non_5xx",
    observed: String(adminRes.status),
    detail: "invalid credentials must not authenticate or leak server errors",
  });
}

async function runHeadersAndCORS() {
  section("Headers and CORS");
  const res = await request("GET", "/healthz");
  const headers = lowerHeaders(res.headers);
  const requiredHeaders = [
    ["x-content-type-options", "nosniff", "high"],
    ["x-frame-options", "deny", "high"],
    ["referrer-policy", "no-referrer", "medium"],
  ];
  for (const [header, expected, severity] of requiredHeaders) {
    const value = headers[header] || "";
    addResult({
      name: `security header ${header}`,
      category: "headers",
      status: res.status,
      latencyMs: res.latencyMs,
      pass: value.toLowerCase().includes(expected),
      severity,
      control: "HDR-001",
      expected,
      observed: value || "<missing>",
      detail: value ? `value=${value}` : "missing",
    });
  }

  for (const header of ["strict-transport-security", "content-security-policy"]) {
    const value = headers[header] || "";
    addResult({
      name: `recommended header ${header}`,
      category: "headers",
      status: res.status,
      latencyMs: res.latencyMs,
      pass: true,
      warning: true,
      severity: "low",
      control: "HDR-002",
      expected: "present",
      observed: value || "<missing>",
      detail: value ? `value=${value}` : "missing; recommended for browser-facing production domains",
    });
  }

  for (const origin of [
    EVIL_ORIGIN,
    "null",
    "https://chainfx.store.evil.example",
    "https://CHAINFX.STORE.evil.example",
    "https://chainfx.store:444",
  ]) {
    const corsRes = await request("OPTIONS", "/api/mobile/health", {
      Origin: origin,
      "Access-Control-Request-Method": "POST",
    });
    const corsHeaders = lowerHeaders(corsRes.headers);
    const echoed = corsHeaders["access-control-allow-origin"] === origin || corsHeaders["access-control-allow-origin"] === "*";
    addResult({
      name: `CORS rejects arbitrary origin ${origin}`,
      category: "cors",
      status: corsRes.status,
      latencyMs: corsRes.latencyMs,
      pass: !echoed,
      severity: "high",
      control: "CORS-001",
      expected: "no ACAO echo or wildcard",
      observed: corsHeaders["access-control-allow-origin"] || "<empty>",
      detail: `access-control-allow-origin=${corsHeaders["access-control-allow-origin"] || "<empty>"}`,
    });
  }

  const querySecret = await request("GET", "/rates?apiKey=sk_live_should_not_be_in_query");
  addResult({
    name: "API key in query string rejected in production",
    category: "headers",
    status: querySecret.status,
    latencyMs: querySecret.latencyMs,
    pass: [400, 401, 403].includes(querySecret.status),
    warning: querySecret.status === 200,
    severity: "medium",
    control: "HDR-003",
    expected: "400|401|403",
    observed: String(querySecret.status),
    detail: "production should reject apiKey query parameter",
  });
}

async function runAttackPayloads() {
  section("Payload attacks");
  const payloads = [
    {
      name: "quote rejects SQLi side",
      method: "POST",
      path: "/quote",
      body: { side: "' OR 1=1 --", asset: "USDT", amount: 10 },
      pass: (res) => !is2xx(res.status) && res.status < 500,
      severity: "medium",
    },
    {
      name: "quote rejects XSS asset",
      method: "POST",
      path: "/quote",
      body: { side: "buy", asset: "<script>alert(1)</script>", amount: 10 },
      pass: (res) => !rawReflectsScript(res.bodyText) && res.status < 500,
      severity: "medium",
    },
    {
      name: "sell invalid body cannot create order",
      method: "POST",
      path: "/sell",
      body: { quoteId: "../../../etc/passwd", pix: { cpf: "' OR 1=1 --" } },
      headers: { "Idempotency-Key": `sec-${randomToken(16)}` },
      pass: (res) => !is2xx(res.status) && res.status < 500,
      severity: "high",
    },
    {
      name: "agent trade execute invalid tx cannot settle",
      method: "POST",
      path: "/agent/v1/trade/execute",
      body: { tradeIntentId: "00000000-0000-0000-0000-000000000000", txHash: "0x1234" },
      headers: { "Idempotency-Key": `sec-${randomToken(16)}` },
      pass: (res) => !is2xx(res.status),
      severity: "high",
    },
    {
      name: "internal sweep rejects missing HMAC",
      method: "POST",
      path: "/internal/sweep",
      body: {},
      pass: (res) => [401, 403].includes(res.status),
      severity: "critical",
    },
    {
      name: "internal sweep rejects fake HMAC",
      method: "POST",
      path: "/internal/sweep",
      headers: { "x-internal-hmac": "fake" },
      body: {},
      pass: (res) => [401, 403].includes(res.status),
      severity: "critical",
    },
    {
      name: "webhook subscription SSRF blocked before unauth access",
      method: "POST",
      path: "/api/webhooks/subscriptions",
      body: { targetUrl: "http://169.254.169.254/latest/meta-data", events: ["order.created"] },
      pass: (res) => !is2xx(res.status),
      severity: "high",
    },
    {
      name: "order IDOR without token is not readable",
      method: "GET",
      path: "/order/00000000-0000-0000-0000-000000000000",
      pass: (res) => !is2xx(res.status),
      severity: "high",
    },
    {
      name: "invalid webhook signature is not accepted",
      method: "POST",
      path: "/api/pix/webhook/buy",
      headers: { "x-chainfx-signature": "fake" },
      body: { event: "pix.received", txid: `sec-${randomToken(8)}` },
      pass: (res) => !is2xx(res.status),
      severity: "high",
    },
  ];

  for (const tc of payloads) {
    const headers = { ...jsonHeaders(), ...(tc.headers || {}) };
    const res = await request(tc.method, tc.path, headers, tc.body);
    addResult({
      name: tc.name,
      category: "attack_payload",
      status: res.status,
      latencyMs: res.latencyMs,
      pass: tc.pass(res),
      severity: tc.severity,
      control: tc.control || "PAYLOAD-001",
      expected: "no_2xx_no_5xx_or_no_reflection",
      observed: String(res.status),
      detail: res.status >= 500 ? "server returned 5xx to adversarial input" : "non-destructive payload",
    });
  }

  const idempotencyKey = `sec-replay-${randomToken(18)}`;
  const first = await request("POST", "/sell", { ...jsonHeaders(), "Idempotency-Key": idempotencyKey }, {
    quoteId: "invalid-quote",
    pix: { cpf: "00000000000" },
  });
  const second = await request("POST", "/sell", { ...jsonHeaders(), "Idempotency-Key": idempotencyKey }, {
    quoteId: "different-invalid-quote",
    pix: { cpf: "11111111111" },
  });
  addResult({
    name: "same idempotency key with different payload does not create success",
    category: "replay",
    status: `${first.status}/${second.status}`,
    latencyMs: maxLatency([first, second]),
    pass: !is2xx(first.status) && !is2xx(second.status),
    severity: "high",
    control: "REPLAY-001",
    expected: "no_2xx",
    observed: `${first.status}/${second.status}`,
    detail: "invalid replay probe; no valid API key supplied",
  });
}

async function runSensitivePathEnumeration() {
  section("Sensitive path enumeration");
  const paths = [
    "/.env",
    "/.git/config",
    "/secrets/efi-production.p12",
    "/secrets/",
    "/debug/pprof/",
    "/actuator/env",
    "/phpinfo.php",
    "/config.json",
    "/admin/metrics",
    "/..%2f.env",
    "/docs/..%2f..%2f.env",
  ];
  for (const urlPath of paths) {
    const res = await request("GET", urlPath);
    const sensitive = looksSensitive(res.bodyText);
    const genericCatchAll = is2xx(res.status) && !sensitive;
    addResult({
      name: `sensitive path blocked ${urlPath}`,
      category: "enumeration",
      status: res.status,
      latencyMs: res.latencyMs,
      pass: !sensitive,
      warning: genericCatchAll,
      severity: "critical",
      control: "ENUM-002",
      expected: "404|403 and no sensitive body",
      observed: String(res.status),
      detail: genericCatchAll
        ? "returned generic catch-all page; no sensitive marker detected"
        : "sensitive local file/debug path must not be served",
    });
  }

  for (const method of ["TRACE", "CONNECT", "PUT", "DELETE"]) {
    const res = await request(method, "/healthz");
    addResult({
      name: `unsafe method ${method} rejected`,
      category: "method_surface",
      status: res.status,
      latencyMs: res.latencyMs,
      pass: !is2xx(res.status),
      severity: method === "TRACE" || method === "CONNECT" ? "high" : "medium",
      control: "HTTP-001",
      expected: "not_2xx",
      observed: String(res.status),
      detail: "unexpected HTTP methods should not return success",
    });
  }
}

async function runOptionalRateLimitProbe() {
  section("Optional rate limit");
  if (RATE_LIMIT_COUNT <= 0) {
    addResult({
      name: "optional invalid request flood skipped",
      category: "rate_limit",
      status: "skip",
      latencyMs: 0,
      pass: true,
      warning: true,
      severity: "low",
      control: "RL-000",
      expected: "skip unless SECURITY_RPA_RATE_LIMIT_COUNT is set",
      observed: "skip",
      detail: "set SECURITY_RPA_RATE_LIMIT_COUNT to run a bounded flood",
    });
    return;
  }
  const count = Math.min(RATE_LIMIT_COUNT, 200);
  const started = Date.now();
  const reqs = Array.from({ length: count }, (_, i) =>
    request("POST", "/agent/v1/trade/execute", { ...jsonHeaders(), "Idempotency-Key": `sec-flood-${Date.now()}-${i}` }, {
      tradeIntentId: "00000000-0000-0000-0000-000000000000",
      txHash: "0x1234",
    })
  );
  const responses = await Promise.all(reqs);
  const fiveXX = responses.filter((r) => r.status >= 500).length;
  const twoXX = responses.filter((r) => is2xx(r.status)).length;
  const rateLimited = responses.filter((r) => r.status === 429).length;
  const retryAfterCount = responses.filter((r) => r.status === 429 && lowerHeaders(r.headers)["retry-after"]).length;
  addResult({
    name: `bounded invalid execution flood stays fail-closed (${count} requests)`,
    category: "rate_limit",
    status: `${rateLimited}/${count} rate-limited, ${retryAfterCount}/${rateLimited} retry-after, ${fiveXX}/${count} 5xx, ${twoXX}/${count} success`,
    latencyMs: Date.now() - started,
    pass: twoXX === 0 && fiveXX === 0 && (rateLimited === 0 || retryAfterCount === rateLimited),
    severity: "high",
    control: "RL-001",
    expected: "0 success, 0 5xx, Retry-After on every 429",
    observed: `${rateLimited}/${count} 429, ${retryAfterCount}/${rateLimited} retry-after`,
    detail: "bounded unauth/invalid requests must not succeed or crash",
  });
}

async function request(method, urlPath, headers = {}, body = null, options = {}) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), TIMEOUT_MS);
  const started = Date.now();
  const init = {
    method,
    headers: {
      "User-Agent": "ChainFX-Security-RPA/1.0",
      Accept: "application/json,text/plain,*/*",
      ...headers,
    },
    signal: controller.signal,
    redirect: "manual",
  };
  if (body !== null && body !== undefined && method !== "GET" && method !== "HEAD") {
    init.body = typeof body === "string" ? body : JSON.stringify(body);
    if (!init.headers["Content-Type"] && !init.headers["content-type"]) {
      init.headers["Content-Type"] = "application/json";
    }
  }

  try {
    const res = await fetch(BASE_URL + urlPath, init);
    const bodyText = await safeText(res);
    const latencyMs = Date.now() - started;
    if (options.recordLatency !== false) latencies.push(latencyMs);
    return {
      status: res.status,
      headers: Object.fromEntries(res.headers.entries()),
      bodyText,
      latencyMs,
      error: "",
    };
  } catch (err) {
    const latencyMs = Date.now() - started;
    return {
      status: 0,
      headers: {},
      bodyText: "",
      latencyMs,
      error: err && err.message ? err.message : String(err),
    };
  } finally {
    clearTimeout(timer);
  }
}

function addResult(item) {
  const control = item.control || defaultControl(item.category);
  const expected = item.expected || "";
  const observed = item.observed || String(item.status);
  const blocking = item.blocking !== undefined ? item.blocking : item.severity === "critical" || item.severity === "high";
  const status = item.pass ? (item.warning ? "WARN" : "PASS") : "FAIL";
  results.push({ ...item, control, expected, observed, blocking, outcome: status });
  const statusText = String(item.status).padEnd(18);
  const latencyText = item.latencyMs ? `${item.latencyMs}ms` : "";
  console.log(`${status.padEnd(4)} ${control} ${item.name} status=${statusText} ${latencyText} ${item.detail ? `- ${item.detail}` : ""}`);
}

function buildReport() {
  const failures = results.filter((r) => r.outcome === "FAIL").length;
  const warnings = results.filter((r) => r.outcome === "WARN").length;
  const passes = results.filter((r) => r.outcome === "PASS").length;
  return {
    target: BASE_URL,
    startedAt: startedAt.toISOString(),
    finishedAt: new Date().toISOString(),
    counts: { pass: passes, warn: warnings, fail: failures, total: results.length },
    failures,
    warnings,
    latencyMs: latencySummary(latencies),
    layers: [
      "public_surface",
      "auth_required",
      "enumeration",
      "headers",
      "cors",
      "attack_payload",
      "replay",
      "method_surface",
      "rate_limit",
    ],
    results,
  };
}

function writeReports(report) {
  fs.mkdirSync(REPORT_DIR, { recursive: true });
  const stamp = startedAt.toISOString().replace(/[:.]/g, "-");
  const jsonPath = path.join(REPORT_DIR, `security-cloud-report-${stamp}.json`);
  const txtPath = path.join(REPORT_DIR, `security-cloud-report-${stamp}.txt`);
  fs.writeFileSync(jsonPath, JSON.stringify(report, null, 2));
  fs.writeFileSync(txtPath, textReport(report));
  console.log("");
  console.log(`Reports:`);
  console.log(`  ${jsonPath}`);
  console.log(`  ${txtPath}`);
}

function printSummary(report) {
  console.log("");
  console.log("Summary:");
  console.log(`  PASS=${report.counts.pass} WARN=${report.counts.warn} FAIL=${report.counts.fail} TOTAL=${report.counts.total}`);
  console.log(
    `  Latency ms: min=${report.latencyMs.min} avg=${report.latencyMs.avg} p50=${report.latencyMs.p50} p55=${report.latencyMs.p55} p75=${report.latencyMs.p75} p90=${report.latencyMs.p90} p95=${report.latencyMs.p95} p99=${report.latencyMs.p99} max=${report.latencyMs.max}`
  );
}

function textReport(report) {
  const lines = [];
  lines.push("ChainFX Security Cloud RPA Report");
  lines.push(`Target: ${report.target}`);
  lines.push(`Started: ${report.startedAt}`);
  lines.push(`Finished: ${report.finishedAt}`);
  lines.push(`Counts: PASS=${report.counts.pass} WARN=${report.counts.warn} FAIL=${report.counts.fail} TOTAL=${report.counts.total}`);
  lines.push(
    `Latency ms: min=${report.latencyMs.min} avg=${report.latencyMs.avg} p50=${report.latencyMs.p50} p55=${report.latencyMs.p55} p75=${report.latencyMs.p75} p90=${report.latencyMs.p90} p95=${report.latencyMs.p95} p99=${report.latencyMs.p99} max=${report.latencyMs.max}`
  );
  lines.push("");
  for (const r of report.results) {
    lines.push(`${r.outcome} [${r.severity}] ${r.control} ${r.category} - ${r.name}`);
    lines.push(`  expected=${r.expected || ""} observed=${r.observed || r.status} blocking=${r.blocking} status=${r.status} latency=${r.latencyMs}ms`);
    if (r.detail) lines.push(`  detail=${r.detail}`);
  }
  return lines.join("\n") + "\n";
}

function defaultControl(category) {
  const map = {
    public_surface: "PUB-000",
    auth_required: "AUTH-000",
    enumeration: "ENUM-000",
    headers: "HDR-000",
    cors: "CORS-000",
    attack_payload: "PAYLOAD-000",
    replay: "REPLAY-000",
    method_surface: "HTTP-000",
    rate_limit: "RL-000",
  };
  return map[category] || "GEN-000";
}

function jsonHeaders() {
  return { "Content-Type": "application/json" };
}

function sampleBody(urlPath) {
  if (urlPath.includes("transfer")) return { to: "0x000000000000000000000000000000000000dEaD", amount: "1", token: "USDT" };
  if (urlPath.includes("probe")) return {};
  return null;
}

function cleanBaseURL(value) {
  return String(value || "").trim().replace(/\/+$/, "");
}

function intEnv(name, fallback) {
  const value = Number.parseInt(process.env[name] || "", 10);
  return Number.isFinite(value) ? value : fallback;
}

function randomToken(bytes) {
  return crypto.randomBytes(bytes).toString("hex");
}

function is2xx(status) {
  return status >= 200 && status < 300;
}

function isAuthRejected(status) {
  return [400, 401, 403, 404, 405, 429].includes(status);
}

function lowerHeaders(headers) {
  const out = {};
  for (const [key, value] of Object.entries(headers || {})) out[key.toLowerCase()] = String(value);
  return out;
}

async function safeText(res) {
  try {
    const text = await res.text();
    return text.length > 4096 ? text.slice(0, 4096) : text;
  } catch {
    return "";
  }
}

function normalizeBody(body) {
  return String(body || "")
    .replace(/[0-9a-f]{8}-[0-9a-f-]{27,}/gi, "<uuid>")
    .replace(/[0-9a-f]{16,}/gi, "<hex>")
    .replace(/\d+/g, "<n>");
}

function looksLikeEnumerationLeak(body) {
  const text = String(body || "").toLowerCase();
  return [
    "user exists",
    "usuario existe",
    "usuário existe",
    "user not found",
    "usuario nao encontrado",
    "usuário não encontrado",
    "email not found",
    "email existe",
  ].some((needle) => text.includes(needle));
}

function containsToken(body) {
  const text = String(body || "").toLowerCase();
  return text.includes("access_token") || text.includes("refresh_token") || text.includes('"token"');
}

function rawReflectsScript(body) {
  return String(body || "").toLowerCase().includes("<script>alert(1)</script>");
}

function looksSensitive(body) {
  const text = String(body || "").toLowerCase();
  return [
    "database_url=",
    "private_key",
    "mnemonic",
    "signer_hmac_secret",
    "mobile_wallet_encryption_secret",
    "begin private key",
    "[core]",
    "repositoryformatversion",
  ].some((needle) => text.includes(needle));
}

function latencySummary(values) {
  if (!values.length) return { count: 0, min: 0, avg: 0, p50: 0, p55: 0, p75: 0, p90: 0, p95: 0, p99: 0, max: 0 };
  const sorted = [...values].sort((a, b) => a - b);
  const avg = sorted.reduce((sum, value) => sum + value, 0) / sorted.length;
  return {
    count: sorted.length,
    min: sorted[0],
    avg: Math.round(avg),
    p50: percentile(sorted, 0.50),
    p55: percentile(sorted, 0.55),
    p75: percentile(sorted, 0.75),
    p90: percentile(sorted, 0.90),
    p95: percentile(sorted, 0.95),
    p99: percentile(sorted, 0.99),
    max: sorted[sorted.length - 1],
  };
}

function percentile(sorted, p) {
  const index = Math.min(sorted.length - 1, Math.ceil(sorted.length * p) - 1);
  return sorted[Math.max(0, index)];
}

function maxLatency(items) {
  return Math.max(...items.map((item) => item.latencyMs || 0));
}

function section(name) {
  console.log("");
  console.log(`[${name}]`);
}
