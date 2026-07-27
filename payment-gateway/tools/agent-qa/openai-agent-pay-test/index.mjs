#!/usr/bin/env node

import { writeFile } from 'node:fs/promises';
import { createHash, createPublicKey, verify } from 'node:crypto';

const DEFAULT_CARD_URL = 'https://api-production-bc748.up.railway.app/.well-known/agent-card.json';
const DEFAULT_AGENT_WALLET = '0x830000000000000000000000000000000000019a';

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i += 1) {
    const current = argv[i];
    if (!current.startsWith('--')) continue;
    const key = current.slice(2);
    const next = argv[i + 1];
    if (!next || next.startsWith('--')) {
      out[key] = true;
      continue;
    }
    out[key] = next;
    i += 1;
  }
  return out;
}

const args = parseArgs(process.argv.slice(2));
const cardUrl = args.card || process.env.CHAINFX_AGENT_CARD_URL || DEFAULT_CARD_URL;
const reportPath = args.out || process.env.CHAINFX_AGENT_QA_OUT || '';
const bearer = args.bearer || process.env.CHAINFX_API_KEY || process.env.OPENAI_AGENT_QA_BEARER || '';
const amountBRL = String(args.amount || process.env.CHAINFX_AMOUNT_BRL || '10.00');
const pixKey = args.pix || process.env.CHAINFX_PIX_KEY || '+5511999999999';
const agentWallet = args.wallet || process.env.CHAINFX_AGENT_WALLET || DEFAULT_AGENT_WALLET;

const report = {
  started_at: new Date().toISOString(),
  card_url: cardUrl,
  objective: 'Discover ChainFX Agent Pay from Agent Card only and execute A2A payment flow.',
  inputs: {
    amount_brl: amountBRL,
    pix_key_configured: Boolean(pixKey),
    agent_wallet_configured: Boolean(agentWallet),
    bearer_configured: Boolean(bearer),
  },
  checks: {
    card_fetched: false,
    a2a_url_detected: false,
    skills_detected: false,
    payment_skill_selected: false,
    quote_called: false,
    auth_handled: false,
    intent_created: false,
    status_checked: false,
    jwks_fetched: false,
    signature_fetched: false,
    card_hash_verified: false,
    card_signature_verified: false,
    reputation_fetched: false,
    sla_fetched: false,
    task_created: false,
    task_status_checked: false,
    task_events_url_detected: false,
    x402_discovered: false,
    x402_challenge_received: false,
    registries_fetched: false,
    agntcy_fetched: false,
    oasf_fetched: false,
    registry_record_fetched: false,
    policy_discovery_fetched: false,
    capability_graph_fetched: false,
    capability_graph_v2_validated: false,
    pay_pix_graph_contract_validated: false,
    graph_phase_report_detected: false,
    capability_compositions_fetched: false,
    capability_compositions_validated: false,
    planner_api_called: false,
    planner_api_validated: false,
    episode_reputation_validated: false,
    graph_registry_fetched: false,
    graph_registry_validated: false,
    policy_required_detected: false,
  },
  selected_skills: {},
  steps: [],
  completed: false,
};

function addStep(name, data) {
  report.steps.push({
    name,
    at: new Date().toISOString(),
    ...data,
  });
}

function redactHeaders(headers) {
  const copy = { ...headers };
  if (copy.Authorization) copy.Authorization = 'Bearer ***';
  return copy;
}

function safeJson(value) {
  if (value == null) return null;
  if (typeof value !== 'object') return value;
  return JSON.parse(JSON.stringify(value));
}

function compactBody(body) {
  if (body == null) return null;
  const serialized = JSON.stringify(body);
  if (serialized.length <= 4000) return body;
  return {
    truncated: true,
    preview: serialized.slice(0, 4000),
  };
}

