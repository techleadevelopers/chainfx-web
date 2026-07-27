#!/usr/bin/env node
"use strict";

/*
 * ChainFX Tap NFC product RPA.
 *
 * This is a controlled product/security smoke for the closed-loop NFC rail.
 * It is non-mutating by default. Mutating flows require:
 *
 *   NFC_RPA_RUN_MUTATING=true
 *   NFC_RPA_BASE_URL=https://...
 *   NFC_RPA_CHAINFX_API_KEY=...
 *   NFC_RPA_TERMINAL_KEY=...
 *   NFC_RPA_MERCHANT_ID=...
 *   NFC_RPA_TERMINAL_ID=...
 *   NFC_RPA_WALLET=0x...
 *
 * Optional:
 *   NFC_RPA_NETWORK=BSC
 *   NFC_RPA_AMOUNT_BRL=10.00
 *   NFC_RPA_FUND_USDT=100.000000
 *   NFC_RPA_ITERATIONS=5
 */

const fs = require("fs");
const path = require("path");
const crypto = require("crypto");

const BASE_URL = cleanBaseURL(process.env.NFC_RPA_BASE_URL || process.env.API_BASE || "http://127.0.0.1:8080");
const RUN_MUTATING = boolEnv("NFC_RPA_RUN_MUTATING", false);
const CHAINFX_API_KEY = process.env.NFC_RPA_CHAINFX_API_KEY || "";
const TERMINAL_KEY = process.env.NFC_RPA_TERMINAL_KEY || "";
const MERCHANT_ID = process.env.NFC_RPA_MERCHANT_ID || "merchant_demo";
const TERMINAL_ID = process.env.NFC_RPA_TERMINAL_ID || "terminal_01";
const WALLET = process.env.NFC_RPA_WALLET || "";
const NETWORK = process.env.NFC_RPA_NETWORK || "BSC";
const AMOUNT_BRL = process.env.NFC_RPA_AMOUNT_BRL || "10.00";
const FUND_USDT = process.env.NFC_RPA_FUND_USDT || "100.000000";
const ITERATIONS = intEnv("NFC_RPA_ITERATIONS", 5);
const TIMEOUT_MS = intEnv("NFC_RPA_TIMEOUT_MS", 5000);
const REPORT_DIR = process.env.NFC_RPA_REPORT_DIR || "tests";
const EXPECTED_FEE_BPS = intEnv("NFC_RPA_EXPECTED_FEE_BPS", 400);

const results = [];
const latencies = [];
const serverTimingSamples = [];

main().catch((err) => {
  console.error(`FATAL NFC RPA failed: ${err && err.stack ? err.stack : err}`);
  process.exit(2);
});

async function main() {
  console.log("ChainFX Tap NFC product RPA");
  console.log(`Target: ${BASE_URL}`);
  console.log(`Mode: ${RUN_MUTATING ? "MUTATING controlled sandbox/staging" : "non-mutating checks only"}`);
  console.log("");

  await publicReadiness();
  await protectedTerminalSurface();

  if (RUN_MUTATING) {
    requireEnv("NFC_RPA_CHAINFX_API_KEY", CHAINFX_API_KEY);
    requireEnv("NFC_RPA_TERMINAL_KEY", TERMINAL_KEY);
    requireEnv("NFC_RPA_WALLET", WALLET);
    await mutatingProductFlow();
    await latencyBaseline();
  } else {
    addWarn("mutating NFC product flow skipped", "set NFC_RPA_RUN_MUTATING=true with terminal/app credentials");
  }

  const report = buildReport();
  writeReports(report);
  printSummary(report);
  if (report.fail > 0) process.exit(1);
}

async function publicReadiness() {
  section("Readiness");
  const health = await request("GET", "/healthz");
  record("healthz public", health, [200], "health endpoint must be public");
  const ready = await request("GET", "/readyz");
  record("readyz reports NFC/webhook state", ready, [200, 503], "readyz should not crash");
  if (ready.body && ready.body.webhooks) {
    addPass("readyz includes webhook status", `running=${ready.body.webhooks.running}`);
  } else {
    addWarn("readyz webhook status missing", "expected webhooks object");
  }
}

