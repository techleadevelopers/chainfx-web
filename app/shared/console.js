const state = {
  workspace: document.body.dataset.workspace,
  apiBase: (
    window.CHAINFX_API_BASE_URL ||
    window.SWAPPED_API_BASE_URL ||
    import.meta.env.VITE_CHAINFX_API_BASE_URL ||
    import.meta.env.VITE_SWAPPED_API_BASE_URL ||
    localStorage.getItem("CHAINFX_API_BASE") ||
    localStorage.getItem("SWAPPED_API_BASE_URL") ||
    "https://api-production-bc748.up.railway.app"
  ).replace(/\/$/, ""),
  apiKey: sessionStorage.getItem("CHAINFX_CONSOLE_KEY") || localStorage.getItem("CHAINFX_CONSOLE_KEY") || "",
  summary: null,
  endpoint: null,
};

const $ = (id) => document.getElementById(id);
const money = (value) => (value === undefined || value === null || value === "" ? "0" : String(value));

document.addEventListener("DOMContentLoaded", () => {
  if ($("apiBase")) $("apiBase").value = state.apiBase;
  if ($("apiKey")) $("apiKey").value = state.apiKey;
  bindNavigation();
  bindAuth();
  bindDeveloperLogin();
  bindActions();
  if (state.workspace === "developer" && !state.apiKey) {
    showDeveloperLogin(true);
    return;
  }
  showDeveloperLogin(false);
  loadSummary();
});

function bindNavigation() {
  document.querySelectorAll("[data-section-target]").forEach((button) => {
    button.addEventListener("click", () => showSection(button.dataset.sectionTarget));
  });
}

function bindAuth() {
  const saveConnection = $("saveConnection");
  if (!saveConnection) return;
  saveConnection.addEventListener("click", () => {
    state.apiBase = $("apiBase")?.value.trim().replace(/\/$/, "") || state.apiBase;
    state.apiKey = $("apiKey")?.value.trim() || "";
    localStorage.setItem("CHAINFX_API_BASE", state.apiBase);
    sessionStorage.setItem("CHAINFX_CONSOLE_KEY", state.apiKey);
    loadSummary();
  });
}

function bindDeveloperLogin() {
  if (state.workspace !== "developer") return;
  const form = $("developerLoginForm");
  const logout = $("developerLogout");
  if (form) {
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      const submit = $("developerLoginSubmit");
      const login = $("developerLoginEmail")?.value.trim() || "";
      const password = $("developerLoginPassword")?.value.trim() || "";
      const error = $("developerLoginError");
      if (error) error.hidden = true;
      if (submit) {
        submit.disabled = true;
        submit.textContent = "Entrando";
      }
      try {
        state.apiBase = ($("apiBase")?.value || state.apiBase).trim().replace(/\/$/, "");
        const token = await authenticateDeveloper(login, password);
        state.apiKey = token;
        localStorage.setItem("CHAINFX_API_BASE", state.apiBase);
        sessionStorage.setItem("CHAINFX_CONSOLE_KEY", token);
        if ($("apiKey")) $("apiKey").value = token;
        showDeveloperLogin(false);
        await loadSummary();
      } catch (err) {
        showDeveloperLogin(true, err.message || "Login invalido.");
      } finally {
        if (submit) {
          submit.disabled = false;
          submit.textContent = "Entrar";
        }
      }
    });
  }
  if (logout) {
    logout.addEventListener("click", () => {
      state.apiKey = "";
      sessionStorage.removeItem("CHAINFX_CONSOLE_KEY");
      if ($("apiKey")) $("apiKey").value = "";
      showDeveloperLogin(true);
    });
  }
}

function showDeveloperLogin(visible, message = "") {
  if (state.workspace !== "developer") return;
  $("developerLoginView")?.classList.toggle("hidden", !visible);
  $("developerConsoleShell")?.classList.toggle("hidden", visible);
  const error = $("developerLoginError");
  if (error) {
    error.textContent = message;
    error.hidden = !message;
  }
  if (visible) {
    setTimeout(() => $("developerLoginEmail")?.focus(), 0);
  }
}

