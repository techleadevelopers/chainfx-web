package server

import (
        "context"
        "encoding/json"
        "fmt"
        "log/slog"
        "net/http"
        "strconv"
        "strings"
        "time"
)

// AdversarialEngine is the interface the chaos handlers depend on.
// Implemented by adversarial.Engine.
type AdversarialEngine interface {
        RunFullSuite(ctx context.Context) (scenarios, failures int, logOutput string, err error)
}

// ── POST /v1/admin/gas/chaos-run ─────────────────────────────────────────────

func (s *Server) handleTriggerChaos(w http.ResponseWriter, r *http.Request) {
        adminUser, _, ok := s.authorizeAdmin(w, r)
        if !ok {
                return
        }

        if s.adversarialEngine == nil {
                writeJSON(w, http.StatusServiceUnavailable, map[string]any{
                        "error": "Motor adversarial não inicializado",
                })
                return
        }

        triggeredBy := adminUser.Email
        if triggeredBy == "" {
                triggeredBy = r.Header.Get("X-Admin-User")
        }
        if triggeredBy == "" {
                triggeredBy = "unknown"
        }

        // Prevent concurrent runs for the same operator.
        s.chaosMu.Lock()
        if s.chaosRunning {
                s.chaosMu.Unlock()
                writeJSON(w, http.StatusConflict, map[string]any{
                        "error": "Uma execução adversarial já está em andamento. Aguarde a conclusão.",
                })
                return
        }
        s.chaosRunning = true
        s.chaosMu.Unlock()

        // Persist the run record immediately so the history table shows RUNNING.
        runCtx := context.Background()
        runID, err := s.db.CreateAdversarialRun(runCtx, triggeredBy)
        if err != nil {
                s.chaosMu.Lock()
                s.chaosRunning = false
                s.chaosMu.Unlock()
                slog.Error("[Chaos] falha ao criar registro de run", "error", err)
                writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Falha ao registrar execução"})
                return
        }

        startedAt := time.Now()

        // Dispatch asynchronously so the HTTP response returns in <100 ms.
        go func() {
                defer func() {
                        s.chaosMu.Lock()
                        s.chaosRunning = false
                        s.chaosMu.Unlock()
                }()

                ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
                defer cancel()

                slog.Info("[Chaos] suite adversarial iniciado", "triggered_by", triggeredBy, "run_id", runID)

                scenarios, failures, logs, runErr := s.adversarialEngine.RunFullSuite(ctx)

                status := "SUCCESS"
                if runErr != nil || failures > 0 {
                        status = "FAILED"
                }
                if runErr != nil {
                        logs += fmt.Sprintf("\n[ERRO INTERNO] %v", runErr)
                }

                if dbErr := s.db.CompleteAdversarialRun(ctx, runID, scenarios, failures, logs, status); dbErr != nil {
                        slog.Error("[Chaos] falha ao salvar resultado", "run_id", runID, "error", dbErr)
                }

                slog.Info("[Chaos] suite adversarial concluído",
                        "run_id", runID, "status", status,
                        "scenarios", scenarios, "failures", failures,
                        "elapsed", time.Since(startedAt).Round(time.Millisecond))
        }()

        writeJSON(w, http.StatusAccepted, map[string]any{
                "status":     "LAUNCHED",
                "run_id":     runID,
                "message":    "Suite adversarial e testes de concorrência iniciados em background.",
                "started_at": startedAt.Format(time.RFC3339),
        })
}

// ── GET /v1/admin/gas/chaos-history ──────────────────────────────────────────

func (s *Server) handleChaosHistory(w http.ResponseWriter, r *http.Request) {
        if _, _, ok := s.authorizeAdmin(w, r); !ok {
                return
        }

        limit := 20
        if q := r.URL.Query().Get("limit"); q != "" {
                if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 100 {
                        limit = n
                }
        }

        runs, err := s.db.ListAdversarialRuns(r.Context(), limit)
        if err != nil {
                slog.Error("[Chaos] erro ao listar runs", "error", err)
                writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Falha ao carregar histórico"})
                return
        }

        s.chaosMu.Lock()
        running := s.chaosRunning
        s.chaosMu.Unlock()

        writeJSON(w, http.StatusOK, map[string]any{
                "runs":           runs,
                "engine_running": running,
        })
}

// ── HTML template ─────────────────────────────────────────────────────────────

