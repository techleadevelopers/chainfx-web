/**
 * ChainFX — k6 Stress Test (production-grade)
 *
 * Testa os endpoints mais críticos sob carga real:
 *   - /agent/v1/capabilities  (discovery — sem write)
 *   - /mcp/tools/call         (execução MCP — rota mais cara)
 *   - /api/mobile/assets      (mobile API — leitura pública)
 *
 * SLOs (contratual de produção):
 *   p50 < 100ms, p95 < 300ms, erro < 0.01%
 *
 * Uso:
 *   k6 run --env API_KEY=sk_live_cfx_... --env BASE_URL=https://api.chainfx.com tests/stress_production.js
 */

import http from 'k6/http';
import { check, group, sleep } from 'k6';
import { Rate, Trend, Counter } from 'k6/metrics';

// ─── Custom metrics ──────────────────────────────────────────────────────────

const rateLimitedRate  = new Rate('chainfx_rate_limited');
const mcpDuration      = new Trend('chainfx_mcp_duration', true);
const overpaymentAlerts = new Counter('chainfx_overpayment_alerts');

// ─── Test config ─────────────────────────────────────────────────────────────

export const options = {
  scenarios: {
    // Sustained baseline — simulates normal agent traffic
    baseline: {
      executor: 'constant-arrival-rate',
      rate: 50,
      timeUnit: '1s',
      duration: '2m',
      preAllocatedVUs: 50,
      maxVUs: 100,
      tags: { scenario: 'baseline' },
    },
    // Spike — simulates a burst of MCP tool calls (AI agents waking up simultaneously)
    spike: {
      executor: 'ramping-arrival-rate',
      startRate: 10,
      timeUnit: '1s',
      stages: [
        { duration: '30s', target: 200 },
        { duration: '1m',  target: 200 },
        { duration: '30s', target: 10  },
      ],
      preAllocatedVUs: 100,
      maxVUs: 400,
      startTime: '2m',
      tags: { scenario: 'spike' },
    },
  },

  thresholds: {
    // Global SLO
    http_req_duration: ['p(50)<100', 'p(95)<300', 'p(99)<800'],
    http_req_failed:   ['rate<0.0001'],   // < 0.01% error

    // MCP-specific (AI calls are heavier)
    'chainfx_mcp_duration': ['p(95)<500'],

    // Rate limiting must engage before errors do
    'chainfx_rate_limited': ['rate<0.05'],  // < 5% rate-limited is acceptable
  },
};

// ─── Shared headers ───────────────────────────────────────────────────────────

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const API_KEY  = __ENV.API_KEY  || 'sk_test_cfx_stress_placeholder';

const headers = {
  'Content-Type':    'application/json',
  'Authorization':   `Bearer ${API_KEY}`,
  'X-Api-Key':       API_KEY,
};

// ─── Scenarios ────────────────────────────────────────────────────────────────

export default function () {
  const scenario = __ENV.K6_SCENARIO_NAME || 'baseline';

  group('discovery', () => {
    const res = http.get(`${BASE_URL}/agent/v1/capabilities`, { headers, tags: { name: 'capabilities' } });
    check(res, {
      'capabilities 200': (r) => r.status === 200,
      'has capabilities array': (r) => {
        try { return JSON.parse(r.body).capabilities !== undefined; } catch { return false; }
      },
    });
  });

  group('mobile_assets', () => {
    const res = http.get(`${BASE_URL}/api/mobile/assets`, { headers, tags: { name: 'mobile_assets' } });
    check(res, {
      'assets 200': (r) => r.status === 200,
      'no BUSD in response': (r) => {
        try {
          const body = JSON.parse(r.body);
          return !(JSON.stringify(body)).includes('"BUSD"');
        } catch { return true; }
      },
    });
  });

  group('mcp_tools_call', () => {
    const start = Date.now();
    const res = http.post(
      `${BASE_URL}/mcp/tools/call`,
      JSON.stringify({
        name: 'get_rates',
        arguments: {},
      }),
      { headers, tags: { name: 'mcp_tools_call' } }
    );
    mcpDuration.add(Date.now() - start);

    const passed = check(res, {
      'mcp 200 or 429': (r) => r.status === 200 || r.status === 429,
      'mcp not 5xx': (r) => r.status < 500,
    });

    if (res.status === 429) {
      rateLimitedRate.add(1);
    } else {
      rateLimitedRate.add(0);
    }
  });

  group('metrics_endpoint', () => {
    // Only hit /metrics occasionally (it's admin-gated)
    if (Math.random() < 0.02) {
      const res = http.get(`${BASE_URL}/metrics`, {
        headers: { Authorization: `Bearer ${__ENV.ADMIN_KEY || API_KEY}` },
        tags: { name: 'metrics' },
      });
      // Check for overpayment alert in the metrics body
      if (res.status === 200 && res.body.includes('chainfx_m2m_overpayment_total')) {
        const match = res.body.match(/chainfx_m2m_overpayment_total (\d+)/);
        if (match && parseInt(match[1], 10) > 0) {
          overpaymentAlerts.add(1);
          console.warn(`[ALERT] Overpayments detected: ${match[1]}`);
        }
      }
    }
  });

  // Realistic think time: agents call at 100–500ms intervals
  sleep(0.1 + Math.random() * 0.4);
}

export function handleSummary(data) {
  return {
    'tests/stress_results.json': JSON.stringify(data, null, 2),
    stdout: textSummary(data),
  };
}

function textSummary(data) {
  const m = data.metrics;
  const p50  = m.http_req_duration?.values?.['p(50)']?.toFixed(2) ?? '?';
  const p95  = m.http_req_duration?.values?.['p(95)']?.toFixed(2) ?? '?';
  const p99  = m.http_req_duration?.values?.['p(99)']?.toFixed(2) ?? '?';
  const fail = ((m.http_req_failed?.values?.rate ?? 0) * 100).toFixed(4);
  const rl   = ((m.chainfx_rate_limited?.values?.rate ?? 0) * 100).toFixed(2);
  return `
╔══════════════════════════════════════════════╗
║   ChainFX Stress Test — Production Results  ║
╠══════════════════════════════════════════════╣
║  p50  latency : ${p50.padStart(8)} ms                  ║
║  p95  latency : ${p95.padStart(8)} ms  (SLO ≤ 300ms)  ║
║  p99  latency : ${p99.padStart(8)} ms                  ║
║  Error rate   : ${fail.padStart(8)} %   (SLO < 0.01%) ║
║  Rate limited : ${rl.padStart(8)} %   (SLO < 5.00%)  ║
╚══════════════════════════════════════════════╝
`;
}