async function requestJSON(url, options = {}) {
  const started = performance.now();
  const response = await fetch(url, {
    ...options,
    headers: {
      Accept: 'application/json',
      ...(options.body ? { 'Content-Type': 'application/json' } : {}),
      ...(options.headers || {}),
    },
  });
  const latencyMs = Math.round(performance.now() - started);
  const text = await response.text();
  let body = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = { raw: text };
    }
  }
  return {
    ok: response.ok,
    status: response.status,
    latency_ms: latencyMs,
    text,
    body,
  };
}

function normalizeSkill(skill) {
  const id = String(skill?.id || skill?.name || '').toLowerCase();
  const name = String(skill?.name || '').toLowerCase();
  const description = String(skill?.description || '').toLowerCase();
  const tags = Array.isArray(skill?.tags) ? skill.tags.map((tag) => String(tag).toLowerCase()) : [];
  return { id, name, description, tags };
}

function findSkill(skills, predicates) {
  for (const skill of skills) {
    const normalized = normalizeSkill(skill);
    if (predicates.some((predicate) => predicate(normalized))) {
      return skill.id || skill.name;
    }
  }
  return '';
}

function extractA2AUrl(card) {
  if (typeof card?.url === 'string' && card.url) return card.url;
  if (typeof card?.endpoints?.a2a === 'string' && card.endpoints.a2a) return card.endpoints.a2a;
  if (typeof card?.a2a === 'string' && card.a2a) return card.a2a;
  return '';
}

function extractA2ATasksUrl(card, a2aUrl) {
  if (typeof card?.endpoints?.a2a_tasks === 'string' && card.endpoints.a2a_tasks) return card.endpoints.a2a_tasks;
  try {
    return new URL('/a2a/tasks', a2aUrl).toString();
  } catch {
    return '';
  }
}

function extractX402ExecuteUrl(card, cardUrlValue) {
  const raw = card?.endpoints?.x402_execute || '/x402/capabilities/{capability}/execute';
  return absoluteFromCard(cardUrlValue, String(raw).replace('{capability}', 'document_ocr'));
}

function absoluteFromCard(cardUrlValue, pathOrUrl) {
  if (!pathOrUrl) return '';
  try {
    return new URL(pathOrUrl, cardUrlValue).toString();
  } catch {
    return '';
  }
}

function findJWK(jwks, kid) {
  const keys = Array.isArray(jwks?.keys) ? jwks.keys : [];
  return keys.find((key) => key?.kid === kid) || keys.find((key) => key?.crv === 'Ed25519') || null;
}

function verifyAgentCardSignature(cardText, signatureBody, jwksBody) {
  const canonicalCard = cardText.trim();
  const expectedHash = signatureBody?.card_hash;
  const actualHash = createHash('sha256').update(canonicalCard).digest('hex');
  const hashOk = Boolean(expectedHash && expectedHash === actualHash);
  const jwk = findJWK(jwksBody, signatureBody?.public_key_id);
  let signatureOk = false;
  if (jwk?.x && signatureBody?.signature) {
    const key = createPublicKey({ key: { ...jwk, key_ops: ['verify'], ext: true }, format: 'jwk' });
    signatureOk = verify(null, Buffer.from(canonicalCard), key, Buffer.from(signatureBody.signature, 'base64url'));
  }
  return { hashOk, signatureOk, actualHash, expectedHash, publicKeyId: jwk?.kid || null };
}