// chaosPageHTML generates the admin chaos dashboard page.
func chaosPageHTML(runsJSON string, engineRunning bool) string {
        runningBadge := `<span class="badge idle">Aguardando comando</span>`
        btnDisabled := ""
        if engineRunning {
                runningBadge = `<span class="badge running">EXECUTANDO ATAQUES...</span>`
                btnDisabled = "disabled"
        }

        return `<!DOCTYPE html>
<html lang="pt-BR">
<head>
  <meta charset="UTF-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>ChainFX — Motor Adversarial &amp; Auditoria</title>
  <style>
    *{box-sizing:border-box;margin:0;padding:0}
    body{font-family:'Segoe UI',system-ui,sans-serif;background:#0f1117;color:#e2e8f0;min-height:100vh}
    .sidebar{position:fixed;top:0;left:0;width:220px;height:100vh;background:#1a1d27;border-right:1px solid #2d3148;padding:24px 0;overflow-y:auto}
    .sidebar-logo{padding:0 20px 24px;font-size:18px;font-weight:700;color:#7c6af7;border-bottom:1px solid #2d3148;margin-bottom:16px}
    .menu-category{padding:12px 20px 4px;font-size:10px;font-weight:600;letter-spacing:.1em;text-transform:uppercase;color:#64748b}
    .menu-item{display:block;padding:9px 20px;color:#94a3b8;text-decoration:none;font-size:13.5px;border-left:3px solid transparent;transition:all .15s}
    .menu-item:hover{color:#e2e8f0;background:#ffffff08}
    .menu-item.active{color:#a78bfa;background:#7c6af71a;border-left-color:#7c6af7}
    .content{margin-left:220px;padding:32px 40px;max-width:1100px}
    h1{font-size:22px;font-weight:700;color:#f1f5f9;margin-bottom:6px}
    .subtitle{color:#64748b;font-size:14px;margin-bottom:32px}
    .card{background:#1a1d27;border:1px solid #2d3148;border-radius:12px;padding:24px;margin-bottom:24px}
    .card h2{font-size:16px;font-weight:600;color:#e2e8f0;margin-bottom:16px}
    .action-row{display:flex;align-items:center;gap:16px;flex-wrap:wrap}
    .btn-danger{background:linear-gradient(135deg,#dc2626,#b91c1c);color:#fff;border:none;padding:11px 22px;border-radius:8px;font-size:14px;font-weight:600;cursor:pointer;transition:opacity .15s}
    .btn-danger:hover:not(:disabled){opacity:.85}
    .btn-danger:disabled{opacity:.4;cursor:not-allowed}
    .badge{padding:5px 12px;border-radius:20px;font-size:12px;font-weight:600}
    .badge.idle{background:#1e293b;color:#64748b;border:1px solid #334155}
    .badge.running{background:#1d3a5f;color:#60a5fa;border:1px solid #2563eb;animation:pulse 1.5s infinite}
    .badge.success{background:#14532d;color:#4ade80;border:1px solid #16a34a}
    .badge.failed{background:#450a0a;color:#f87171;border:1px solid #dc2626}
    @keyframes pulse{0%,100%{opacity:1}50%{opacity:.6}}
    table{width:100%;border-collapse:collapse;font-size:13px}
    th{padding:10px 14px;text-align:left;color:#64748b;font-weight:500;border-bottom:1px solid #2d3148;white-space:nowrap}
    td{padding:11px 14px;border-bottom:1px solid #1e2235;vertical-align:top}
    tr:last-child td{border-bottom:none}
    .logs-cell{font-family:monospace;font-size:11px;white-space:pre-wrap;color:#94a3b8;max-width:380px;overflow:hidden;text-overflow:ellipsis}
    .empty{color:#475569;text-align:center;padding:32px;font-size:14px}
    .tag-pass{color:#4ade80}
    .tag-fail{color:#f87171}
    .scenarios{color:#a78bfa;font-weight:600}
    .failures-zero{color:#4ade80}
    .failures-nonzero{color:#f87171;font-weight:600}
  </style>
</head>
<body>
<nav class="sidebar">
  <div class="sidebar-logo">⛓ ChainFX</div>
  <div class="menu-category">Core Gateway</div>
  <a href="/developers/dashboard" class="menu-item">📈 Visão Geral</a>
  <a href="/api/admin/overview" class="menu-item">📋 Admin</a>
  <a href="/api/admin/gas-station" class="menu-item">⛽ Gas Station</a>
  <a href="/api/admin/sweeper" class="menu-item">🔄 Auto-Sweeper</a>
  <div class="menu-category">Segurança &amp; IA</div>
  <a href="/mcp/test" class="menu-item">🤖 MCP / Agentes</a>
  <a href="/metrics" class="menu-item">📊 Métricas</a>
  <a href="/admin/chaos" class="menu-item active">⚡ Caos &amp; Auditoria</a>
</nav>

<div class="content">
  <h1>⚡ Motor Adversarial &amp; Testes de Resiliência</h1>
  <p class="subtitle">Dispare simulações destrutivas concorrentes para validar travas de segurança, pisos on-chain e integridade das wallets.</p>

  <div class="card">
    <h2>Disparar Suite de Estresse Completo</h2>
    <div class="action-row">
      <button id="btn-chaos" class="btn-danger" onclick="triggerChaos()" ` + btnDisabled + `>
        ⚡ Executar Ataque Concorrente
      </button>
      <div id="status-badge">` + runningBadge + `</div>
    </div>
    <p style="margin-top:12px;color:#475569;font-size:12px">
      Executa 6 cenários: DB Connectivity · On-chain Floor · Concurrent Sig-Lock · SSRF Webhook · Rate Limiter Flood · Config Integrity
    </p>
  </div>

  <div class="card">
    <h2>Histórico de Auditorias</h2>
    <div id="history-container">` + historyTableHTML(runsJSON) + `</div>
  </div>
</div>

<script>
const API_HISTORY = '/v1/admin/gas/chaos-history';
const API_TRIGGER = '/v1/admin/gas/chaos-run';

function getBearerToken() {
  return localStorage.getItem('chainfx_admin_token') || '';
}

function authHeaders() {
  const t = getBearerToken();
  return t ? { 'Authorization': 'Bearer ' + t, 'Content-Type': 'application/json' }
           : { 'Content-Type': 'application/json' };
}

function triggerChaos() {
  const btn  = document.getElementById('btn-chaos');
  const badge = document.getElementById('status-badge');

  btn.disabled = true;
  badge.innerHTML = '<span class="badge running">EXECUTANDO ATAQUES...</span>';

  fetch(API_TRIGGER, { method: 'POST', headers: authHeaders() })
    .then(res => {
      if (res.status === 202) {
        scheduleRefresh(4000);
      } else if (res.status === 409) {
        alert('Uma execução já está em andamento. Aguarde.');
        btn.disabled = false;
        badge.innerHTML = '<span class="badge running">EXECUTANDO ATAQUES...</span>';
      } else {
        return res.json().then(j => { throw new Error(j.error || 'Erro desconhecido'); });
      }
    })
    .catch(err => {
      alert('Erro ao acionar motor de caos: ' + err.message);
      btn.disabled = false;
      badge.innerHTML = '<span class="badge idle">Aguardando comando</span>';
    });
}

function scheduleRefresh(ms) {
  setTimeout(() => {
    fetch(API_HISTORY, { headers: authHeaders() })
      .then(r => r.json())
      .then(data => {
        renderHistory(data.runs || []);
        const btn = document.getElementById('btn-chaos');
        const badge = document.getElementById('status-badge');
        if (data.engine_running) {
          btn.disabled = true;
          badge.innerHTML = '<span class="badge running">EXECUTANDO ATAQUES...</span>';
          scheduleRefresh(3000);
        } else {
          btn.disabled = false;
          badge.innerHTML = '<span class="badge idle">Aguardando comando</span>';
        }
      })
      .catch(() => scheduleRefresh(5000));
  }, ms);
}

function renderHistory(runs) {
  const el = document.getElementById('history-container');
  if (!runs || runs.length === 0) {
    el.innerHTML = '<p class="empty">Nenhuma execução registrada ainda.</p>';
    return;
  }
  let html = '<table><thead><tr>' +
    '<th>ID</th><th>Data/Hora</th><th>Operador</th>' +
    '<th>Cenários</th><th>Falhas</th><th>Status</th><th>Logs</th>' +
    '</tr></thead><tbody>';

  runs.forEach(r => {
    const dt = new Date(r.created_at).toLocaleString('pt-BR');
    const scen = '<span class="scenarios">' + r.scenarios_executed + '</span>';
    const fail = r.failures_detected === 0
      ? '<span class="failures-zero">0</span>'
      : '<span class="failures-nonzero">' + r.failures_detected + '</span>';
    let statusBadge = '';
    if (r.status === 'SUCCESS') statusBadge = '<span class="badge success">PASSOU</span>';
    else if (r.status === 'FAILED') statusBadge = '<span class="badge failed">FALHOU</span>';
    else statusBadge = '<span class="badge running">' + r.status + '</span>';

    const logsShort = (r.logs || '').substring(0, 200) + ((r.logs || '').length > 200 ? '...' : '');

    html += '<tr>' +
      '<td>#' + r.id + '</td>' +
      '<td>' + dt + '</td>' +
      '<td>' + escapeHtml(r.triggered_by) + '</td>' +
      '<td>' + scen + '</td>' +
      '<td>' + fail + '</td>' +
      '<td>' + statusBadge + '</td>' +
      '<td class="logs-cell">' + escapeHtml(logsShort) + '</td>' +
      '</tr>';
  });
  html += '</tbody></table>';
  el.innerHTML = html;
}

function escapeHtml(s) {
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

// Auto-refresh to pick up live history on page load.
scheduleRefresh(3000);
</script>
</body>
</html>`
}

func historyTableHTML(_ string) string {
        return `<p style="color:#64748b;font-size:13px">Carregando histórico...</p>`
}

// handleChaosDashboard renders the admin chaos page and is registered by the
// router. It wraps the raw HTML generator after reading run history from DB.
func (s *Server) handleAdminChaosPage(w http.ResponseWriter, r *http.Request) {
        if _, _, ok := s.authorizeAdmin(w, r); !ok {
                // Graceful redirect for browser navigation without token.
                if r.Header.Get("Accept") != "" && strings.Contains(r.Header.Get("Accept"), "text/html") {
                        http.Redirect(w, r, "/api/admin/login?next=/admin/chaos", http.StatusFound)
                }
                return
        }

        s.chaosMu.Lock()
        running := s.chaosRunning
        s.chaosMu.Unlock()

        runs, _ := s.db.ListAdversarialRuns(r.Context(), 20)
        runsBytes, _ := json.Marshal(runs)

        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        w.Header().Set("X-Frame-Options", "DENY")
        fmt.Fprint(w, chaosPageHTML(string(runsBytes), running))
}
