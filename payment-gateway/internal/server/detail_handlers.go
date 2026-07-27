package server

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/webhooks"
)

// ─── Agent Detail ─────────────────────────────────────────────────────────────

func (s *Server) handleAppAgentDetail(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	detail, err := s.db.GetAgentDetailForProject(r.Context(), auth.ProjectID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if detail == nil {
		writeAPIError(w, r, http.StatusNotFound, "AGENT_NOT_FOUND", "Agent not found.")
		return
	}
	// Ensure policy fallback is written to DB on first read
	if detail.Policy == nil {
		policy, _ := s.db.UpsertAgentPolicy(r.Context(), detail.AgentID, database.DefaultAgentPolicyInput())
		detail.Policy = policy
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent":     detail,
		"feeLayer":  agentFeeLayer(),
		"policyDoc": agentPolicyDoc(),
	})
}

// ─── Purchase Detail ──────────────────────────────────────────────────────────

func (s *Server) handleAppPurchaseDetail(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	detail, err := s.db.GetPurchaseDetailForProject(r.Context(), auth.ProjectID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if detail == nil {
		writeAPIError(w, r, http.StatusNotFound, "PURCHASE_NOT_FOUND", "Purchase not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"purchase":     detail,
		"feeBreakdown": purchaseFeeBreakdown(detail),
		"lifecycle":    marketplacePurchaseLifecycle(),
	})
}

// ─── Execution Detail ─────────────────────────────────────────────────────────

func (s *Server) handleAppExecutionDetail(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	exec, err := s.db.GetExecutionDetailForProject(r.Context(), auth.ProjectID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if exec == nil {
		writeAPIError(w, r, http.StatusNotFound, "EXECUTION_NOT_FOUND", "Execution not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"execution": exec,
		"providerInfo": map[string]any{
			"slug":        exec.ProviderSlug,
			"name":        exec.ProviderName,
			"routeName":   exec.RouteName,
			"routingMode": exec.RoutingMode,
		},
		"latencyBuckets": latencyBuckets(exec.LatencyMS),
	})
}

// ─── Fee Layer ────────────────────────────────────────────────────────────────

func (s *Server) handleAppFees(w http.ResponseWriter, r *http.Request) {
	rate := s.workers.PriceWorker.GetPrice("BRL")
	writeJSON(w, http.StatusOK, map[string]any{
		"generatedAt": time.Now().UTC(),
		"usdtBrlRate": rate,
		"layers": []map[string]any{
			{
				"layer":       "buy",
				"description": "PIX → USDT (usuário compra stablecoin)",
				"flow":        "web / mobile",
				"model":       "spread sobre cotação",
				"spreadBps":   s.buySpreadBpsForFeesPage(),
				"exampleBRL":  500,
				"exampleUSDT": fmtRate(500, rate, s.buySpreadBpsForFeesPage()),
			},
			{
				"layer":       "sell",
				"description": "USDT → PIX (usuário vende stablecoin)",
				"flow":        "web / mobile",
				"model":       "spread sobre cotação",
				"spreadBps":   s.sellSpreadBps(100, rate),
				"exampleUSDT": 100,
				"exampleBRL":  fmtSell(100, rate, s.sellSpreadBps(100, rate)),
			},
			{
				"layer":       "marketplace_purchase",
				"description": "Agente compra acesso a capability via BSC USDT",
				"flow":        "agent / MCP",
				"model":       "take-rate por plano (bps varia por plano)",
				"note":        "take_rate_bps está no campo do plano; provedor recebe o restante",
			},
			{
				"layer":       "marketplace_execution",
				"description": "Agente executa capability após compra de acesso",
				"flow":        "agent / MCP",
				"model":       "quota por execução; sem taxa adicional da ChainFX",
			},
			{
				"layer":       "m2m_pix",
				"description": "Agente paga PIX fiat em nome de terceiro via USDT",
				"flow":        "agent / MCP (M2M)",
				"model":       "taxa fixa sobre gross USDT",
				"feeBps":      s.cfg.M2MPixFeeBps,
				"feePct":      fmt.Sprintf("%.0f%%", float64(s.cfg.M2MPixFeeBps)/100),
				"exampleBRL":  1000,
				"requiredUSDT": func() string {
					if rate <= 0 {
						return "cotação indisponível"
					}
					gross := 1000.0 / rate
					fee := gross * float64(s.cfg.M2MPixFeeBps) / 10000
					return fmt.Sprintf("%.6f USDT", gross+fee)
				}(),
			},
			{
				"layer":       "m2m_credit_card",
				"description": "Agente paga cartão de crédito fiat em nome de terceiro via USDT",
				"flow":        "agent / MCP (M2M)",
				"model":       "taxa fixa sobre gross USDT",
				"feeBps":      s.cfg.M2MCreditFeeBps,
				"feePct":      fmt.Sprintf("%.0f%%", float64(s.cfg.M2MCreditFeeBps)/100),
			},
			{
				"layer":       "access_v1",
				"description": "Acesso temporário via /v1/access (BSC USDT, sem cadastro)",
				"flow":        "developer / MCP",
				"model":       "preço fixo por plan de acesso; quota por request",
			},
		},
		"dailyCaps": map[string]any{
			"m2mMaxDailyOutflowBRL": s.cfg.M2MMaxDailyOutflowBRL,
			"description":           "Limite de saída PIX via M2M por dia corrido (UTC)",
		},
	})
}

// ─── MCP Test Connection ──────────────────────────────────────────────────────

func (s *Server) handleMCPTestConnection(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	ctx := r.Context()
	dbOK := s.db != nil
	if dbOK {
		if err := s.db.SQL.PingContext(ctx); err != nil {
			dbOK = false
		}
	}
	priceOK := s.workers != nil && s.workers.PriceWorker.GetPrice("BRL") > 0
	toolCount := 0
	// Count tools via reflection-free approach: we know the list length from the server
	toolCount = len(s.mcpToolNames())

	status := "ok"
	if !dbOK || !priceOK {
		status = "degraded"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        status,
		"timestamp":     time.Now().UTC(),
		"protocol":      "MCP/2024-11-05",
		"serverName":    "ChainFX",
		"serverVersion": "1.0.0",
		"capabilities": map[string]any{
			"tools":     map[string]any{"listChanged": false},
			"resources": map[string]any{"listChanged": false},
			"prompts":   map[string]any{"listChanged": false},
		},
		"health": map[string]any{
			"database":    dbOK,
			"priceWorker": priceOK,
			"usdtBrlRate": s.workers.PriceWorker.GetPrice("BRL"),
		},
		"toolCount": toolCount,
		"endpoints": map[string]string{
			"initialize":    "POST /mcp/initialize",
			"toolsList":     "POST /mcp/tools/list",
			"toolsCall":     "POST /mcp/tools/call",
			"resourcesList": "POST /mcp/resources/list",
			"resourcesRead": "POST /mcp/resources/read",
			"promptsList":   "POST /mcp/prompts/list",
		},
		"sampleRequest": map[string]any{
			"method": "POST",
			"path":   "/mcp/tools/list",
			"headers": map[string]string{
				"Authorization": "Bearer sk_live_...",
				"Content-Type":  "application/json",
			},
			"body": "{}",
		},
	})
}

// mcpToolNames returns the list of MCP tool names (so handleMCPTestConnection
// can count them without importing the mcp package).
func (s *Server) mcpToolNames() []string {
	return []string{
		"get_rates", "searchCapabilities", "listCapabilities", "getCapability",
		"getCapabilityContract", "purchaseCapability", "getPurchase", "executeCapability",
		"chooseRoute", "getUsage", "listAssets", "quote", "trade", "settlementStatus",
		"get_order_status", "market_analysis", "trade_recommendation", "price_prediction",
		"detect_anomalies", "summarize_transactions",
		"list_webhook_events", "create_webhook_subscription", "list_webhook_subscriptions", "trigger_test_webhook",
		"createPaymentIntent", "getPaymentIntent",
		// Phase 5+ agent self-service tools
		"listAgentGrants", "getAgentPolicy", "dryRunCapability", "listAgentPaymentIntents",
	}
}

// ─── Webhooks UI ─────────────────────────────────────────────────────────────

func (s *Server) handleAppWebhooksUI(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	subs, err := s.db.ListWebhookSubscriptions(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	stats, _ := s.db.WebhookDashboardStats(r.Context())

	if isJSONRequest(r) {
		writeJSON(w, http.StatusOK, map[string]any{
			"subscriptions":   subs,
			"stats":           stats,
			"availableEvents": webhookEventList(),
		})
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(renderWebhooksUI(subs, stats)))
}

func renderWebhooksUI(subs []*database.WebhookSubscription, stats *database.WebhookDeliveryStats) string {
	rows := strings.Builder{}
	for _, sub := range subs {
		activeLabel := "Inactive"
		activeBadge := `style="color:#b45309"`
		if sub.Active {
			activeLabel = "Active"
			activeBadge = `style="color:#0f8a5f"`
		}
		lastCode := "—"
		if sub.LastStatusCode != nil {
			lastCode = fmt.Sprintf("%d", *sub.LastStatusCode)
		}
		eventsStr := strings.Join(sub.Events, ", ")
		if len(eventsStr) > 60 {
			eventsStr = eventsStr[:57] + "…"
		}
		rows.WriteString(fmt.Sprintf(`
<tr>
  <td><code>%s</code></td>
  <td><a href="%s" target="_blank" rel="noopener">%s</a></td>
  <td>%s</td>
  <td><span %s>%s</span></td>
  <td>%d</td>
  <td>%s</td>
  <td>%s</td>
  <td class="actions-cell">
    <button onclick="toggleSub('%s',%v)" class="btn-sm">%s</button>
    <button onclick="deleteSub('%s')" class="btn-sm btn-danger">Delete</button>
    <a href="/api/webhooks/subscriptions/%s/logs" class="btn-sm" target="_blank">Logs</a>
  </td>
</tr>`,
			html.EscapeString(sub.ID),
			html.EscapeString(sub.TargetURL), html.EscapeString(sub.TargetURL),
			html.EscapeString(eventsStr),
			activeBadge, activeLabel,
			sub.FailureCount, lastCode,
			html.EscapeString(sub.CreatedAt.Format("2006-01-02 15:04")),
			html.EscapeString(sub.ID), !sub.Active, map[bool]string{true: "Pause", false: "Resume"}[sub.Active],
			html.EscapeString(sub.ID),
			html.EscapeString(sub.ID),
		))
	}

	deliveries, failures := 0, 0
	activeSubs := 0
	if stats != nil {
		deliveries = stats.Deliveries24h
		failures = stats.Failures24h
		activeSubs = stats.ActiveSubscriptions
	}

	eventOptions := strings.Builder{}
	for _, ev := range webhookEventList() {
		eventOptions.WriteString(fmt.Sprintf(`<option value="%s">%s</option>`, html.EscapeString(ev), html.EscapeString(ev)))
	}

	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>ChainFX · Webhook Subscriptions</title>
  <style>
    :root{--bg:#eef4fa;--panel:#fff;--ink:#102a43;--muted:#64748b;--line:#d8e5f2;--blue:#1266d6;--cyan:#12b7d8;--ok:#0f8a5f;--warn:#b45309;--danger:#c0392b}
    *{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.5 Inter,system-ui,sans-serif}
    header{padding:28px 28px 16px;background:linear-gradient(135deg,#fff,#e8f8ff);border-bottom:1px solid var(--line)}
    header h1{margin:0 0 6px;font-size:28px}header p{margin:0;color:var(--muted)}
    main{padding:24px 28px;max-width:1400px;margin:auto}
    .grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:14px;margin-bottom:24px}
    .card{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:16px}
    .card span{display:block;color:var(--muted);font-size:11px;text-transform:uppercase;margin-bottom:4px}
    .card strong{font-size:28px}
    h2{font-size:17px;margin:24px 0 10px}
    table{width:100%%;border-collapse:collapse;background:#fff;border:1px solid var(--line);border-radius:8px;overflow:hidden;font-size:13px}
    th,td{padding:9px 10px;border-bottom:1px solid var(--line);text-align:left;vertical-align:middle}
    th{background:#f8fbff;color:#475569;font-size:12px}td code{font-family:monospace;font-size:11px}
    .actions-cell{white-space:nowrap;display:flex;gap:6px;align-items:center}
    .btn-sm{padding:4px 10px;border:1px solid var(--line);border-radius:5px;background:#fff;cursor:pointer;font-size:12px;color:var(--blue);text-decoration:none}
    .btn-danger{color:var(--danger);border-color:#fcc}
    .panel{background:#fff;border:1px solid var(--line);border-radius:8px;padding:20px;margin-bottom:20px}
    label{display:block;font-size:12px;color:var(--muted);margin:10px 0 3px}
    input,select,textarea{width:100%%;padding:8px;border:1px solid var(--line);border-radius:5px;font:14px inherit}
    .row2{display:grid;grid-template-columns:1fr 1fr;gap:16px}
    button.primary{padding:10px 18px;background:linear-gradient(135deg,var(--blue),var(--cyan));color:#fff;border:none;border-radius:7px;cursor:pointer;font-weight:700;margin-top:12px}
    #msg{padding:10px;border-radius:6px;margin-bottom:12px;display:none}
    .ok-msg{background:#d1fae5;color:#065f46}.err-msg{background:#fee2e2;color:#991b1b}
    a{color:var(--blue)}
  </style>
</head>
<body>
<header>
  <h1>Webhook Subscriptions</h1>
  <p>Manage outbound automation callbacks to n8n, Zapier, Make.com or any HTTP endpoint.</p>
</header>
<main>
  <section class="grid">
    <div class="card"><span>Deliveries 24h</span><strong>%d</strong></div>
    <div class="card"><span>Failures 24h</span><strong>%d</strong></div>
    <div class="card"><span>Active subscriptions</span><strong>%d</strong></div>
  </section>

  <div id="msg"></div>

  <section class="panel">
    <h2 style="margin-top:0">Add Subscription</h2>
    <div class="row2">
      <div>
        <label>Target URL *</label>
        <input id="targetUrl" type="url" placeholder="https://hook.n8n.cloud/webhook/...">
      </div>
      <div>
        <label>Provider / Label</label>
        <input id="provider" placeholder="n8n, zapier, make, custom…">
      </div>
    </div>
    <label>Events (hold Ctrl/Cmd to select multiple)</label>
    <select id="events" multiple size="6">%s</select>
    <div class="row2" style="margin-top:10px">
      <div>
        <label>Signing secret (optional)</label>
        <input id="secret" type="password" placeholder="Blank = no signature">
      </div>
      <div>
        <label>Description</label>
        <input id="desc" placeholder="e.g. Production n8n workflow">
      </div>
    </div>
    <button class="primary" onclick="createSub()">Create subscription</button>
  </section>

  <h2>Existing subscriptions (%d)</h2>
  <table>
    <thead><tr><th>ID</th><th>URL</th><th>Events</th><th>Status</th><th>Failures</th><th>Last code</th><th>Created</th><th>Actions</th></tr></thead>
    <tbody>%s</tbody>
  </table>
</main>
<script>
function show(msg, ok) {
  const el = document.getElementById('msg');
  el.textContent = msg;
  el.className = ok ? 'ok-msg' : 'err-msg';
  el.style.display = 'block';
  setTimeout(() => { el.style.display = 'none'; }, 5000);
}
async function apiFetch(method, path, body) {
  const r = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined
  });
  return r.json();
}
async function createSub() {
  const url = document.getElementById('targetUrl').value.trim();
  if (!url) { show('Target URL is required', false); return; }
  const sel = document.getElementById('events');
  const events = Array.from(sel.selectedOptions).map(o => o.value);
  if (!events.length) { show('Select at least one event', false); return; }
  const data = await apiFetch('POST', '/api/webhooks/subscriptions', {
    url, provider: document.getElementById('provider').value.trim() || 'custom',
    secret: document.getElementById('secret').value,
    description: document.getElementById('desc').value.trim(),
    events,
  });
  if (data.error || data.code) { show(data.error || data.message || 'Error', false); return; }
  show('Subscription created — reloading…', true);
  setTimeout(() => location.reload(), 1200);
}
async function deleteSub(id) {
  if (!confirm('Delete subscription ' + id + '?')) return;
  await apiFetch('DELETE', '/api/webhooks/subscriptions/' + id);
  show('Deleted — reloading…', true);
  setTimeout(() => location.reload(), 1000);
}
async function toggleSub(id, activate) {
  await apiFetch('POST', '/api/webhooks/subscriptions/' + id + '/active', { active: activate });
  show((activate ? 'Activated' : 'Paused') + ' — reloading…', true);
  setTimeout(() => location.reload(), 1000);
}
</script>
</body>
</html>`, deliveries, failures, activeSubs, eventOptions.String(), len(subs), rows.String())
}

// webhookEventList returns the canonical event names that webhook subscribers
// can use. This must stay in sync with webhooks.AllEvents().
func webhookEventList() []string {
	// Delegate to the single source of truth so the UI, API and dispatcher
	// always expose the same set of subscribable events.
	all := webhooks.AllEvents()
	result := make([]string, 0, len(all)+1)
	result = append(result, "*") // wildcard: subscribe to all events
	result = append(result, all...)
	return result
}

// ─── helper renderers / data ──────────────────────────────────────────────────

func purchaseFeeBreakdown(d *database.PurchaseDetail) map[string]any {
	if d == nil {
		return nil
	}
	return map[string]any{
		"grossAmount":    d.GrossAmount,
		"chainfxAmount":  d.ChainFXAmount,
		"providerAmount": d.ProviderAmount,
		"takeRateBps":    d.TakeRateBps,
		"takeRatePct":    fmt.Sprintf("%.2f%%", float64(d.TakeRateBps)/100),
	}
}

func marketplacePurchaseLifecycle() []map[string]string {
	return []map[string]string{
		{"status": "created", "description": "Intent created, awaiting on-chain payment"},
		{"status": "pending_payment", "description": "Payment address active, watching BSC/Polygon"},
		{"status": "payment_detected", "description": "Transfer event detected on-chain"},
		{"status": "verifying", "description": "Confirming block depth and amount"},
		{"status": "paid", "description": "Payment confirmed; issuing access grant"},
		{"status": "granting_access", "description": "Writing API access grant to database"},
		{"status": "active", "description": "Grant active; agent can execute capability"},
		{"status": "exhausted", "description": "Quota fully consumed"},
		{"status": "expired", "description": "Grant TTL elapsed"},
		{"status": "payment_invalid", "description": "Amount mismatch or wrong token"},
		{"status": "grant_failed", "description": "Grant write failed; purchase pending review"},
		{"status": "manual_review", "description": "Flagged for operator review"},
	}
}

func agentFeeLayer() []map[string]any {
	return []map[string]any{
		{"layer": "marketplace_purchase", "model": "take-rate bps per plan", "paidBy": "agent buyer"},
		{"layer": "capability_execution", "model": "quota debit per unit", "paidBy": "agent (pre-paid)"},
		{"layer": "m2m_pix", "model": "configured M2M_PIX_FEE_BPS on gross USDT", "paidBy": "calling agent"},
		{"layer": "m2m_credit_card", "model": "configured M2M_CREDIT_FEE_BPS on gross USDT", "paidBy": "calling agent"},
	}
}

func agentPolicyDoc() map[string]any {
	return map[string]any{
		"fields": map[string]string{
			"dailyLimitUsdt":      "Maximum USDT spend per UTC day",
			"monthlyLimitUsdt":    "Maximum USDT spend per calendar month",
			"maxTransactionUsdt":  "Single-transaction cap",
			"allowedAssets":       "JSON array of permitted payment assets",
			"allowedCapabilities": "JSON array of permitted capability IDs (empty = all)",
			"allowedProviders":    "JSON array of permitted provider slugs (empty = all)",
			"permissions":         "JSON array of permission scopes",
			"requireRealProvider": "If true, mock_dev fallback is blocked",
			"mockFallback":        "If true, executions fall back to mock when provider fails",
			"status":              "active | paused | disabled",
		},
		"updateEndpoint": "PATCH /agent/{id}/policy",
	}
}

func latencyBuckets(ms int) map[string]any {
	bucket := "fast"
	switch {
	case ms > 2000:
		bucket = "slow"
	case ms > 800:
		bucket = "moderate"
	case ms > 300:
		bucket = "acceptable"
	}
	return map[string]any{
		"latencyMs": ms,
		"bucket":    bucket,
		"sla":       map[string]int{"p50": 100, "p95": 500, "p99": 2000},
	}
}

func (s *Server) buySpreadBpsForFeesPage() int {
	// ChainFX buy spread: look up from config if present, else return standard 200bps
	return 200
}

func fmtRate(brl, rate float64, spreadBps int) string {
	if rate <= 0 {
		return "—"
	}
	usdt := brl / rate * (1 - float64(spreadBps)/10000)
	return fmt.Sprintf("%.6f USDT", usdt)
}

func fmtSell(usdt, rate float64, spreadBps int) string {
	if rate <= 0 {
		return "—"
	}
	brl := usdt * rate * (1 - float64(spreadBps)/10000)
	return fmt.Sprintf("%.2f BRL", brl)
}

func isJSONRequest(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json") ||
		strings.HasSuffix(r.URL.Path, ".json") ||
		r.URL.Query().Get("format") == "json"
}
