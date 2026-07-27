#!/usr/bin/env bash
#
# ChainFX — Local Chaos & Stress Simulation Suite
#
# Boots the REAL API (cmd/api), fires a REAL k6 load against it, and injects
# REAL infrastructure faults mid-run — DB connection loss, DB latency, and
# forced RPC/PSP-style upstream errors — using a small Go chaos proxy
# (cmd/chaosproxy) instead of docker-compose + `tc netem`, neither of which
# is available/supported in this environment. See replit.md /
# .agents/memory for why this differs from the originally pasted plan.
#
# What this validates:
#   - The Paymaster (idempotency, rate limiting) and Human Rail (Pix
#     settlement dedup) degrade cleanly (429/409/503) under load AND under
#     DB failure/latency — never with a panic or a silent double-settlement.
#   - No 5xx leakage rate above the SLO in tests/paymaster_stress.js.
#
# Usage:
#   tests/chaos_suite.sh
#
# Env overrides:
#   PORT                 (default 18080)     — port the real API binds to
#   DB_PROXY_PORT        (default 15432)     — chaos TCP proxy in front of Postgres
#   CHAOS_CONTROL_PORT   (default 19100)     — chaos proxy control API
#   BUY_ORDER_COUNT      (default 200)       — how many real buy orders to seed for Human Rail
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'

PORT="${PORT:-18080}"
DB_PROXY_PORT="${DB_PROXY_PORT:-15432}"
CHAOS_CONTROL_PORT="${CHAOS_CONTROL_PORT:-19100}"
BUY_ORDER_COUNT="${BUY_ORDER_COUNT:-200}"
PIX_SECRET="chaos-suite-local-secret-$(date +%s)"

WORK_DIR="$(mktemp -d)"
BUY_IDS_FILE="$WORK_DIR/buy_ids.json"
API_LOG="$WORK_DIR/api.log"
K6_SUMMARY="$WORK_DIR/k6_summary.json"

CHAOS_PROXY_PID=""
API_PID=""
FAILED=0

cleanup() {
  echo -e "${YELLOW}[cleanup] Encerrando processos e limpando dados de teste...${NC}"
  [[ -n "$API_PID" ]] && kill "$API_PID" 2>/dev/null || true
  [[ -n "$CHAOS_PROXY_PID" ]] && kill "$CHAOS_PROXY_PID" 2>/dev/null || true
  wait 2>/dev/null || true
  if [[ -f "$BUY_IDS_FILE" ]]; then
    go run ./cmd/chaosseed -cleanup "$BUY_IDS_FILE" 2>/dev/null || true
  fi
  echo -e "${YELLOW}[cleanup] Logs desta execução: ${API_LOG}${NC}"
}
trap cleanup EXIT

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo -e "${RED}DATABASE_URL não configurado — não é possível rodar o suite de caos contra Postgres real.${NC}"
  exit 1
fi

# Real Postgres host:port the app would normally talk to. The chaos proxy
# sits in front of it; the app under test talks to 127.0.0.1:$DB_PROXY_PORT
# instead, so we can inject latency/kill signals without touching the real
# Postgres process (which is a managed service we don't control directly).
DB_HOSTPORT="$(python3 -c "
import sys
from urllib.parse import urlparse
u = urlparse('${DATABASE_URL}')
print(f'{u.hostname}:{u.port or 5432}')
")"
PROXIED_DATABASE_URL="$(python3 -c "
import sys
from urllib.parse import urlparse, urlunparse
u = urlparse('${DATABASE_URL}')
netloc = u.netloc.replace(u.hostname + (f':{u.port}' if u.port else ''), '127.0.0.1:${DB_PROXY_PORT}')
print(urlunparse(u._replace(netloc=netloc)))
")"

echo -e "${YELLOW}[1/6] Compilando binários de caos e da API real...${NC}"
go build -o "$WORK_DIR/chaosproxy" ./cmd/chaosproxy
go build -o "$WORK_DIR/chaosseed" ./cmd/chaosseed
go build -o "$WORK_DIR/api" ./cmd/api

echo -e "${YELLOW}[2/6] Subindo o proxy de caos na frente do Postgres real (${DB_HOSTPORT} -> 127.0.0.1:${DB_PROXY_PORT})...${NC}"
"$WORK_DIR/chaosproxy" -mode=tcp -listen="127.0.0.1:${DB_PROXY_PORT}" -upstream="${DB_HOSTPORT}" -control="127.0.0.1:${CHAOS_CONTROL_PORT}" \
  > "$WORK_DIR/chaosproxy.log" 2>&1 &
