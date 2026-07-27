import http from "k6/http";
import { check, fail, sleep } from "k6";
import { Rate, Trend } from "k6/metrics";

const authorizeLatency = new Trend("nfc_authorize_latency", true);
const provisionLatency = new Trend("nfc_provision_latency", true);
const failedRequests = new Rate("nfc_failed_requests");

export const options = {
  scenarios: {
    steady_load: {
      executor: "constant-arrival-rate",
      rate: Number(__ENV.NFC_K6_RATE || 10),
      timeUnit: "1s",
      duration: __ENV.NFC_K6_DURATION || "2m",
      preAllocatedVUs: Number(__ENV.NFC_K6_PREALLOCATED_VUS || 25),
      maxVUs: Number(__ENV.NFC_K6_MAX_VUS || 100),
    },
  },
  thresholds: {
    nfc_authorize_latency: ["p(50)<100", "p(95)<250", "p(99)<500"],
    nfc_failed_requests: ["rate<0.001"],
    http_req_failed: ["rate<0.001"],
  },
};

const baseURL = (__ENV.NFC_BASE_URL || "http://127.0.0.1:8080").replace(/\/+$/, "");
const chainfxKey = __ENV.NFC_CHAINFX_API_KEY || "";
const terminalKey = __ENV.NFC_TERMINAL_KEY || "";
const merchantID = __ENV.NFC_MERCHANT_ID || "merchant_demo";
const terminalID = __ENV.NFC_TERMINAL_ID || "terminal_01";
const wallet = __ENV.NFC_WALLET || "";
const network = __ENV.NFC_NETWORK || "BSC";
const amountBRL = __ENV.NFC_AMOUNT_BRL || "10.00";

export default function () {
  if (!chainfxKey || !terminalKey || !wallet) {
    fail("NFC_CHAINFX_API_KEY, NFC_TERMINAL_KEY and NFC_WALLET are required");
  }

  const deviceID = `k6-${__VU}-${__ITER}`;
  const provisionBody = JSON.stringify({
    wallet_address: wallet,
    device_id: deviceID,
    network,
    ttl_seconds: 120,
  });
  const provision = http.post(`${baseURL}/api/nfc/provision`, provisionBody, {
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${chainfxKey}`,
    },
    timeout: "2s",
  });
  provisionLatency.add(provision.timings.duration);
  const provisionOK = check(provision, {
    "provision 201": (r) => r.status === 201,
  });
  if (!provisionOK) {
    failedRequests.add(1);
    return;
  }

  const token = provision.json("token");
  const idempotencyKey = `k6-${__VU}-${__ITER}-${Date.now()}`;
  const authorizeBody = JSON.stringify({
    merchant_id: merchantID,
    terminal_id: terminalID,
    token,
    amount_brl: amountBRL,
    currency: "BRL",
    external_ref: idempotencyKey,
    idempotency_key: idempotencyKey,
  });
  const authorize = http.post(`${baseURL}/api/nfc/authorize`, authorizeBody, {
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${terminalKey}`,
      "Idempotency-Key": idempotencyKey,
    },
    timeout: "2s",
  });
  authorizeLatency.add(authorize.timings.duration);
  const authorizeOK = check(authorize, {
    "authorize accepted or controlled decline": (r) => [200, 202, 422, 503].includes(r.status),
    "server timing present": (r) => Boolean(r.headers["Server-Timing"]),
  });
  failedRequests.add(!authorizeOK);
  sleep(0.05);
}