async function protectedTerminalSurface() {
  section("Terminal auth");
  const paths = [
    ["POST", "/api/nfc/authorize", { merchant_id: MERCHANT_ID, terminal_id: TERMINAL_ID, token: "nfc1.invalid.sig", amount_brl: AMOUNT_BRL, idempotency_key: "probe-" + randomID() }],
    ["GET", "/api/nfc/authorizations/nfc_auth_probe", null],
    ["POST", "/api/nfc/authorizations/nfc_auth_probe/capture", null],
    ["POST", "/api/nfc/authorizations/nfc_auth_probe/reverse", null],
  ];
  for (const [method, urlPath, body] of paths) {
    const res = await request(method, urlPath, jsonHeaders(), body);
    addResult({
      name: `${method} ${urlPath} rejects missing terminal key`,
      category: "terminal_auth",
      status: res.status,
      latencyMs: res.latencyMs,
      pass: [401, 403, 404].includes(res.status),
      expected: "401|403|404",
      observed: String(res.status),
      detail: "terminal endpoint must not be accessible without credential",
    });
  }
}

async function mutatingProductFlow() {
  section("Mutating product flow");
  await sandboxFund();

  const tokenA = await provisionToken("flow-authorize-capture");
  const idemA = "nfc-rpa-capture-" + randomID();
  const authA = await authorize(tokenA, idemA, AMOUNT_BRL);
  expectStatus("authorize approved/accepted", authA, [200, 202]);
  validateFinancialBreakdown("authorize financial breakdown", authA.body, AMOUNT_BRL, EXPECTED_FEE_BPS);
  const authorizationID = authA.body && authA.body.authorization_id;
  if (!authorizationID) throw new Error(`authorize did not return authorization_id: ${authA.bodyText}`);

  const replay = await authorize(tokenA, idemA, AMOUNT_BRL);
  expectStatus("same idempotency replay returns same auth", replay, [200, 202]);
  if ((replay.body && replay.body.authorization_id) !== authorizationID) {
    addFail("same idempotency replay changed authorization_id", `${authorizationID} != ${replay.body && replay.body.authorization_id}`);
  } else {
    addPass("same idempotency replay returns same authorization_id", authorizationID);
  }

  const mismatch = await authorize(tokenA, idemA, "11.00");
  expectStatus("same idempotency different payload rejected", mismatch, [409]);

  const capture = await request("POST", `/api/nfc/authorizations/${authorizationID}/capture`, terminalHeaders(), null);
  expectStatus("capture succeeds once", capture, [200]);

  const duplicateCapture = await request("POST", `/api/nfc/authorizations/${authorizationID}/capture`, terminalHeaders(), null);
  expectStatus("duplicate capture is controlled", duplicateCapture, [200, 409]);

  const tokenB = await provisionToken("flow-authorize-reverse");
  const authB = await authorize(tokenB, "nfc-rpa-reverse-" + randomID(), AMOUNT_BRL);
  expectStatus("authorize for reverse", authB, [200, 202]);
  const reverseID = authB.body && authB.body.authorization_id;
  const reverse = await request("POST", `/api/nfc/authorizations/${reverseID}/reverse`, terminalHeaders(), null);
  expectStatus("reverse succeeds", reverse, [200]);

  const wrongKey = await request("GET", `/api/nfc/authorizations/${reverseID}`, {
    Authorization: "Bearer wrong-terminal-key",
  });
  expectStatus("wrong terminal key cannot read auth", wrongKey, [401, 403]);
}

function validateFinancialBreakdown(name, body, amountBRL, feeBps) {
  if (!body || typeof body !== "object") {
    addFail(name, "missing authorization body");
    return;
  }
  const amount = Number(amountBRL);
  const merchantAmount = Number(body.merchant_amount_brl || body.amount_brl);
  const fee = Number(body.chainfx_fee_brl || 0);
  const total = Number(body.total_debit_brl || 0);
  const expectedFee = round2(amount * feeBps / 10000);
  const expectedTotal = round2(amount + expectedFee);
  const ok =
    near(merchantAmount, amount, 0.01) &&
    near(fee, expectedFee, 0.01) &&
    near(total, expectedTotal, 0.01) &&
    Number(body.required_usdt) > 0;
  addResult({
    name,
    category: "financial_invariant",
    status: ok ? "pass" : "fail",
    latencyMs: 0,
    pass: ok,
    expected: `merchant=${amount.toFixed(2)} fee=${expectedFee.toFixed(2)} total=${expectedTotal.toFixed(2)} required_usdt>0`,
    observed: `merchant=${merchantAmount} fee=${fee} total=${total} required_usdt=${body.required_usdt}`,
    detail: "NFC must hold compra + ChainFX fee in USDT while merchant settlement remains the purchase amount",
  });
}