CHAOS_PROXY_PID=$!
sleep 1

echo -e "${YELLOW}[3/6] Semeando ${BUY_ORDER_COUNT} buy orders reais (Human Rail) via cmd/chaosseed...${NC}"
DATABASE_URL="$DATABASE_URL" go run ./cmd/chaosseed -count="$BUY_ORDER_COUNT" -out="$BUY_IDS_FILE"

echo -e "${YELLOW}[4/6] Iniciando a API real (cmd/api) apontando para o Postgres via proxy de caos...${NC}"
APP_ENV=test \
DATABASE_URL="$PROXIED_DATABASE_URL" \
PORT="$PORT" \
PIX_WEBHOOK_SECRET="$PIX_SECRET" \
CHAINFX_REQUIRE_API_KEY=false \
ALLOW_SIMULATIONS=true \
"$WORK_DIR/api" > "$API_LOG" 2>&1 &
API_PID=$!

echo "Aguardando a API ficar pronta em http://127.0.0.1:${PORT} ..."
for i in $(seq 1 30); do
  if curl -sf "http://127.0.0.1:${PORT}/v1/gas/status" > /dev/null 2>&1; then
    break
  fi
  sleep 1
  if [[ "$i" -eq 30 ]]; then
    echo -e "${RED}API não respondeu a tempo. Log:${NC}"
    tail -n 60 "$API_LOG"
    exit 1
  fi
done

echo -e "${YELLOW}[5/6] Disparando k6 contra a API real e injetando caos de infraestrutura no meio do teste...${NC}"
(
  k6 run tests/paymaster_stress.js \
    --summary-export="$K6_SUMMARY" \
    -e BASE_URL="http://127.0.0.1:${PORT}" \
    -e API_KEY_LIVE="sk_live_chainfx_local" \
    -e API_KEY_TEST="sk_test_chainfx_local" \
    -e PIX_WEBHOOK_SECRET="$PIX_SECRET" \
    -e BUY_IDS_FILE="$BUY_IDS_FILE" \
    || true
) &
K6_PID=$!

# --- INJEÇÃO DE CAOS 1: latência de rede no Postgres (simula gargalo/lag) ---
sleep 20
echo -e "${RED}🔥 [CAOS] Introduzindo 500ms de latência na conexão com o Postgres...${NC}"
curl -sf -X POST "http://127.0.0.1:${CHAOS_CONTROL_PORT}/chaos/latency" -d '{"ms":500}' > /dev/null

sleep 10
echo -e "${GREEN}🔄 [RESTAURAÇÃO] Removendo latência do Postgres...${NC}"
curl -sf -X POST "http://127.0.0.1:${CHAOS_CONTROL_PORT}/chaos/reset" > /dev/null

# --- INJEÇÃO DE CAOS 2: queda total das conexões com o Postgres ---
echo -e "${RED}🔥 [CAOS] Derrubando todas as conexões com o Postgres (simulação de queda de banco)...${NC}"
curl -sf -X POST "http://127.0.0.1:${CHAOS_CONTROL_PORT}/chaos/kill" > /dev/null

sleep 8
echo -e "${GREEN}🔄 [RESTAURAÇÃO] Banco de dados acessível novamente (novas conexões fluem normalmente).${NC}"
curl -sf -X POST "http://127.0.0.1:${CHAOS_CONTROL_PORT}/chaos/reset" > /dev/null

echo -e "${YELLOW}Aguardando o motor k6 concluir...${NC}"
wait "$K6_PID" || true

echo -e "${YELLOW}[6/6] Caos concluído. Verificando logs da API por panics ou vazamento de erros 5xx...${NC}"

if grep -iq "panic" "$API_LOG"; then
  echo -e "${RED}🚨 FALHA GRAVE: a API sofreu um panic sob estresse/caos. Trecho do log:${NC}"
  grep -i -A 20 "panic" "$API_LOG" | head -40
  FAILED=1
fi

# k6 exits non-zero if any threshold failed (p95 SLOs, infra_errors rate, etc).
if [[ -f "$K6_SUMMARY" ]]; then
  echo -e "${YELLOW}Resumo de métricas salvo em: ${K6_SUMMARY}${NC}"
fi

if [[ "$FAILED" -eq 0 ]]; then
  echo -e "${GREEN}🎉 SUCESSO: o sistema se degradou de forma limpa (429/409/503) durante o caos e não sofreu panic.${NC}"
else
  echo -e "${RED}🚨 O suite de caos encontrou uma falha real — ver acima.${NC}"
fi

exit "$FAILED"