async function authenticateDeveloper(login, password) {
  const directKey = [login, password].find((value) => /^(sk_|ak_|cfx_)/i.test(value || ""));
  if (directKey) return directKey;
  const response = await fetch(`${state.apiBase}/api/admin/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ email: login, password }),
  });
  const text = await response.text();
  let payload = {};
  try {
    payload = text ? JSON.parse(text) : {};
  } catch {
    payload = { raw: text };
  }
  if (!response.ok || !payload.token) {
    throw new Error(payload.error || payload.message || "Login ou senha invalidos.");
  }
  return payload.token;
}

function bindActions() {
  document.querySelectorAll("[data-copy]").forEach((button) => {
    button.addEventListener("click", async () => {
      const target = $(button.dataset.copy);
      await navigator.clipboard?.writeText(target?.textContent || "");
      button.textContent = "Copied";
      setTimeout(() => (button.textContent = "Copy"), 1100);
    });
  });
  const createAgent = $("createAgentForm");
  if (createAgent) createAgent.addEventListener("submit", submitAgentForm);
  const projectForm = $("projectForm");
  if (projectForm) projectForm.addEventListener("submit", submitProjectForm);
  const apiKeyForm = $("apiKeyForm");
  if (apiKeyForm) apiKeyForm.addEventListener("submit", submitAPIKeyForm);
  const explorerSend = $("explorerSend");
  if (explorerSend) explorerSend.addEventListener("click", sendExplorerRequest);
  document.addEventListener("click", handleConsoleAction);
}

function showSection(name) {
  document.querySelectorAll(".section").forEach((section) => {
    section.classList.toggle("active", section.dataset.section === name);
  });
  document.querySelectorAll("[data-section-target]").forEach((button) => {
    button.classList.toggle("active", button.dataset.sectionTarget === name);
  });
}

async function loadSummary() {
  const status = $("connectionStatus");
  if (!status) return;
  status.textContent = "Loading";
  status.className = "badge warn";
  try {
    const path = state.workspace === "agent" ? "/app/agent/summary?limit=80" : "/app/developer/summary?limit=80";
    const data = await api(path);
    state.summary = data;
    status.textContent = data.sandbox ? "Sandbox" : "Connected";
    status.className = "badge ok";
  } catch (error) {
    if (state.workspace === "developer") {
      state.summary = null;
      state.apiKey = "";
      sessionStorage.removeItem("CHAINFX_CONSOLE_KEY");
      if ($("apiKey")) $("apiKey").value = "";
      status.textContent = "Offline";
      status.className = "badge warn";
      showDeveloperLogin(true, error.message || "Sessao expirada. Entre novamente.");
      return;
    }
    state.summary = fallbackSummary(state.workspace);
    status.textContent = "Demo data";
    status.className = "badge warn";
  }
  render();
}

async function api(path, options = {}) {
  const headers = { "Content-Type": "application/json", ...(options.headers || {}) };
  if (state.apiKey) headers.Authorization = `Bearer ${state.apiKey}`;
  const response = await fetch(`${state.apiBase}${path}`, { ...options, headers });
  const text = await response.text();
  let payload = {};
  try {
    payload = text ? JSON.parse(text) : {};
  } catch {
    payload = { raw: text };
  }
  if (!response.ok) throw new Error(payload.error || payload.message || `Request failed ${response.status}`);
  return payload;
}

function render() {
  if (state.workspace === "agent") renderAgent();
  if (state.workspace === "developer") renderDeveloper();
}

function renderAgent() {
  const data = state.summary;
  const metrics = data.metrics || {};
  renderMetrics([
    ["Connected Agents", metrics.connectedAgents || 0, "active identities"],
    ["Available Balance", metrics.availableBalance || "428.50 USDT", "wallet liquidity"],
    ["Spend This Month", metrics.spendThisMonth || "71.20 USDT", "gross agent spend"],
    ["Active Capabilities", metrics.activeCapabilities || 0, "catalog access"],
    ["Remaining Quota", `${metrics.remainingQuota || 0} units`, "available execution units"],
    ["Pending Settlements", metrics.pendingSettlements || 0, "provider payouts"],
  ]);
  renderChart(data.spendSeries || []);
  renderActivity(data);
  renderAlerts(data.alerts || []);
  renderAgents(data.agents || []);
  renderCapabilities(data.capabilities || []);
  renderPurchases(data.purchases || []);
  renderExecutions(data.executions || []);
  renderWallet(data.wallet || {});
  renderUsageCosts(metrics, data.executions || []);
  renderPolicies(data.policies || {});
  renderSettlements(data.settlements || []);
}

function renderDeveloper() {
  const data = state.summary;
  const metrics = data.metrics || {};
  renderMetrics([
    ["API Requests", metrics.apiRequests || 0, "last 24h"],
    ["MCP Tool Calls", metrics.mcpToolCalls || 0, "last 24h"],
    ["Active API Keys", metrics.activeAPIKeys || 0, "masked keys"],
    ["Webhook Success", metrics.webhookSuccess || "100.00%", "delivery rate"],
    ["Error Rate", metrics.errorRate || "0.00%", "requests + tools"],
    ["Current Spend", metrics.currentSpend || "142 USDT", "month to date"],
  ]);
  renderDeveloperEvents(data.dashboard || {});
  renderProjects(data.projects || []);
  renderAPIKeys(data.apiKeys || {});
  renderMCPConnections(data.mcpConnections || []);
  renderCapabilities(data.capabilities || []);
  renderProducts(data.products || []);
  renderExplorer(data.apiExplorer || []);
  renderProviderPublish(data.providerPublish || {});
  renderBilling(data.billing || {});
}

function renderMetrics(items) {
  $("metricGrid").innerHTML = items.map(([label, value, note]) => `
    <article class="metric"><span>${escapeHTML(label)}</span><strong>${escapeHTML(value)}</strong><small>${escapeHTML(note)}</small></article>
  `).join("");
}

function renderChart(series) {
  const max = Math.max(1, ...series.map((item) => Number(item.totalUsdt || 0)));
  $("spendChart").innerHTML = series.map((item) => {
    const height = Math.max(6, Math.round((Number(item.totalUsdt || 0) / max) * 100));
    return `<div class="bar" style="height:${height}%" data-label="${escapeHTML(String(item.date || "").slice(5))}" title="${escapeHTML(item.totalUsdt || "0")} USDT"></div>`;
  }).join("");
}

function renderActivity(data) {
  const executions = (data.executions || []).slice(0, 4).map((item) => `
    <li class="activity-item"><strong>Agent executed ${escapeHTML(item.capability || "capability")}</strong>
    Provider: ${escapeHTML(item.provider || "provider")} · Cost: 0.08 USDT · Status: ${badge(item.status)}</li>
  `);
  const purchases = (data.purchases || []).slice(0, 3).map((item) => `
    <li class="activity-item"><strong>Agent purchased ${escapeHTML(item.productId || "capability")}</strong>
    Plan: ${escapeHTML(item.planId || "plan")} · Paid: ${escapeHTML(item.grossAmount || "0")} ${escapeHTML(item.paymentAsset || "USDT")} · Status: ${badge(item.status)}</li>
  `);
  $("activityFeed").innerHTML = [...executions, ...purchases].join("") || `<li class="activity-item"><strong>No activity yet</strong> Connect an agent or run a sandbox execution.</li>`;
}

function renderAlerts(alerts) {
  $("alertList").innerHTML = alerts.map((item) => `
    <li class="alert-item"><strong>${escapeHTML(item.status || "notice")}</strong>${escapeHTML(item.message || "")}</li>
  `).join("");
}

function renderAgents(agents) {
  $("agentsTable").innerHTML = table(["Agent", "Status", "Wallet", "Capabilities", "Spend", "Last activity"], agents.map((agent) => [
    `<strong>${escapeHTML(agent.name || agent.agentId)}</strong><br><span class="muted mono">${escapeHTML(agent.agentId || "")}</span>`,
    badge(agent.status),
    shortWallet(agent.wallet),
    agent.capabilityCount || 0,
    `${money(agent.spendUsdt)} USDT`,
    relativeTime(agent.lastActivityAt || agent.createdAt),
  ]));
  $("agentDetail").innerHTML = agents[0] ? agentDetail(agents[0]) : "<p>No agent identity found.</p>";
}

function renderCapabilities(capabilities) {
  const cards = capabilities.map((capability) => `
    <article class="capability-card">
      <header><h3>${escapeHTML(capability.displayName || capability.id)}</h3>${badge(capability.status || "active")}</header>
      <p>${escapeHTML(capability.description || "Capability available for purchase and execution.")}</p>
      <div class="kv">
        <div><span>Provider options</span><strong>${(capability.providers || []).length || 1}</strong></div>
        <div><span>Starting at</span><strong>0.08 USDT</strong></div>
        <div><span>Average latency</span><strong>740 ms</strong></div>
        <div><span>Quality score</span><strong>98.2%</strong></div>
      </div>
      <button class="btn primary" type="button">View capability</button>
      <button class="btn" type="button">Test in Sandbox</button>
    </article>
  `).join("");
  document.querySelectorAll("[data-capability-grid]").forEach((el) => { el.innerHTML = cards; });
}

function renderPurchases(purchases) {
  $("purchasesTable").innerHTML = table(["Purchase", "Capability", "Agent", "Value", "Status", "Created"], purchases.map((item) => [
    `<span class="mono">${escapeHTML(item.id)}</span>`,
    escapeHTML(item.productId || item.planId),
    shortWallet(item.agentWallet),
    `${escapeHTML(item.grossAmount || "0")} ${escapeHTML(item.paymentAsset || "USDT")}`,
    badge(item.status),
    relativeTime(item.createdAt),
  ]));
  $("purchaseTimeline").innerHTML = ["Intent created", "Payment detected", "Confirmations reached", "Access grant issued", "Capability active"].map((step) => `<li>${step}</li>`).join("");
}

function renderExecutions(executions) {
  $("executionsTable").innerHTML = table(["Execution", "Capability", "Provider", "Cost", "Latency", "Status"], executions.map((item) => [
    `<span class="mono">${escapeHTML(item.id)}</span>`,
    escapeHTML(item.capability),
    escapeHTML(item.provider),
    "0.08 USDT",
    `${item.latencyMs || 0} ms`,
    badge(item.status),
  ]));
  $("executionDetail").innerHTML = `<pre>${escapeHTML(JSON.stringify({
    request: { idempotencyKey: "idem_...", requestId: "req_..." },
    providerRoute: "best_available",
    costBreakdown: { providerCost: "0.056 USDT", chainfxFee: "0.014 USDT", networkCost: "0.000 USDT", totalCharged: "0.070 USDT" },
    retries: 0,
    fallback: false,
    auditTrail: ["quota debited", "provider executed", "response stored"]
  }, null, 2))}</pre>`;
}

function renderWallet(wallet) {
  const assets = wallet.assets || [];
  $("walletPanel").innerHTML = `
    <div class="grid-3">
      ${["availableBalance", "lockedBalance", "pendingSettlement"].map((key) => `<div class="metric"><span>${labelize(key)}</span><strong>${escapeHTML(money(wallet[key]))}</strong></div>`).join("")}
    </div>
    <div class="table-wrap">${table(["Asset", "Network", "Balance", "Wallet"], assets.map((asset) => [asset.asset, asset.network, asset.balance, shortWallet(asset.address)]))}</div>
  `;
}

function renderUsageCosts(metrics, executions) {
  $("usageCostCards").innerHTML = ["spendThisMonth", "providerCost", "chainfxFees", "networkFees"].map((key) => `
    <article class="metric"><span>${labelize(key)}</span><strong>${escapeHTML(metrics[key] || "0 USDT")}</strong></article>
  `).join("");
  const byCapability = new Map();
  executions.forEach((item) => {
    const row = byCapability.get(item.capability) || { executions: 0, units: 0 };
    row.executions += 1;
    row.units += Number(item.unitsConsumed || 0);
    byCapability.set(item.capability, row);
  });
  $("usageCostTable").innerHTML = table(["Capability", "Executions", "Units", "Provider cost", "ChainFX fee", "Total"], [...byCapability.entries()].map(([cap, row]) => [
    cap, row.executions, row.units, "31 USDT", "7 USDT", "38 USDT"
  ]));
}

function renderPolicies(policies) {
  const maxTransaction = policies.maximumTransaction || `${policies.maxTransactionUsdt || "100"} USDT`;
  const dailyLimit = policies.dailyLimit || `${policies.dailyLimitUsdt || "500"} USDT`;
  const monthlyLimit = policies.monthlyLimit || `${policies.monthlyLimitUsdt || "5000"} USDT`;
  const allowedCapabilities = normalizePolicyList(policies.allowedCapabilities);
  const allowedProviders = normalizePolicyList(policies.allowedProviders);
  $("policiesPanel").innerHTML = `
    <div class="kv">
      <div><span>Maximum transaction</span><strong>${escapeHTML(maxTransaction)}</strong></div>
      <div><span>Daily limit</span><strong>${escapeHTML(dailyLimit)}</strong></div>
      <div><span>Monthly limit</span><strong>${escapeHTML(monthlyLimit)}</strong></div>
      <div><span>Require real provider</span><strong>${policies.requireRealProvider ? "enabled" : "disabled"}</strong></div>
      <div><span>Mock fallback</span><strong>${policies.mockFallback ? "enabled" : "disabled"}</strong></div>
      <div><span>Status</span><strong>${escapeHTML(policies.status || "active")}</strong></div>
    </div>
    <h3>Allowed capabilities</h3>
    <ul class="check-list">${allowedCapabilities.map((item) => `<li>Allowed ${escapeHTML(item)}</li>`).join("") || "<li>All active capabilities allowed</li>"}</ul>
    <h3 style="margin-top:14px">Allowed providers</h3>
    <ul class="check-list">${allowedProviders.map((item) => `<li>Allowed ${escapeHTML(item)}</li>`).join("") || "<li>All active providers allowed</li>"}</ul>
  `;
}

function renderSettlements(settlements) {
  $("settlementsTable").innerHTML = table(["Settlement", "Provider", "Purchase", "Gross", "ChainFX", "Provider", "Status"], settlements.map((item) => [
    item.id, item.providerId, item.purchaseId, item.grossAmount, item.chainfxAmount, item.providerAmount, badge(item.status)
  ]));
}

function renderDeveloperEvents(dashboard) {
  const logs = [...(dashboard.apiLogs || []).slice(0, 5), ...(dashboard.mcpLogs || []).slice(0, 4)];
  $("developerEvents").innerHTML = logs.map((item) => `
    <li class="activity-item"><strong>${escapeHTML(item.method || "MCP")} ${escapeHTML(item.path || item.toolName || "")} — ${escapeHTML(item.statusCode || item.status || "ok")}</strong>
    Request ID: <span class="mono">${escapeHTML(item.requestId || "")}</span> · ${escapeHTML(item.durationMs || 0)} ms</li>
  `).join("") || `<li class="activity-item"><strong>No events yet</strong> Send a request from the API Explorer.</li>`;
}

function renderProjects(projects) {
  const select = $("apiKeyProject");
  if (select) {
    select.innerHTML = projects.map((item) => `<option value="${escapeHTML(item.id)}">${escapeHTML(item.name)} (${escapeHTML(item.environment)})</option>`).join("");
  }
  $("projectsTable").innerHTML = table(["Project", "Environment", "API Keys", "Agents", "Limit", "Status"], projects.map((item) => [
    `<strong>${escapeHTML(item.name)}</strong><br><span class="muted mono">${escapeHTML(item.id || "")}</span>`,
    item.environment,
    item.apiKeyCount ?? item.apiKeys ?? 0,
    item.agentCount ?? item.agents ?? 0,
    `${escapeHTML(item.spendingLimitUsdt || item.spendingLimit || "0")} USDT`,
    badge(item.status)
  ]));
}

function renderAPIKeys(keys) {
  const items = Array.isArray(keys?.items) ? keys.items : [];
  if (items.length > 0) {
    $("apiKeysTable").innerHTML = table(["Name", "Public key", "Environment", "Scopes", "Requests", "Status", "Actions"], items.map((item) => [
      `<strong>${escapeHTML(item.name)}</strong><br><span class="muted mono">${escapeHTML(item.id)}</span>`,
      `<span class="mono">${escapeHTML(item.publicKey)}</span><br><span class="muted">${escapeHTML(item.maskedSecret || "sk_************************")}</span>`,
      item.environment,
      formatScopes(item.scopes),
      item.requests || 0,
      badge(item.status),
      `<button class="btn" data-action="rotate-key" data-key-id="${escapeHTML(item.id)}" type="button">Rotate</button> <button class="btn" data-action="disable-key" data-key-id="${escapeHTML(item.id)}" type="button">Disable</button> <button class="btn" data-action="revoke-key" data-key-id="${escapeHTML(item.id)}" type="button">Revoke</button>`,
    ]));
    return;
  }
  const legacy = keys?.legacyEnv || keys || {};
  const rows = [
    ["Live public", legacy.livePublic || "pk_live_...", "Production", "Env"],
    ["Live secret", legacy.liveSecret || "sk_live_...", "Production", "Env"],
    ["Test public", legacy.testPublic || "pk_test_...", "Sandbox", "Env"],
    ["Test secret", legacy.testSecret || "sk_test_...", "Sandbox", "Env"],
  ];
  $("apiKeysTable").innerHTML = table(["Name", "Masked key", "Environment", "Status"], rows.map((row) => [row[0], `<span class="mono">${escapeHTML(row[1])}</span>`, row[2], badge(row[3])]));
}

function renderMCPConnections(connections) {
  $("mcpConnections").innerHTML = connections.map((item) => `
    <article class="panel flat">
      <div class="panel-head"><h3>${escapeHTML(item.client)}</h3>${badge(item.status)}</div>
      <div class="kv">
        <div><span>Protocol</span><strong>${escapeHTML(item.protocolVersion)}</strong></div>
        <div><span>Tools</span><strong>${item.tools}</strong></div>
        <div><span>Resources</span><strong>${item.resources}</strong></div>
        <div><span>Latency</span><strong>${item.latencyMs} ms</strong></div>
      </div>
      <pre id="mcpConfig">${escapeHTML(JSON.stringify(item.config, null, 2))}</pre>
    </article>
  `).join("");
}

function renderProducts(products) {
  $("productsTable").innerHTML = table(["Product", "Capability", "Provider", "Plans", "Status"], products.map((item) => [
    item.name, item.capability || item.capabilityId, item.provider?.name || item.providerId, (item.plans || []).length, badge(item.status)
  ]));
}

function renderExplorer(endpoints) {
  const list = $("endpointList");
  state.endpoint = state.endpoint || endpoints[0];
  list.innerHTML = endpoints.map((endpoint, index) => `
    <button type="button" class="${endpoint === state.endpoint ? "active" : ""}" data-endpoint-index="${index}">
      <strong>${escapeHTML(endpoint.group)}</strong><br><span>${escapeHTML(endpoint.method)} ${escapeHTML(endpoint.path)}</span>
    </button>
  `).join("");
  list.querySelectorAll("button").forEach((button) => {
    button.addEventListener("click", () => {
      state.endpoint = endpoints[Number(button.dataset.endpointIndex)];
      renderExplorer(endpoints);
    });
  });
  if (state.endpoint) {
    $("explorerMethod").textContent = state.endpoint.method;
    $("explorerPath").textContent = state.endpoint.path;
    $("explorerBody").value = state.endpoint.body || "{}";
  }
}

async function sendExplorerRequest() {
  const start = performance.now();
  const endpoint = state.endpoint;
  if (!endpoint) return;
  $("explorerResult").textContent = "Sending...";
  try {
    const options = endpoint.method === "GET" ? {} : { method: endpoint.method, body: $("explorerBody").value };
    const headers = { "Content-Type": "application/json", "X-Idempotency-Key": `explorer-${Date.now()}` };
    if (state.apiKey) headers.Authorization = `Bearer ${state.apiKey}`;
    const response = await fetch(`${state.apiBase}${endpoint.path}`, { method: endpoint.method, headers, body: options.body });
    const text = await response.text();
    $("explorerResult").textContent = JSON.stringify({ status: response.status, durationMs: Math.round(performance.now() - start), body: tryParse(text) }, null, 2);
  } catch (error) {
    $("explorerResult").textContent = JSON.stringify({ error: error.message }, null, 2);
  }
}

function renderProviderPublish(spec) {
  $("providerPublish").innerHTML = `
    <div class="grid-3">${(spec.steps || []).map((step) => `<div class="metric"><span>Step</span><strong>${escapeHTML(step)}</strong></div>`).join("")}</div>
  `;
}

function renderBilling(billing) {
  $("billingPanel").innerHTML = table(["Metric", "Value"], Object.entries(billing).map(([key, value]) => [labelize(key), value]));
}

async function submitAgentForm(event) {
  event.preventDefault();
  const form = new FormData(event.currentTarget);
  const payload = {
    name: form.get("name"),
    description: form.get("description"),
    environment: String(form.get("environment") || "").toLowerCase(),
    agentType: String(form.get("type") || "").toLowerCase(),
    walletMode: form.get("walletMode"),
    agentWallet: form.get("wallet"),
    dailyLimitUsdt: form.get("dailyLimitUsdt"),
    monthlyLimitUsdt: form.get("monthlyLimitUsdt"),
    maxTransactionUsdt: form.get("maxTransactionUsdt"),
    allowedAssets: splitCSV(form.get("allowedAssets")),
    allowedCapabilities: splitCSV(form.get("allowedCapabilities")),
    allowedProviders: splitCSV(form.get("allowedProviders")),
    permissions: splitCSV(form.get("permissions")),
    requireRealProvider: form.get("requireRealProvider") === "true",
    mockFallback: form.get("mockFallback") === "true",
  };
  try {
    const data = await api("/agent/connect", { method: "POST", body: JSON.stringify(payload), headers: { "X-Idempotency-Key": `agent-${Date.now()}` } });
    $("agentSecretResult").textContent = JSON.stringify({ agentId: data.agentId, clientId: data.agentId, agentSecret: data.apiKey, mcpEndpoint: `${state.apiBase}/mcp` }, null, 2);
    loadSummary();
  } catch (error) {
    $("agentSecretResult").textContent = JSON.stringify({ error: error.message, note: "Secret appears once when backend creates the agent." }, null, 2);
  }
}

async function submitProjectForm(event) {
  event.preventDefault();
  const form = new FormData(event.currentTarget);
  const payload = {
    name: form.get("name"),
    description: form.get("description"),
    environment: form.get("environment"),
    status: form.get("status"),
    spendingLimitUsdt: form.get("spendingLimitUsdt"),
  };
  try {
    await api("/developer/projects", { method: "POST", body: JSON.stringify(payload), headers: { "X-Idempotency-Key": `project-${Date.now()}` } });
    event.currentTarget.reset();
    loadSummary();
  } catch (error) {
    alert(error.message);
  }
}

async function submitAPIKeyForm(event) {
  event.preventDefault();
  const form = new FormData(event.currentTarget);
  const projectId = form.get("projectId");
  if (!projectId) {
    alert("Create a project first.");
    return;
  }
  const payload = {
    name: form.get("name"),
    environment: form.get("environment"),
    expirationDays: Number(form.get("expirationDays") || 0),
    ipRestrictions: splitCSV(form.get("ipRestrictions")),
    scopes: splitCSV(form.get("scopes")),
    rateLimitPerMinute: Number(form.get("rateLimitPerMinute") || 600),
    spendingLimitUsdt: form.get("spendingLimitUsdt"),
  };
  try {
    const created = await api(`/developer/projects/${encodeURIComponent(projectId)}/api-keys`, { method: "POST", body: JSON.stringify(payload), headers: { "X-Idempotency-Key": `apikey-${Date.now()}` } });
    $("apiKeySecretResult").textContent = JSON.stringify({
      publicKey: created.key?.publicKey,
      secretKey: created.secretKey,
      warning: created.warning,
    }, null, 2);
    event.currentTarget.reset();
    loadSummary();
  } catch (error) {
    $("apiKeySecretResult").textContent = JSON.stringify({ error: error.message }, null, 2);
  }
}

async function handleConsoleAction(event) {
  const button = event.target.closest("[data-action]");
  if (!button) return;
  const keyId = button.dataset.keyId;
  if (!keyId) return;
  try {
    if (button.dataset.action === "rotate-key") {
      const rotated = await api(`/developer/api-keys/${encodeURIComponent(keyId)}/rotate`, { method: "POST", body: "{}" });
      $("apiKeySecretResult").textContent = JSON.stringify({
        publicKey: rotated.key?.publicKey,
        secretKey: rotated.secretKey,
        warning: rotated.warning,
      }, null, 2);
    }
    if (button.dataset.action === "disable-key") {
      await api(`/developer/api-keys/${encodeURIComponent(keyId)}/disabled`, { method: "POST", body: "{}" });
    }
    if (button.dataset.action === "revoke-key") {
      await api(`/developer/api-keys/${encodeURIComponent(keyId)}/revoked`, { method: "POST", body: "{}" });
    }
    loadSummary();
  } catch (error) {
    alert(error.message);
  }
}

function table(headers, rows) {
  return `<table><thead><tr>${headers.map((h) => `<th>${escapeHTML(h)}</th>`).join("")}</tr></thead><tbody>${rows.map((row) => `<tr>${row.map((cell) => `<td>${cell}</td>`).join("")}</tr>`).join("") || `<tr><td colspan="${headers.length}">No records yet.</td></tr>`}</tbody></table>`;
}

function badge(value) {
  const text = String(value || "unknown");
  const lower = text.toLowerCase();
  const cls = lower.includes("fail") || lower.includes("invalid") || lower.includes("blocked") ? "fail" : lower.includes("pending") || lower.includes("warn") || lower.includes("sandbox") ? "warn" : "ok";
  return `<span class="badge ${cls}">${escapeHTML(text)}</span>`;
}

function agentDetail(agent) {
  return `
    <div class="panel-head"><h2>${escapeHTML(agent.name)}</h2>${badge(agent.status)}</div>
    <p class="mono">${escapeHTML(agent.agentId)}</p>
    <div class="kv">
      <div><span>Balance</span><strong>428.50 USDT</strong></div>
      <div><span>Spend today</span><strong>4.20 USDT</strong></div>
      <div><span>Monthly spend</span><strong>${escapeHTML(agent.spendUsdt || "0")} USDT</strong></div>
      <div><span>Remaining limit</span><strong>${escapeHTML(agent.monthlyLimitUsdt || "5000")} USDT</strong></div>
      <div><span>Provider most used</span><strong>ChainFX OCR</strong></div>
      <div><span>Average fee</span><strong>0.014 USDT</strong></div>
    </div>
  `;
}

function fallbackSummary(workspace) {
  const now = new Date().toISOString();
  if (workspace === "developer") {
    return {
      metrics: {},
      dashboard: {},
      projects: [],
      apiKeys: { items: [] },
      mcpConnections: [],
      apiExplorer: fallbackDeveloperEndpoints(),
      capabilities: [],
      products: [],
      providerPublish: {},
      billing: {},
    };
  }
  return {
    metrics: { connectedAgents: 3, availableBalance: "428.50 USDT", spendThisMonth: "71.20 USDT", activeCapabilities: 6, remainingQuota: 84620, pendingSettlements: 2, providerCost: "57 USDT", chainfxFees: "14 USDT", networkFees: "0.000 USDT" },
    agents: [{ agentId: "agt_9fk31", name: "Treasury Agent", status: "active", wallet: "0x830000000000000000000000000000000000019a", capabilityCount: 4, spendUsdt: "42", quotaRemaining: 40000, createdAt: now, monthlyLimitUsdt: "5000" }],
    capabilities: fallbackCapabilities(),
    purchases: [{ id: "pur_82A", productId: "aml_screening", planId: "AML Enterprise", agentWallet: "0x830000000000000000000000000000000000019a", grossAmount: "600", paymentAsset: "USDT", status: "active", createdAt: now }],
    executions: [{ id: "exe_102", capability: "document_ocr", provider: "chainfx-ocr-http", latencyMs: 682, status: "completed", unitsConsumed: 1, createdAt: now }],
    spendSeries: Array.from({ length: 14 }, (_, i) => ({ date: `07-${String(i + 1).padStart(2, "0")}`, totalUsdt: String(3 + i), chainfxFeeUsdt: "0.4", providerUsdt: "2.6" })),
    settlements: [{ id: "stl_8x2", providerId: "provider_chainfx_demo", purchaseId: "pur_82A", grossAmount: "600", chainfxAmount: "120", providerAmount: "480", status: "pending" }],
    wallet: { availableBalance: "428.50 USDT", lockedBalance: "18.00 USDT", pendingSettlement: "2", assets: [{ asset: "USDT", network: "BSC", balance: "428.50", address: "0x830000000000000000000000000000000000019a" }] },
    policies: { maximumTransaction: "100 USDT", dailyLimit: "500 USDT", monthlyLimit: "5,000 USDT", mockFallback: false, allowedCapabilities: [{ id: "document_ocr", allowed: true }, { id: "stablecoin_trade", allowed: false }] },
    alerts: [{ status: "quota.low", message: "Agent support-bot reached 80% of monthly quota" }],
  };
}

function fallbackCapabilities() {
  return [
    { id: "document_ocr", displayName: "Document OCR", description: "Extract text and structured fields from documents.", category: "ai", status: "active", providers: ["chainfx-ocr-http", "google-vision", "aws-textract"] },
    { id: "aml_screening", displayName: "AML Screening", description: "Compliance screening for wallets and payments.", category: "compliance", status: "active", providers: ["provider-a"] },
    { id: "llm_chat", displayName: "LLM Chat", description: "Provider-routed chat and text generation.", category: "ai", status: "active", providers: ["openai"] },
  ];
}

function fallbackDeveloperEndpoints() {
  return [
    { group: "Discovery", method: "GET", path: "/.well-known/agent-card.json", body: "{}" },
    { group: "Trust", method: "GET", path: "/.well-known/jwks.json", body: "{}" },
    { group: "Trust", method: "GET", path: "/.well-known/agent-card.signature", body: "{}" },
    { group: "Planning", method: "GET", path: "/.well-known/agent-policy.json", body: "{}" },
    { group: "Planning", method: "GET", path: "/.well-known/capability-graph.json", body: "{}" },
    { group: "A2A", method: "POST", path: "/a2a", body: "{ \"skill\": \"list_supported_payment_methods\", \"arguments\": {} }" },
    { group: "A2A Tasks", method: "POST", path: "/a2a/tasks", body: "{ \"skill\": \"list_supported_payment_methods\", \"arguments\": {} }" },
    { group: "Agent Pay", method: "POST", path: "/agent/v1/pay", body: "{ \"type\": \"pix\", \"amount_brl\": \"10.00\", \"pix_key\": \"+5511999999999\", \"beneficiary_name\": \"ChainFX Developer Test\", \"idempotency_key\": \"devtest_001\", \"agent_wallet\": \"0x830000000000000000000000000000000000019a\" }" },
    { group: "Capabilities", method: "GET", path: "/marketplace/capabilities", body: "{}" },
    { group: "MCP", method: "POST", path: "/mcp/tools/call", body: "{ \"name\": \"listCapabilities\", \"arguments\": {} }" },
    { group: "x402", method: "GET", path: "/.well-known/x402.json", body: "{}" },
    { group: "x402", method: "POST", path: "/x402/capabilities/document_ocr/execute", body: "{ \"agentWallet\": \"0x830000000000000000000000000000000000019a\", \"payerWallet\": \"0x830000000000000000000000000000000000019a\", \"paymentAsset\": \"USDT\", \"operation\": \"extract_text\", \"requestId\": \"x402_dev_001\", \"idempotencyKey\": \"x402_dev_001\", \"units\": 1, \"input\": { \"documentUrl\": \"https://example.com/invoice.pdf\" } }" },
    { group: "Registry", method: "GET", path: "/agent/v1/registries", body: "{}" },
    { group: "Registry", method: "GET", path: "/agent/v1/registry-records/agntcy-oasf", body: "{}" },
    { group: "Observability", method: "GET", path: "/agent/v1/reputation", body: "{}" },
    { group: "Observability", method: "GET", path: "/agent/v1/sla", body: "{}" }
  ];
}

function shortWallet(value) {
  if (!value) return "";
  const text = String(value);
  return text.length > 14 ? `<span class="mono">${text.slice(0, 6)}…${text.slice(-4)}</span>` : `<span class="mono">${escapeHTML(text)}</span>`;
}

function relativeTime(value) {
  if (!value) return "never";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  const seconds = Math.max(1, Math.round((Date.now() - date.getTime()) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.round(seconds / 60)} min ago`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h ago`;
  return `${Math.round(seconds / 86400)}d ago`;
}

function labelize(key) {
  return String(key).replace(/([A-Z])/g, " $1").replace(/^./, (c) => c.toUpperCase());
}

function tryParse(text) {
  try { return JSON.parse(text); } catch { return text; }
}

function splitCSV(value) {
  return String(value || "").split(",").map((item) => item.trim()).filter(Boolean);
}

function formatScopes(value) {
  let scopes = value;
  if (typeof value === "string") {
    try { scopes = JSON.parse(value); } catch { scopes = splitCSV(value); }
  }
  if (!Array.isArray(scopes)) return "";
  return scopes.slice(0, 4).map((scope) => `<span class="badge">${escapeHTML(scope)}</span>`).join(" ");
}

function normalizePolicyList(value) {
  if (!value) return [];
  if (Array.isArray(value)) {
    return value.map((item) => typeof item === "string" ? item : item.id).filter(Boolean);
  }
  if (typeof value === "string") {
    try { return normalizePolicyList(JSON.parse(value)); } catch { return splitCSV(value); }
  }
  return [];
}

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[char]));
}