function round2(value) {
  return Math.round(Number(value) * 100) / 100;
}

function near(a, b, tolerance) {
  return Number.isFinite(a) && Math.abs(a - b) <= tolerance;
}

async function latencyBaseline() {
  section("Latency baseline");
  for (let i = 0; i < ITERATIONS; i += 1) {
    const token = await provisionToken(`latency-${i}`);
    const res = await authorize(token, `nfc-rpa-latency-${i}-${randomID()}`, AMOUNT_BRL);
    expectStatus(`authorize latency sample ${i + 1}/${ITERATIONS}`, res, [200, 202, 422]);
  }
}

async function sandboxFund() {
  if (!FUND_USDT || Number(FUND_USDT) <= 0) return;
  const res = await request("POST", "/api/nfc/sandbox/fund", chainFXHeaders(), {
    wallet_address: WALLET,
    network: NETWORK,
    amount_usdt: FUND_USDT,
  });
  expectStatus("sandbox fund NFC ledger", res, [200, 403]);
  if (res.status === 403) addWarn("sandbox fund disabled", "continuing; wallet must already have NFC ledger balance");
}

async function provisionToken(deviceID) {
  const res = await request("POST", "/api/nfc/provision", chainFXHeaders(), {
    wallet_address: WALLET,
    device_id: `rpa-${deviceID}`,
    network: NETWORK,
    ttl_seconds: 120,
  });
  expectStatus("provision NFC token", res, [201]);
  if (!res.body || !res.body.token) throw new Error(`provision did not return token: ${res.bodyText}`);
  return res.body.token;
}

async function authorize(token, idempotencyKey, amountBRL) {
  return request("POST", "/api/nfc/authorize", terminalHeaders({ "Idempotency-Key": idempotencyKey }), {
    token,
    amount_brl: amountBRL,
    currency: "BRL",
    merchant_id: MERCHANT_ID,
    terminal_id: TERMINAL_ID,
    external_ref: `rpa-${idempotencyKey}`,
    idempotency_key: idempotencyKey,
  });
}

async function request(method, urlPath, headers = {}, body = null) {
  const started = Date.now();
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), TIMEOUT_MS);
  let status = 0, bodyText = "", responseHeaders = {};
  try {
    const res = await fetch(BASE_URL + urlPath, {
      method,
      headers,
      body: body == null ? undefined : JSON.stringify(body),
      signal: controller.signal,
    });
    status = res.status;
    responseHeaders = Object.fromEntries(res.headers.entries());
    bodyText = await res.text();
  } catch (err) {
    bodyText = err && err.message ? err.message : String(err);
  } finally {
    clearTimeout(timeout);
  }
  const latencyMs = Date.now() - started;
  latencies.push(latencyMs);
  const serverTiming = responseHeaders["server-timing"] || "";
  if (serverTiming) serverTimingSamples.push({ path: urlPath, status, serverTiming });
  let parsed = null;
  try { parsed = bodyText ? JSON.parse(bodyText) : null; } catch (_) {}
  return { status, latencyMs, headers: responseHeaders, bodyText, body: parsed, serverTiming };
}

function chainFXHeaders(extra = {}) {
  return { ...jsonHeaders(), Authorization: `Bearer ${CHAINFX_API_KEY}`, ...extra };
}

function terminalHeaders(extra = {}) {
  return { ...jsonHeaders(), Authorization: `Bearer ${TERMINAL_KEY}`, ...extra };
}

function jsonHeaders(extra = {}) {
  return { "Content-Type": "application/json", ...extra };
}

function record(name, res, expected, detail) {
  addResult({ name, category: "nfc_product", status: res.status, latencyMs: res.latencyMs, pass: expected.includes(res.status), expected: expected.join("|"), observed: String(res.status), detail });
}

function expectStatus(name, res, expected) {
  record(name, res, expected, res.serverTiming || res.bodyText.slice(0, 240));
}

function addPass(name, detail) {
  addResult({ name, pass: true, status: "ok", latencyMs: 0, expected: "pass", observed: "pass", detail });
}