function validateCapabilityGraphV2(graphBody) {
  const contracts = Array.isArray(graphBody?.skill_contracts)
    ? graphBody.skill_contracts
    : Array.isArray(graphBody?.skills)
      ? graphBody.skills
      : [];
  const payPix = contracts.find((contract) => contract?.skill === 'pay_pix_with_usdt');
  const requiredArrays = ['requires', 'produces', 'next', 'preconditions', 'recovery_actions', 'policy_requirements'];
  const missingArrays = requiredArrays.filter((key) => !Array.isArray(payPix?.[key]) || payPix[key].length === 0);
  const validFailureModes = payPix?.failure_modes && typeof payPix.failure_modes === 'object' && Object.keys(payPix.failure_modes).length > 0;
  const validCost = payPix?.estimated_cost && typeof payPix.estimated_cost === 'object';
  const validLatency = payPix?.expected_latency_ms && typeof payPix.expected_latency_ms === 'object';
  const phaseReport = graphBody?.phase_report || {};
  return {
    version: graphBody?.version || '',
    skill_contract_count: contracts.length,
    pay_pix_contract_found: Boolean(payPix),
    missing_required_arrays: missingArrays,
    has_failure_modes: Boolean(validFailureModes),
    has_estimated_cost: Boolean(validCost),
    has_expected_latency: Boolean(validLatency),
    phase_report_id: phaseReport.id || '',
    phase_report_detected: phaseReport.id === 'agent_graph_v2_report',
    valid: graphBody?.version === '2.0.0'
      && Boolean(payPix)
      && missingArrays.length === 0
      && Boolean(validFailureModes)
      && Boolean(validCost)
      && Boolean(validLatency)
      && phaseReport.id === 'agent_graph_v2_report',
  };
}

function validateCapabilityCompositions(body) {
  const compositions = Array.isArray(body?.compositions) ? body.compositions : [];
  const documentPayment = compositions.find((item) => item?.id === 'document_to_memory_payment');
  const pixPayment = compositions.find((item) => item?.id === 'pix_payment_with_quote');
  return {
    version: body?.version || '',
    composition_count: compositions.length,
    document_to_memory_payment_found: Boolean(documentPayment),
    pix_payment_with_quote_found: Boolean(pixPayment),
    phase_report_id: body?.phase_report?.id || '',
    valid: compositions.length >= 2
      && Boolean(documentPayment)
      && Boolean(pixPayment)
      && body?.phase_report?.id === 'planning_layer_report',
  };
}

function validatePlannerResponse(body) {
  return {
    plan_id: body?.plan_id || '',
    status: body?.status || '',
    composition_id: body?.composition_id || '',
    step_count: Array.isArray(body?.steps) ? body.steps.length : 0,
    executes_now: body?.executes_now,
    has_missing_requirements: Array.isArray(body?.missing_requirements),
    estimated_cost_usdt: body?.estimated_cost_usdt || '',
    estimated_latency_ms: body?.estimated_latency_ms || 0,
    phase_report_id: body?.phase_report?.id || '',
    valid: Boolean(body?.plan_id)
      && Array.isArray(body?.steps)
      && body.steps.includes('quote_required_usdt')
      && body.steps.includes('pay_pix_with_usdt')
      && body.executes_now === false
      && Array.isArray(body?.missing_requirements)
      && Boolean(body?.estimated_cost_usdt)
      && Number(body?.estimated_latency_ms || 0) > 0
      && body?.phase_report?.id === 'planning_layer_report',
  };
}

function validateEpisodeDerivedReputation(body) {
  const reputation = body?.reputation || {};
  const bySkill = reputation.by_skill || {};
  return {
    score: reputation.score || body?.reputation_score || '',
    total_episodes: Number(reputation.total_episodes ?? body?.total_episodes ?? 0),
    success_rate: reputation.success_rate || body?.success_rate || '',
    has_latency_percentiles: Boolean(reputation.latency_ms?.p50 !== undefined && reputation.latency_ms?.p95 !== undefined && reputation.latency_ms?.p99 !== undefined),
    by_skill_count: Object.keys(bySkill).length,
    failures_by_type_count: Object.keys(reputation.failures_by_type || {}).length,
    phase_report_id: body?.phase_report?.id || '',
    valid: Boolean(reputation.score || body?.reputation_score)
      && typeof reputation === 'object'
      && Boolean(reputation.latency_ms)
      && typeof bySkill === 'object'
      && body?.phase_report?.id === 'reputation_graph_registry_report',
  };
}

