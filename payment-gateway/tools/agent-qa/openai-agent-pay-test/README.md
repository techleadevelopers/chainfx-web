# ChainFX Agent QA

External QA harness for Agent Pay interoperability. It receives only the public Agent Card URL, validates the trust layer, discovers skills, calls `/a2a`, creates a PIX intent when credentials are provided, checks status, and writes a JSON report.

It does not call internal REST, MCP, or backend-private routes directly.

Its purpose is to behave like a real economic agent before ChainFX depends on third-party agents. A passing report means an external agent can discover ChainFX, verify identity, understand policy prerequisites, plan the capability/payment sequence and execute through A2A/x402 without private documentation.

Trust checks covered:

- JWKS discovery.
- Agent Card signature discovery.
- Agent Card SHA-256 hash verification.
- Ed25519 signature verification.
- Reputation document discovery.
- SLA document discovery.
- A2A task lifecycle creation and status polling.
- x402 discovery and HTTP 402 capability challenge.
- Multi-registry discovery documents for AGNTCY/OASF-style publication.
- Agent policy discovery and capability graph planning documents.
- Agent Graph v2 validation, including `pay_pix_with_usdt` dependencies, outputs, failure modes, recovery actions, cost source, latency target and phase report.
- Capability compositions discovery and Planner API validation. The planner call is non-executing and must return steps, missing requirements, estimated cost and estimated latency.
- Episode-derived reputation validation and Capability Graph Registry validation for Phase 3 provider comparison.

## Run discovery without credentials

```powershell
cd C:\Users\Paulo\Desktop\payment-gateway
node tools\agent-qa\openai-agent-pay-test\index.mjs --card "https://api-production-bc748.up.railway.app/.well-known/agent-card.json"
```

Expected result without a bearer token: card discovery works, public methods work, and the report stops at the authenticated quote/payment step with `outcome: discovery_ok_auth_required`.

## Run full Agent Pay QA

```powershell
cd C:\Users\Paulo\Desktop\payment-gateway
$env:CHAINFX_API_KEY="YOUR_CHAINFX_API_KEY"
$env:CHAINFX_AGENT_WALLET="0x830000000000000000000000000000000000019a"
$env:CHAINFX_PIX_KEY="+5511999999999"
$env:CHAINFX_AMOUNT_BRL="10.00"
node tools\agent-qa\openai-agent-pay-test\index.mjs --card "https://api-production-bc748.up.railway.app/.well-known/agent-card.json" --out ".\agent-qa-report.json"
```

The report answers whether the agent verified ChainFX identity, discovered registry manifests, discovered policy prerequisites, read the capability graph, created an asynchronous A2A task, received an x402 capability payment challenge, selected `pay_pix_with_usdt`, called `quote_required_usdt`, created an intent, checked status, and where it failed if the flow did not complete.

If the payment intent returns `AGENT_POLICY_REQUIRED`, the script reports `outcome: policy_required_before_payment_intent` instead of treating it as a transport bug. That means discovery worked and the next required action is to create or activate the wallet policy.

## Agent Graph v2 acceptance

The QA report includes `capability_graph_v2` and these checks:

- `capability_graph_v2_validated`
- `pay_pix_graph_contract_validated`
- `graph_phase_report_detected`

For Phase 1 to pass, `/.well-known/capability-graph.json` must expose version `2.0.0`, an `agent_graph_v2_report`, and a complete `pay_pix_with_usdt` contract with `requires`, `produces`, `next`, `preconditions`, `failure_modes`, `recovery_actions`, `estimated_cost`, `expected_latency_ms` and `policy_requirements`.

## Planning Layer acceptance

The QA report includes `capability_compositions`, `planner_api` and these checks:

- `capability_compositions_fetched`
- `capability_compositions_validated`
- `planner_api_called`
- `planner_api_validated`

For Phase 2 to pass, `/.well-known/capability-compositions.json` must expose a `planning_layer_report`, and `POST /agent/v1/plans` must return a non-executing plan with `plan_id`, `steps`, `missing_requirements`, `estimated_cost_usdt`, `estimated_latency_ms`, and `executes_now: false`.

## Reputation + Graph Registry acceptance

The QA report includes `episode_derived_reputation`, `graph_registry` and these checks:

- `episode_reputation_validated`
- `graph_registry_fetched`
- `graph_registry_validated`

For Phase 3 to pass, `/.well-known/agent-reputation.json` must expose `phase_report.id = reputation_graph_registry_report` and a nested `reputation` object with score, success rate, latency percentiles and per-skill metrics. `/.well-known/capability-graph-registry.json` must connect payment, marketplace, stablecoin, trust, planning and observability groups and publish a `reputation_graph_registry_report`.