function addWarn(name, detail) {
  addResult({ name, pass: true, warn: true, status: "warn", latencyMs: 0, expected: "configured", observed: "missing_or_skipped", detail });
}

function addFail(name, detail) {
  addResult({ name, pass: false, status: "fail", latencyMs: 0, expected: "pass", observed: "fail", detail });
}

function addResult(result) {
  results.push(result);
  const label = result.pass ? (result.warn ? "WARN" : "PASS") : "FAIL";
  console.log(`${label} ${result.name} status=${result.status} ${result.latencyMs || ""}ms - ${result.detail || ""}`);
}

function buildReport() {
  const sorted = [...latencies].sort((a, b) => a - b);
  const pass = results.filter((r) => r.pass && !r.warn).length;
  const warn = results.filter((r) => r.warn).length;
  const fail = results.filter((r) => !r.pass).length;
  return {
    target: BASE_URL,
    mode: RUN_MUTATING ? "mutating" : "non_mutating",
    generated_at: new Date().toISOString(),
    pass,
    warn,
    fail,
    total: results.length,
    latency_ms: {
      min: sorted[0] || 0,
      p50: pct(sorted, 50),
      p75: pct(sorted, 75),
      p90: pct(sorted, 90),
      p95: pct(sorted, 95),
      p99: pct(sorted, 99),
      max: sorted[sorted.length - 1] || 0,
    },
    server_timing_samples: serverTimingSamples,
    results,
  };
}

function writeReports(report) {
  fs.mkdirSync(REPORT_DIR, { recursive: true });
  const stamp = new Date().toISOString().replace(/[:.]/g, "-");
  const jsonPath = path.join(REPORT_DIR, `nfc-product-report-${stamp}.json`);
  const txtPath = path.join(REPORT_DIR, `nfc-product-report-${stamp}.txt`);
  fs.writeFileSync(jsonPath, JSON.stringify(report, null, 2));
  fs.writeFileSync(txtPath, renderText(report));
  console.log(`\nReports:\n  ${jsonPath}\n  ${txtPath}`);
}

function renderText(report) {
  return [
    "ChainFX Tap NFC product RPA",
    `Target: ${report.target}`,
    `Mode: ${report.mode}`,
    `Summary: PASS=${report.pass} WARN=${report.warn} FAIL=${report.fail} TOTAL=${report.total}`,
    `Latency ms: min=${report.latency_ms.min} p50=${report.latency_ms.p50} p75=${report.latency_ms.p75} p90=${report.latency_ms.p90} p95=${report.latency_ms.p95} p99=${report.latency_ms.p99} max=${report.latency_ms.max}`,
    "",
    ...report.results.map((r) => `${r.pass ? (r.warn ? "WARN" : "PASS") : "FAIL"} ${r.name} status=${r.status} detail=${r.detail || ""}`),
    "",
    "Server-Timing samples:",
    ...report.server_timing_samples.slice(-20).map((s) => `${s.status} ${s.path} ${s.serverTiming}`),
  ].join("\n");
}

function printSummary(report) {
  console.log("\nSummary:");
  console.log(`  PASS=${report.pass} WARN=${report.warn} FAIL=${report.fail} TOTAL=${report.total}`);
  console.log(`  Latency ms: min=${report.latency_ms.min} p50=${report.latency_ms.p50} p75=${report.latency_ms.p75} p90=${report.latency_ms.p90} p95=${report.latency_ms.p95} p99=${report.latency_ms.p99} max=${report.latency_ms.max}`);
}

function pct(sorted, p) {
  if (!sorted.length) return 0;
  const idx = Math.min(sorted.length - 1, Math.ceil((p / 100) * sorted.length) - 1);
  return sorted[idx];
}

function cleanBaseURL(v) {
  return String(v || "").replace(/\/+$/, "");
}

function randomID() {
  return crypto.randomBytes(8).toString("hex");
}

function intEnv(name, fallback) {
  const n = Number.parseInt(process.env[name] || "", 10);
  return Number.isFinite(n) ? n : fallback;
}

function boolEnv(name, fallback) {
  const v = String(process.env[name] || "").toLowerCase();
  if (["1", "true", "yes", "on"].includes(v)) return true;
  if (["0", "false", "no", "off"].includes(v)) return false;
  return fallback;
}

function requireEnv(name, value) {
  if (!value) throw new Error(`${name} is required`);
}

function section(name) {
  console.log(`\n[${name}]`);
}