function validateGraphRegistry(body) {
  const graph = body?.graph || {};
  const requiredGroups = ['payment', 'marketplace', 'stablecoin', 'trust', 'planning', 'observability'];
  const missingGroups = requiredGroups.filter((group) => !Array.isArray(graph[group]) || graph[group].length === 0);
  return {
    version: body?.version || '',
    missing_groups: missingGroups,
    relation_count: Array.isArray(body?.relations) ? body.relations.length : 0,
    provider_comparison: Boolean(body?.provider_comparison),
    phase_report_id: body?.phase_report?.id || '',
    valid: missingGroups.length === 0
      && Array.isArray(body?.relations)
      && body.relations.length > 0
      && Boolean(body?.provider_comparison)
      && body?.phase_report?.id === 'reputation_graph_registry_report',
  };
}

async function callA2A(a2aUrl, skill, payload, authMode = 'optional') {
  const headers = {};
  if (bearer) headers.Authorization = `Bearer ${bearer}`;
  const requestBody = {
    jsonrpc: '2.0',
    id: `agentqa-${Date.now()}-${Math.random().toString(16).slice(2)}`,
    skill,
    arguments: payload,
  };
  const response = await requestJSON(a2aUrl, {
    method: 'POST',
    headers,
    body: JSON.stringify(requestBody),
  });
  const unauthorized = response.status === 401 || response.status === 403;
  if (unauthorized && authMode === 'required') {
    report.checks.auth_handled = true;
  }
  addStep(`a2a:${skill}`, {
    request: {
      url: a2aUrl,
      method: 'POST',
      headers: redactHeaders(headers),
      body: requestBody,
    },
    response: {
      status: response.status,
      latency_ms: response.latency_ms,
      body: compactBody(response.body),
    },
  });
  return response;
}

async function createA2ATask(tasksUrl, skill, payload) {
  const headers = {};
  if (bearer) headers.Authorization = `Bearer ${bearer}`;
  const requestBody = {
    jsonrpc: '2.0',
    id: `agentqa-task-${Date.now()}-${Math.random().toString(16).slice(2)}`,
    skill,
    arguments: payload,
  };
  const response = await requestJSON(tasksUrl, {
    method: 'POST',
    headers,
    body: JSON.stringify(requestBody),
  });
  addStep(`a2a-task:create:${skill}`, {
    request: {
      url: tasksUrl,
      method: 'POST',
      headers: redactHeaders(headers),
      body: requestBody,
    },
    response: {
      status: response.status,
      latency_ms: response.latency_ms,
      body: compactBody(response.body),
    },
  });
  return response;
}

function pickIntentId(body) {
  const candidates = [
    body?.result?.payment?.intent_id,
    body?.result?.payment?.id,
    body?.result?.payment?.payment_intent_id,
    body?.result?.intent_id,
    body?.result?.id,
    body?.payment?.intent_id,
    body?.payment?.id,
    body?.intent_id,
    body?.id,
  ];
  return candidates.find((value) => typeof value === 'string' && value.trim()) || '';
}

async function main() {
  try {
    const cardResponse = await requestJSON(cardUrl);
    addStep('fetch_agent_card', {
      request: { url: cardUrl, method: 'GET' },
      response: {
        status: cardResponse.status,
        latency_ms: cardResponse.latency_ms,
        body: compactBody(cardResponse.body),
      },
    });

    if (!cardResponse.ok || !cardResponse.body) {
      report.failed_at = 'fetch_agent_card';
      throw new Error(`Agent Card fetch failed with HTTP ${cardResponse.status}`);
    }

    report.checks.card_fetched = true;
    const card = cardResponse.body;
    const a2aUrl = extractA2AUrl(card);
    if (!a2aUrl) {
      report.failed_at = 'extract_a2a_url';
      throw new Error('Agent Card does not expose an A2A URL.');
    }
    report.a2a_url = a2aUrl;
    report.checks.a2a_url_detected = true;
    const a2aTasksUrl = extractA2ATasksUrl(card, a2aUrl);
    report.a2a_tasks_url = a2aTasksUrl || null;

    const identity = card.agent_identity || {};
    const jwksUrl = absoluteFromCard(cardUrl, identity.jwks_url || '/.well-known/jwks.json');
    const signatureUrl = absoluteFromCard(cardUrl, identity.signature_url || '/.well-known/agent-card.signature');
    const reputationUrl = absoluteFromCard(cardUrl, identity.reputation || '/.well-known/agent-reputation.json');
    const slaUrl = absoluteFromCard(cardUrl, identity.sla || '/.well-known/agent-sla.json');
    const x402DiscoveryUrl = absoluteFromCard(cardUrl, '/.well-known/x402.json');
    const registriesUrl = absoluteFromCard(cardUrl, card.registry?.index || '/agent/v1/registries');
    const agntcyUrl = absoluteFromCard(cardUrl, card.registry?.agntcy || '/.well-known/agntcy.json');
    const oasfUrl = absoluteFromCard(cardUrl, card.registry?.oasf || '/.well-known/oasf.json');
    const registryRecordUrl = absoluteFromCard(cardUrl, card.registry?.signedRecord || '/agent/v1/registry-records/agntcy-oasf');
    const policyDiscoveryUrl = absoluteFromCard(cardUrl, card.planning?.policy_discovery || '/.well-known/agent-policy.json');
    const capabilityGraphUrl = absoluteFromCard(cardUrl, card.planning?.capability_graph || '/.well-known/capability-graph.json');
    const compositionsUrl = absoluteFromCard(cardUrl, card.planning?.compositions || '/.well-known/capability-compositions.json');
    const plannerUrl = absoluteFromCard(cardUrl, card.planning?.planner_api || '/agent/v1/plans');
    const graphRegistryUrl = absoluteFromCard(cardUrl, card.planning?.graph_registry || card.registry?.graph || '/.well-known/capability-graph-registry.json');

    const [jwksResponse, signatureResponse, reputationResponse, slaResponse, x402DiscoveryResponse, registriesResponse, agntcyResponse, oasfResponse, registryRecordResponse, policyDiscoveryResponse, capabilityGraphResponse, compositionsResponse, graphRegistryResponse] = await Promise.all([
      requestJSON(jwksUrl),
      requestJSON(signatureUrl),
      requestJSON(reputationUrl),
      requestJSON(slaUrl),
      requestJSON(x402DiscoveryUrl),
      requestJSON(registriesUrl),
      requestJSON(agntcyUrl),
      requestJSON(oasfUrl),
      requestJSON(registryRecordUrl),
      requestJSON(policyDiscoveryUrl),
      requestJSON(capabilityGraphUrl),
      requestJSON(compositionsUrl),
      requestJSON(graphRegistryUrl),
    ]);
    addStep('fetch_agent_trust_documents', {
      requests: { jwks_url: jwksUrl, signature_url: signatureUrl, reputation_url: reputationUrl, sla_url: slaUrl, graph_registry_url: graphRegistryUrl },
      responses: {
        jwks: { status: jwksResponse.status, latency_ms: jwksResponse.latency_ms, body: compactBody(jwksResponse.body) },
        signature: { status: signatureResponse.status, latency_ms: signatureResponse.latency_ms, body: compactBody(signatureResponse.body) },
        reputation: { status: reputationResponse.status, latency_ms: reputationResponse.latency_ms, body: compactBody(reputationResponse.body) },
        sla: { status: slaResponse.status, latency_ms: slaResponse.latency_ms, body: compactBody(slaResponse.body) },
        x402: { status: x402DiscoveryResponse.status, latency_ms: x402DiscoveryResponse.latency_ms, body: compactBody(x402DiscoveryResponse.body) },
        registries: { status: registriesResponse.status, latency_ms: registriesResponse.latency_ms, body: compactBody(registriesResponse.body) },
        agntcy: { status: agntcyResponse.status, latency_ms: agntcyResponse.latency_ms, body: compactBody(agntcyResponse.body) },
        oasf: { status: oasfResponse.status, latency_ms: oasfResponse.latency_ms, body: compactBody(oasfResponse.body) },
        registry_record: { status: registryRecordResponse.status, latency_ms: registryRecordResponse.latency_ms, body: compactBody(registryRecordResponse.body) },
        policy_discovery: { status: policyDiscoveryResponse.status, latency_ms: policyDiscoveryResponse.latency_ms, body: compactBody(policyDiscoveryResponse.body) },
        capability_graph: { status: capabilityGraphResponse.status, latency_ms: capabilityGraphResponse.latency_ms, body: compactBody(capabilityGraphResponse.body) },
        capability_compositions: { status: compositionsResponse.status, latency_ms: compositionsResponse.latency_ms, body: compactBody(compositionsResponse.body) },
        graph_registry: { status: graphRegistryResponse.status, latency_ms: graphRegistryResponse.latency_ms, body: compactBody(graphRegistryResponse.body) },
      },
    });
    report.checks.jwks_fetched = jwksResponse.ok;
    report.checks.signature_fetched = signatureResponse.ok;
    report.checks.reputation_fetched = reputationResponse.ok;
    report.checks.sla_fetched = slaResponse.ok;
    report.checks.x402_discovered = x402DiscoveryResponse.ok;
    report.checks.registries_fetched = registriesResponse.ok;
    report.checks.agntcy_fetched = agntcyResponse.ok;
    report.checks.oasf_fetched = oasfResponse.ok;
    report.checks.registry_record_fetched = registryRecordResponse.ok;
    report.checks.policy_discovery_fetched = policyDiscoveryResponse.ok;
    report.checks.capability_graph_fetched = capabilityGraphResponse.ok;
    report.checks.capability_compositions_fetched = compositionsResponse.ok;
    report.checks.graph_registry_fetched = graphRegistryResponse.ok;
    if (reputationResponse.ok) {
      const reputationValidation = validateEpisodeDerivedReputation(reputationResponse.body);
      report.episode_derived_reputation = reputationValidation;
      report.checks.episode_reputation_validated = reputationValidation.valid;
    }
    if (capabilityGraphResponse.ok) {
      const graphValidation = validateCapabilityGraphV2(capabilityGraphResponse.body);
      report.capability_graph_v2 = graphValidation;
      report.checks.capability_graph_v2_validated = graphValidation.valid;
      report.checks.pay_pix_graph_contract_validated = graphValidation.pay_pix_contract_found
        && graphValidation.missing_required_arrays.length === 0
        && graphValidation.has_failure_modes
        && graphValidation.has_estimated_cost
        && graphValidation.has_expected_latency;
      report.checks.graph_phase_report_detected = graphValidation.phase_report_detected;
    }
    if (compositionsResponse.ok) {
      const compositionsValidation = validateCapabilityCompositions(compositionsResponse.body);
      report.capability_compositions = compositionsValidation;
      report.checks.capability_compositions_validated = compositionsValidation.valid;
    }
    if (graphRegistryResponse.ok) {
      const graphRegistryValidation = validateGraphRegistry(graphRegistryResponse.body);
      report.graph_registry = graphRegistryValidation;
      report.checks.graph_registry_validated = graphRegistryValidation.valid;
    }
    if (plannerUrl) {
      const plannerBody = {
        goal: 'Pay a PIX recipient using USDT after quoting the amount',
        agent_wallet: agentWallet,
        amount_brl: amountBRL,
        pix_key: pixKey,
        constraints: { max_cost_usdt: '10', network: 'BSC', asset: 'USDT' },
      };
      const plannerResponse = await requestJSON(plannerUrl, {
        method: 'POST',
        body: JSON.stringify(plannerBody),
      });
      addStep('planner:create_plan', {
        request: { url: plannerUrl, method: 'POST', body: plannerBody },
        response: { status: plannerResponse.status, latency_ms: plannerResponse.latency_ms, body: compactBody(plannerResponse.body) },
      });
      report.checks.planner_api_called = plannerResponse.ok;
      if (plannerResponse.ok) {
        const plannerValidation = validatePlannerResponse(plannerResponse.body);
        report.planner_api = plannerValidation;
        report.checks.planner_api_validated = plannerValidation.valid;
      }
    }
    if (jwksResponse.ok && signatureResponse.ok) {
      const verification = verifyAgentCardSignature(cardResponse.text, signatureResponse.body, jwksResponse.body);
      report.agent_card_verification = verification;
      report.checks.card_hash_verified = verification.hashOk;
      report.checks.card_signature_verified = verification.signatureOk;
    }

    const skills = Array.isArray(card.skills) ? card.skills : [];
    report.discovered_skill_count = skills.length;
    report.discovered_skills = skills.map((skill) => skill.id || skill.name).filter(Boolean);
    report.checks.skills_detected = skills.length > 0;

    const skillMap = {
      methods: findSkill(skills, [
        (s) => s.id === 'list_supported_payment_methods',
        (s) => s.tags.includes('discovery') && s.tags.includes('payments'),
      ]),
      quote: findSkill(skills, [
        (s) => s.id === 'quote_required_usdt',
        (s) => s.id.includes('quote') && s.id.includes('usdt'),
      ]),
      payPix: findSkill(skills, [
        (s) => s.id === 'pay_pix_with_usdt',
        (s) => s.id.includes('pix') && s.id.includes('usdt'),
      ]),
      status: findSkill(skills, [
        (s) => s.id === 'get_payment_status',
        (s) => s.id.includes('status') && s.tags.includes('payments'),
      ]),
    };
    report.selected_skills = skillMap;
    report.checks.payment_skill_selected = Boolean(skillMap.payPix);

    if (!skillMap.methods || !skillMap.quote || !skillMap.payPix || !skillMap.status) {
      report.failed_at = 'select_required_skills';
      throw new Error(`Required skills were not discoverable: ${JSON.stringify(skillMap)}`);
    }

    await callA2A(a2aUrl, skillMap.methods, {}, 'optional');

    if (a2aTasksUrl) {
      const taskCreateResponse = await createA2ATask(a2aTasksUrl, skillMap.methods, {});
      report.checks.task_created = taskCreateResponse.ok;
      const taskId = taskCreateResponse.body?.id || taskCreateResponse.body?.task?.id || '';
      const taskStatusUrl = taskCreateResponse.body?.status_url || (taskId ? `${a2aTasksUrl.replace(/\/$/, '')}/${encodeURIComponent(taskId)}` : '');
      const taskEventsUrl = taskCreateResponse.body?.events_url || '';
      report.a2a_task_id = taskId || null;
      report.checks.task_events_url_detected = Boolean(taskEventsUrl);
      if (taskStatusUrl) {
        const taskStatusResponse = await requestJSON(taskStatusUrl);
        addStep('a2a-task:status', {
          request: { url: taskStatusUrl, method: 'GET' },
          response: {
            status: taskStatusResponse.status,
            latency_ms: taskStatusResponse.latency_ms,
            body: compactBody(taskStatusResponse.body),
          },
        });
        report.checks.task_status_checked = taskStatusResponse.ok;
      }
    }

    const x402ExecuteUrl = extractX402ExecuteUrl(card, cardUrl);
    if (x402ExecuteUrl) {
      const x402Challenge = await requestJSON(x402ExecuteUrl, {
        method: 'POST',
        body: JSON.stringify({
          agentWallet,
          payerWallet: agentWallet,
          paymentAsset: 'USDT',
          idempotencyKey: `agentqa-x402-${Date.now()}`,
          nonce: `agentqa-x402-${Date.now()}`,
          operation: 'extract_text',
          requestId: `agentqa-x402-req-${Date.now()}`,
          units: 1,
          input: { documentUrl: 'https://example.com/test.pdf' },
        }),
      });
      addStep('x402:capability_challenge', {
        request: { url: x402ExecuteUrl, method: 'POST' },
        response: {
          status: x402Challenge.status,
          latency_ms: x402Challenge.latency_ms,
          body: compactBody(x402Challenge.body),
        },
      });
      report.checks.x402_challenge_received = x402Challenge.status === 402 && Boolean(x402Challenge.body?.payment_requirements);
    }

    const quoteResponse = await callA2A(a2aUrl, skillMap.quote, {
      type: 'pix',
      amount_brl: amountBRL,
      agent_wallet: agentWallet,
    }, 'required');
    report.checks.quote_called = quoteResponse.ok;

    if (!quoteResponse.ok && !bearer && (quoteResponse.status === 401 || quoteResponse.status === 403)) {
      report.completed = false;
      report.outcome = 'discovery_ok_auth_required';
      report.failed_at = 'quote_required_auth';
      return;
    }

    if (!quoteResponse.ok) {
      report.failed_at = 'quote_required_usdt';
      throw new Error(`Quote failed with HTTP ${quoteResponse.status}`);
    }

    const createResponse = await callA2A(a2aUrl, skillMap.payPix, {
      amount_brl: amountBRL,
      pix_key: pixKey,
      beneficiary_name: 'ChainFX Agent QA',
      idempotency_key: `agentqa-${Date.now()}`,
      agent_wallet: agentWallet,
    }, 'required');

    if (!createResponse.ok && !bearer && (createResponse.status === 401 || createResponse.status === 403)) {
      report.completed = false;
      report.outcome = 'discovery_and_quote_ok_auth_required_for_payment';
      report.failed_at = 'payment_required_auth';
      return;
    }

    if (!createResponse.ok) {
      const code = createResponse.body?.error?.code || createResponse.body?.error?.error?.code || createResponse.body?.code;
      if (code === 'AGENT_POLICY_REQUIRED') {
        report.completed = false;
        report.outcome = 'policy_required_before_payment_intent';
        report.failed_at = 'agent_policy_required';
        report.checks.policy_required_detected = true;
        report.next_action = 'Call /agent/connect or configure an active policy for CHAINFX_AGENT_WALLET, then rerun this QA.';
        return;
      }
      report.failed_at = 'pay_pix_with_usdt';
      throw new Error(`Payment intent creation failed with HTTP ${createResponse.status}`);
    }

    const intentId = pickIntentId(createResponse.body);
    report.payment_intent_id = intentId || null;
    report.checks.intent_created = Boolean(intentId);

    if (!intentId) {
      report.failed_at = 'extract_payment_intent_id';
      throw new Error('Payment intent was created but no intent id was found in the response.');
    }

    const statusResponse = await callA2A(a2aUrl, skillMap.status, { intent_id: intentId }, 'required');
    report.checks.status_checked = statusResponse.ok;

    if (!statusResponse.ok) {
      report.failed_at = 'get_payment_status';
      throw new Error(`Status check failed with HTTP ${statusResponse.status}`);
    }

    report.completed = true;
    report.outcome = 'agent_card_to_a2a_payment_flow_completed';
  } catch (error) {
    report.completed = false;
    report.outcome = report.outcome || 'technical_failure';
    report.error = {
      message: error instanceof Error ? error.message : String(error),
    };
  } finally {
    report.finished_at = new Date().toISOString();
    const output = `${JSON.stringify(safeJson(report), null, 2)}\n`;
    if (reportPath) {
      await writeFile(reportPath, output, 'utf8');
    }
    process.stdout.write(output);
    if (!report.completed && bearer) {
      process.exitCode = 2;
    }
  }
}

await main();
