# ChainFX Payment Gateway - Arquitetura Tecnica

## Indice

1. [Visao Geral](#visao-geral)
2. [Requisitos](#requisitos)
3. [Diagrama de Sequencia](#diagrama-de-sequencia)
4. [Componentes](#componentes)
5. [Fluxos Principais](#fluxos-principais)
6. [Status de Ordem](#status-de-ordem)
7. [Endpoints](#endpoints)
8. [Webhooks](#webhooks)
9. [Auditoria e LGPD](#auditoria-e-lgpd)
10. [Idempotencia](#idempotencia)
11. [Configuracao](#configuracao)
12. [Deploy](#deploy)
13. [Benchmark E2E](#benchmark-e2e)
14. [Troubleshooting](#troubleshooting)
15. [Monitoramento](#monitoramento)
16. [Rollback Operacional](#rollback-operacional)

## Visao Geral

O ChainFX Payment Gateway e um backend Go para orquestracao de pagamento fiat e entrega de USDT. O sistema separa o caminho de UX rapida do caminho financeiro critico:

- Quote e criacao de ordem respondem rapido para o frontend.
- Confirmacao fiat entra por webhook assinado.
- Delivery cripto roda em worker.
- Eventos e timestamps ficam persistidos para auditoria.

Fluxo critico:

```text
Cliente paga Pix -> Webhook confirma -> BuySendWorker dispara da wallet ChainFX -> USDT chega na wallet do cliente
```

## Requisitos

- Go: `1.25.0`, conforme `go.mod`.
- PostgreSQL.
- Signer BSC dedicado para producao.
- Provedor PIX/PagBank configurado.
- Full node/API BSC configurada.

## Diagrama de Sequencia

```mermaid
sequenceDiagram
    participant U as Usuario
    participant F as Frontend
    participant API as Go API
    participant DB as PostgreSQL
    participant PIX as Provedor PIX
    participant BUS as EventBus
    participant W as BuySendWorker
    participant S as Signer BSC
    participant T as Blockchain BSC

    U->>F: Informa BRL e wallet BSC
    F->>API: GET/POST /api/quote
    API-->>F: Cotacao, taxa, USDT estimado
    F->>API: POST /api/buy
    API->>PIX: Cria cobranca PIX
    API->>DB: INSERT buy_order aguardando_pix
    API-->>F: buyId + dados de pagamento
    U->>PIX: Paga PIX
    PIX->>API: POST /api/pix/webhook/buy
    API->>API: Valida HMAC e idempotencia
    API->>DB: status = pago_fiat
    API->>BUS: publish buy.paid
    BUS->>W: buy.paid
    W->>S: /hd/transfer HMAC
    S->>T: Broadcast USDT BEP20
    T-->>S: tx hash
    S-->>W: txHash
    W->>DB: status = enviado, tx_hash_out
    F->>API: SSE /api/buy/{id}/stream
    API-->>F: status enviado
```

## Componentes

- `cmd/api`: servidor HTTP publico.
- `cmd/benchflow`: ferramenta de benchmark do fluxo webhook/delivery.
- `internal/server`: handlers REST, webhooks, CORS, request ID, readiness e SSE.
- `internal/workers`: price, on-chain, payout, sweep e buy delivery.
- `internal/database`: schema, repositorios, eventos e persistencia LGPD.
- `internal/privacy`: hash e criptografia AES-GCM.
- `internal/BSC`: validacao e derivacao BSC.
- `internal/psp`: camada PSP com `Router`, `EfiAdapter`, health probe, fallback/restore e parsing de webhooks PIX em lote.
- `internal/paymaster`: Gas Station/Paymaster com oracle, estimator, idempotency, retry, batcher, token relayer e service top-level.
- `internal/rpc`: pool RPC EVM usado por on-chain workers, AutoSweeper e Paymaster.
- `internal/adversarial`: engine de cenarios adversariais/chaos acionado pelo painel admin.
- `signer`: servico isolado de assinatura. Em producao de BSC, usar signer com `SIGNER_NETWORK=BSC`.

## Fluxos Principais

### BUY BRL via PIX

1. Frontend chama `/api/quote`.
2. Frontend chama `/api/buy`.
3. API cria `buy_order` em `aguardando_pix`.
4. Provedor PIX chama `/api/pix/webhook/buy`.
5. API valida assinatura HMAC.
6. API verifica duplicidade via `webhook.provider`.
7. API marca `pago_fiat`.
8. API publica `buy.paid`.
9. `BuySendWorker` chama signer BSC.
10. Ordem vai para `enviado`.

### BUY BRL via Efí Credit Card

1. Frontend gera `payment_token` com a biblioteca JavaScript oficial da Efí.
2. Frontend chama `/api/buy` com `paymentMethod=credit_card`.
3. API cria a cobrança Efí em `/v1/charge` com `metadata.custom_id=buyId`.
4. API paga a cobrança em `/v1/charge/:id/pay` usando apenas o `payment_token`.
5. Efí chama `/api/efi/charges/webhook/buy` com token de notificação.
6. API consulta `GET /v1/notification/:token`.
7. Apenas status Efí `paid` marca `pago_fiat` e publica `buy.paid`.
8. Delivery segue o mesmo fluxo do PIX.

Status Efí `approved` e `waiting` não liberam cripto.

### SELL USDT -> PIX

1. Frontend cria `/api/order`.
2. API gera ou aceita endereco BSC.
3. `OnchainWorker` monitora transferencias USDT.
4. Deposito valido marca `pago`.
5. `PayoutWorker` liquida PIX.

### Gas Station / Paymaster

1. Cliente chama `/v1/gas/status` para verificar disponibilidade.
2. Cliente chama `/v1/gas/quote` para estimar gas/fee.
3. Cliente envia relay em `POST /v1/gas/relay`.
4. `internal/paymaster.Service` deduplica por `sig_hash`, estima gas via RPC pool e persiste `gas_relay_requests`.
5. O batcher agrupa relays em janela curta e o retry executa backoff exponencial com jitter.
6. Falha permanente vai para DLQ/status persistido; status pode ser consultado em `GET /v1/gas/relay/{id}`.

Arquivos principais:

| Arquivo | Responsabilidade |
| --- | --- |
| `internal/paymaster/oracle.go` | Gas oracle via BSC `eth_gasPrice`/RPC pool e cache curto |
| `internal/paymaster/estimator.go` | `eth_estimateGas`, conversao wei/gwei e fee USDT |
| `internal/paymaster/idempotency.go` | Dedup de assinatura / `sig_hash` |
| `internal/paymaster/retry.go` | Exponential backoff com jitter e DLQ |
| `internal/paymaster/batcher.go` | Batching de relays |
| `internal/paymaster/token_relayer.go` | Split net leg + fee leg, aritmetica inteira |
| `internal/paymaster/paymaster.go` | Service top-level, Quote, SubmitRelay, poller |

### NFC Closed-Loop

O trilho NFC e real e fechado: ele atende app mobile ChainFX + leitor/terminal ChainFX. O backend Go e a fonte de verdade do token, protocolo APDU/TLV, autorizacao, hold, capture e reverse.

1. O app chama `POST /api/mobile/nfc/provision` e recebe um token opaco `nfc1...` com TTL curto.
2. O app mobile entrega esse token ao leitor via NFC usando AID `F222222222` e tag `DF01`, sem PAN real e sem Track2 estatico.
3. O leitor chama `POST /api/nfc/authorize` com token, valor BRL, merchant, terminal e idempotency key.
4. O backend valida HMAC/expiracao do token, busca o saldo NFC da wallet e trava o valor USDT necessario.
5. Se houver saldo, responde `response_code=00` e status `approved`; se faltar saldo, responde `response_code=51` e status `requires_funding`.
6. Venda concluida chama `POST /api/nfc/authorizations/{id}/capture`.
7. Venda cancelada/falha chama `POST /api/nfc/authorizations/{id}/reverse`.

Arquivos principais:

| Arquivo | Responsabilidade |
| --- | --- |
| `internal/nfc/token.go` | Emissao e verificacao HMAC do token NFC opaco |
| `internal/nfc/protocol.go` | Contrato APDU/TLV do cartao digital fechado |
| `internal/nfc/hce.go` | Applet Go do contrato de cartao digital ChainFX |
| `internal/server/nfc_handlers.go` | Endpoints de provisionamento, autorizacao, capture, reverse e consulta |
| `internal/database/nfc.go` | Persistencia transacional de tokens, saldos e autorizacoes |
| `migrations/020_nfc_closed_loop.sql` | Schema do trilho NFC fechado |

### PSP Router Efi

Quando `cmd/api/main.go` encontra credenciais e certificado Efi, monta `EfiAdapter` e `psp.Router`.

- `handlePixWebhookBuy` usa `Router.ParseWebhookAll` quando disponivel.
- Cada evento PIX em lote e processado como settlement independente.
- Sem router configurado, o fluxo legado continua ativo.
- `WorkerManager.PSPRouter` roda health probe periodico para failover/restore.

### Chaos / Adversarial Ops

O painel admin consegue disparar cenarios reais em processo:

```http
POST /v1/admin/gas/chaos-run
GET  /v1/admin/gas/chaos-history
GET  /admin/chaos
```

Cenarios cobertos:

- `DB_CONNECTIVITY`
- `ONCHAIN_CONFIRMATION_FLOOR`
- `CONCURRENT_SIG_LOCK`
- `SSRF_WEBHOOK_VALIDATION`
- `RATE_LIMITER_FLOOD`
- `CONFIG_INTEGRITY`

## Status de Ordem

### BUY

| Status | Descricao | Proximo status |
| --- | --- | --- |
| `aguardando_pix` | Ordem criada e aguardando confirmacao PIX | `pago_fiat`, `erro` |
| `aguardando_credit_card` | Ordem criada e aguardando confirmacao final Efí cartão | `pago_fiat`, `erro` |
| `pago_fiat` | Pagamento fiat confirmado | `enviado`, `erro` |
| `pago_pix` | Alias legado para pagamento PIX confirmado | `enviado`, `erro` |
| `enviado` | Cripto enviada para wallet do cliente | Final |
| `delivered` | Cripto entregue/confirmada | Final |
| `confirmado` | Confirmacao final | Final |
| `erro` | Falha operacional ou rejeicao de provider/signer | Intervencao manual |

### SELL

| Status | Descricao | Proximo status |
| --- | --- | --- |
| `aguardando_deposito` | Aguardando deposito USDT do usuario | `pago`, `expirada`, `aguardando_validacao` |
| `aguardando_validacao` | Deposito fora da faixa/tolerancia | Intervencao manual |
| `expirada` | Ordem vencida | Final |
| `pago` | Deposito on-chain detectado | `concluida`, `erro` |
| `processando_payout` | PIX de saida em processamento | `concluida`, `erro` |
| `concluida` | PIX liquidado | Final |
| `erro` | Falha no payout ou validacao | Intervencao manual |

## Endpoints

### Health

```http
GET /healthz
GET /readyz
```

### Gas Station

```http
GET  /v1/gas/status
GET  /v1/gas/quote
POST /v1/gas/relay
GET  /v1/gas/relay/{id}
GET  /v1/gas/relays
GET  /v1/gas/sweeper/runs
```

### NFC Closed-Loop

```http
GET  /api/mobile/nfc/card
POST /api/mobile/nfc/provision
POST /api/nfc/provision
POST /api/nfc/authorize
GET  /api/nfc/authorizations/{id}
POST /api/nfc/authorizations/{id}/capture
POST /api/nfc/authorizations/{id}/reverse
GET  /api/nfc/balance/{wallet}?network=BSC
POST /api/nfc/sandbox/fund
```

`/api/mobile/nfc/provision` e o caminho normal do app. `/api/nfc/authorize`, `capture` e `reverse` sao chamadas de terminal/leitor ChainFX. `/api/nfc/sandbox/fund` so funciona com `ALLOW_SIMULATIONS=true`; em producao, o saldo NFC deve vir de deposito/escrow on-chain reconciliado pelo backend.

### Quote

```http
GET /api/quote?mode=buy&amountBRL=150&asset=USDT
GET /api/quote?mode=buy&amountFiat=150&fiatCurrency=BRL&paymentMethod=credit_card&asset=USDT
POST /api/quote
```

Resposta:

```json
{
  "mode": "buy",
  "asset": "USDT",
  "amountFiat": 150,
  "fiatCurrency": "BRL",
  "paymentMethod": "pix",
  "feeFiat": 12,
  "payoutFiat": 138,
  "rate": 5.43,
  "cryptoAmount": 25.41436464,
  "rateLockExpiresAt": "2026-07-03T03:00:00Z"
}
```

### Buy

```http
POST /api/buy
GET /api/buy/{id}
GET /api/buy/{id}/stream
```

PIX BRL:

```json
{
  "amountBRL": 150,
  "asset": "USDT",
  "address": "T..."
}
```

Efí Credit Card BRL:

```json
{
  "amountFiat": 150,
  "fiatCurrency": "BRL",
  "paymentMethod": "credit_card",
  "paymentToken": "payment_token_gerado_no_frontend",
  "cardBrand": "visa",
  "installments": 1,
  "asset": "USDT",
  "address": "0x...",
  "customer": {
    "name": "Maria Silva",
    "cpf": "12345678909",
    "email": "maria@example.com",
    "phone": "11999999999",
    "birthDate": "1990-05-20",
    "address": {
      "street": "Av Paulista",
      "number": "1000",
      "neighborhood": "Bela Vista",
      "zipcode": "01310100",
      "city": "Sao Paulo",
      "state": "SP"
    }
  }
}
```

### Sell

```http
POST /api/order
GET /api/order/{id}
GET /api/order/{id}/stream
POST /api/order/{id}/deposit
POST /api/order/{id}/payout
```

Payload:

```json
{
  "amountBRL": 150,
  "asset": "USDT",
  "network": "BSC",
  "pixCpf": "12345678901",
  "pixPhone": "11999999999"
}
```

## Webhooks

### Pix BUY

```http
POST /api/pix/webhook/buy
x-pagbank-signature: <hmac_sha256_hex_raw_body>
```

Payload aceito:

```json
{
  "buyId": "018f3f4e-0000-4000-9000-000000000000",
  "status": "concluido",
  "providerId": "pix_123456",
  "error": ""
}
```

Variantes de status que comecam com `conclu` sao tratadas como confirmacao.

### Pix SELL/Payout legado

```http
POST /api/pix/webhook
x-pagbank-signature: <hmac_sha256_hex_raw_body>
```

```json
{
  "orderId": "018f3f4e-0000-4000-9000-000000000000",
  "status": "concluido",
  "providerId": "pix_payout_123",
  "error": ""
}
```

### Efí Credit Card BUY

```http
POST /api/efi/charges/webhook/buy
Content-Type: application/x-www-form-urlencoded
```

Payload enviado pela Efí:

```text
notification=<token>
```

A API consulta a Efí com esse token e só liquida se o último status da cobrança for `paid`.

## Auditoria e LGPD

Campos de auditoria por ordem:

- `id`
- `request_id`
- `created_at`
- `updated_at`
- `paid_at`
- `settled_at`
- `delivered_at`
- `provider_payment_id`
- `tx_hash_out`

Tabelas de eventos:

- `order_events`
- `buy_order_events`

Cada evento guarda:

- `request_id`
- `type`
- `payload`
- `created_at`

LGPD:

- BUY minimiza dados pessoais e nao exige CPF/telefone.
- SELL exige chave PIX quando aplicavel.
- CPF/telefone sao salvos criptografados em `order_private`.
- Hashes ficam em `orders.pix_cpf_hash` e `orders.pix_phone_hash`.
- Sem `LGPD_SECRET`, o backend falha antes de persistir dado pessoal.

## Idempotencia

- Webhooks usam `providerId` em eventos `webhook.provider`.
- Existe indice unico parcial para evitar duplicidade de provider por ordem.
- Delivery usa `idempotencyKey` no signer.
- Endpoints internos aceitam `x-idempotency-key` quando aplicavel.

## Configuracao

Exemplo completo de `.env` para desenvolvimento/staging:

```env
# Runtime
APP_ENV=development
ALLOW_SIMULATIONS=true
PORT=3000
ALLOWED_ORIGINS=http://localhost:5173

# Database
DATABASE_URL=postgres://user:pass@localhost:5432/ChainFX?sslmode=disable

# Security
LGPD_SECRET=use-um-segredo-forte
WEBHOOK_SECRET=webhook-secret
PIX_WEBHOOK_SECRET=pix-webhook-secret

# Fees / limits
ORDER_MIN_BRL=10
ORDER_MAX_BRL=10000
RATE_LOCK_SEC=600
FEE_BPS=200
FEE_FIXED_USD=2
FEE_MIN_BRL=0

# Pix / PagBank
PAGSEGURO_API_TOKEN=token
PAGSEGURO_API_BASE_URL=https://api.pagseguro.com
PIX_CHARGE_ENDPOINT=/orders

# BSC
BSC_XPUB=xpub...
BSC_USDT_CONTRACT=0x55d398326f99059fF775485246999027B3197955
BSC_FULLNODE_URL=https://api.BSCgrid.io
BSC_SOLIDITY_URL=https://api.BSCgrid.io
BSC_USDT_DECIMALS=6
BSC_CONFIRMATIONS=20
BSC_HMAC_SECRET=internal-hmac-secret

# Signer
SIGNER_URL=http://localhost:4010
SIGNER_NETWORK=BSC
SIGNER_HMAC_SECRET=signer-hmac-secret

# Treasury
TREASURY_HOT=T...
TREASURY_COLD=T...
ENABLE_SWEEP_WORKER=false
ENABLE_SWEEP_STUB=false
SWEEP_FREQUENCY_MS=30000
BSC_GAS_RESERVE_BNB=5

# SMTP / ops
SMTP_HOST=smtp.example.com
SMTP_PORT=587
SMTP_USER=user
SMTP_PASS=pass
SMTP_SECURE=false
SMTP_FROM_EMAIL=ops@example.com
SMTP_FROM_NAME=ChainFX Ops
OPS_EMAIL=ops@example.com

# NFC closed-loop
NFC_ENABLED=true
NFC_TOKEN_SECRET=use-um-segredo-forte-diferente-do-jwt
NFC_TOKEN_TTL_SEC=120
NFC_HOLD_TTL_SEC=900
NFC_MAX_AMOUNT_BRL=500
```

Producao deve usar:

```env
APP_ENV=production
ALLOW_SIMULATIONS=false
SIGNER_NETWORK=BSC
ENABLE_SWEEP_STUB=false
```

## Deploy

### Railway

Arquivos:

- `Dockerfile`
- `railway.json`
- `.dockerignore`

O `railway.json` usa:

- builder: `DOCKERFILE`
- healthcheck: `/healthz`
- start command: `/app/api`

Variaveis obrigatorias no Railway:

```env
APP_ENV=production
ALLOW_SIMULATIONS=false
PORT=3000
DATABASE_URL=postgres://...
LGPD_SECRET=...
WEBHOOK_SECRET=...
PIX_WEBHOOK_SECRET=...
PAGSEGURO_API_TOKEN=...
SIGNER_URL=...
SIGNER_NETWORK=BSC
SIGNER_HMAC_SECRET=...
BSC_XPUB=...
BSC_USDT_CONTRACT=...
BSC_FULLNODE_URL=...
TREASURY_HOT=...
```

### Docker local

```bash
docker build -t ChainFX-payment-gateway .
docker run --rm -p 3000:3000 --env-file .env ChainFX-payment-gateway
```

### Migrations operacionais recentes

Aplicar conforme ambiente e historico de migrations:

```bash
psql $DATABASE_URL -f migrations/005_gas_station.sql
psql $DATABASE_URL -f gas_station.sql
psql $DATABASE_URL -f schema_chaos.sql
psql $DATABASE_URL -f schema_m2m.sql
psql $DATABASE_URL -f schema_agent_pricing.sql
```

`migrations/005_gas_station.sql` cria `gas_relay_requests` e `auto_sweeper_runs`. `gas_station.sql` consolida o schema de runtime do Paymaster/Gas Station para ambientes que ainda nao usam o runner incremental.

### Docker Compose

Exemplo minimo:

```yaml
services:
  api:
    build: .
    ports:
      - "3000:3000"
    env_file:
      - .env
    depends_on:
      - postgres

  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: ChainFX
      POSTGRES_USER: ChainFX
      POSTGRES_PASSWORD: ChainFX
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data

volumes:
  pgdata:
```

### Kubernetes

Nao ha manifest Kubernetes oficial neste repositorio. Se for necessario, criar:

- `Deployment` com readiness em `/readyz` e liveness em `/healthz`.
- `Secret` para variaveis sensiveis.
- `Service` HTTP.
- `HorizontalPodAutoscaler` apenas depois de medir gargalos reais.

## Benchmark E2E

Ferramenta:

```bash
go run ./cmd/benchflow -h
```

### Stress k6 Paymaster

```bash
k6 run tests/paymaster_stress.js \
  -e BASE_URL=https://api.chainfx.store \
  -e API_KEY_LIVE=sk_live_... \
  -e API_KEY_TEST=sk_test_...
```

Cenarios:

- `paymaster_spike`: ramping arrival rate para SLO de relay.
- `idempotency_collision`: mesma assinatura em concorrencia; espera 1 aceite e colisoes.
- `rate_limit_tier`: `sk_test_*` limitado.
- `gas_quote_load`: carga continua em quote.
- `gas_status_probe`: probe continuo de status.

### Preparar ambiente

1. Subir API com Postgres real.
2. Configurar `PIX_WEBHOOK_SECRET`.
3. Configurar signer BSC real se for medir `-mode e2e`.
4. Criar ordens BUY validas em `aguardando_pix`.
5. Salvar os IDs em `buy_ids.txt`, um por linha.

Exemplo `buy_ids.txt`:

```text
018f3f4e-0000-4000-9000-000000000001
018f3f4e-0000-4000-9000-000000000002
018f3f4e-0000-4000-9000-000000000003
```

### Benchmark ACK

Mede validacao de webhook, idempotencia, persistencia e publicacao `buy.paid`.

```bash
go run ./cmd/benchflow \
  -api http://localhost:3000 \
  -secret "$PIX_WEBHOOK_SECRET" \
  -buy-ids ./buy_ids.txt \
  -count 50 \
  -concurrency 8 \
  -mode ack \
  -json bench-ack.json \
  -csv bench-ack.csv
```

### Benchmark E2E

Mede ate o status `enviado`, `delivered` ou `confirmado`.

```bash
go run ./cmd/benchflow \
  -api http://localhost:3000 \
  -secret "$PIX_WEBHOOK_SECRET" \
  -buy-ids ./buy_ids.txt \
  -count 20 \
  -concurrency 4 \
  -mode e2e \
  -json bench-e2e.json \
  -csv bench-e2e.csv
```

### Gerar buy_ids.txt automaticamente

Em staging, com `ALLOW_SIMULATIONS=true` ou provider PIX configurado:

```bash
go run ./cmd/benchflow \
  -api http://localhost:3000 \
  -secret "$PIX_WEBHOOK_SECRET" \
  -create-buy \
  -address TXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX \
  -count 10 \
  -concurrency 2 \
  -mode ack
```

Se precisar reutilizar IDs, gere as ordens pelo frontend/admin ou por `POST /api/buy` e salve os `buyId` retornados em `buy_ids.txt`.

### Resultado de benchmark local

Data do teste: **03/07/2026**

Ambiente:

- OS: Windows
- Go arch: `386`
- CPU: Intel(R) Core(TM) i3-7100 CPU @ 3.90GHz
- Comando: `go test ./internal/workers -bench BenchmarkEventBus -benchtime=100ms -count=1`

Resultado:

```text
BenchmarkEventBusPublishNoSubscriber-4       27.23 ns/op    0 B/op    0 allocs/op
BenchmarkEventBusPublishSingleSubscriber-4   93.10 ns/op    0 B/op    0 allocs/op
BenchmarkEventBusPublishManySubscribers-4    1367 ns/op     0 B/op    0 allocs/op
```

Leitura:

- O barramento interno nao e gargalo relevante no caminho `webhook -> buy.paid -> BuySendWorker`.
- `webhook_ack_ms` e `delivery_ms` precisam ser medidos em staging com Postgres, signer BSC, secrets reais e `buy_ids` validos.
- O benchmark E2E real nao foi executado neste ambiente porque signer BSC e IDs de teste nao estavam disponiveis na sessao.

### Teste de fluxo com dinheiro ficticio

Existe um teste automatizado em memoria para validar a reacao esperada do backend sem dinheiro real, provider real ou signer real:

```bash
go test ./internal/settlement
```

O teste cobre:

- PIX ficticio confirmado.
- Transicao `aguardando_pix -> pago_fiat`.
- Publicacao do evento `buy.paid`.
- Worker simulado entregando token.
- Transicao final `pago_fiat -> enviado`.
- Geracao de `txHashOut` simulado.
- Bloqueio de webhook duplicado pelo mesmo `providerId`.
- Status rejeitado nao publica `buy.paid`.

Ultima execucao nesta sessao:

```text
go test ./...
PASS
```

Observacao: a API HTTP completa nao foi iniciada localmente porque o `DATABASE_URL` do `.env` estava malformado e a autenticacao do Postgres local em `localhost:5432` falhou com credenciais padrao. Para rodar o fluxo HTTP completo, configurar um `DATABASE_URL` valido e repetir com `cmd/benchflow`.

### Proximos gargalos provaveis

| Gargalo | Onde aparece | Como medir | Mitigacao |
| --- | --- | --- | --- |
| Postgres lento | `webhook_ack_ms` alto | `cmd/benchflow -mode ack` | indices, pool, menor payload em transacao |
| Provider enviando webhook duplicado | `duplicate=true` frequente | eventos `webhook.provider` | manter idempotencia e observar taxa |
| Signer/RPC BSC lento | `delivery_ms` alto | `cmd/benchflow -mode e2e` | signer dedicado, timeout, retry controlado, RPC redundante |
| EventBus cheio | drops ou delivery sem evento | metricas de fila | aumentar buffer, fila persistente se necessario |
| CoinGecko/price indisponivel | quote 503 | logs `PriceWorker` | cache, fallback controlado, provider alternativo |
| PagBank indisponivel | erro ao criar PIX/payout | logs provider e status 5xx | retry com idempotency key, circuit breaker |

## Troubleshooting

### API nao sobe

Verificar:

```bash
go run ./cmd/api
```

Erros comuns:

- `DATABASE_URL nao configurado`: configurar Postgres.
- `Configuracao invalida para producao`: alguma variavel obrigatoria esta ausente.
- `ALLOW_SIMULATIONS deve ser false em producao`: ajustar variavel no Railway.
- `SIGNER_NETWORK deve ser BSC em producao`: evitar signer EVM por engano.

### `/readyz` retorna `ok=false`

Chamar:

```bash
curl http://localhost:3000/readyz
```

Campos comuns em `warnings`:

- `pix_provider`: falta `PAGSEGURO_API_TOKEN`.
- `pix_webhook`: falta `PIX_WEBHOOK_SECRET` ou `WEBHOOK_SECRET`.
- `signer`: falta `SIGNER_URL` ou `SIGNER_HMAC_SECRET`.
- `signer_BSC`: `SIGNER_NETWORK` nao e `BSC`.
- `BSC_contract`: falta `BSC_USDT_CONTRACT`.
- `BSC_fullnode`: falta `BSC_FULLNODE_URL`.
- `lgpd_secret`: falta `LGPD_SECRET`.

### Webhook PIX recebe 401

Verificar:

- Header `x-pagbank-signature`.
- HMAC SHA-256 em hex do body bruto.
- Mesmo segredo entre provider e `PIX_WEBHOOK_SECRET`.
- Body nao pode ser reformatado depois de assinado.

### Ordem paga nao envia USDT

Verificar:

- Status da ordem: `GET /api/buy/{id}`.
- Logs do `BuySendWorker`.
- `SIGNER_URL`.
- `SIGNER_HMAC_SECRET`.
- `BSC_USDT_CONTRACT`.
- Se o signer e realmente BSC.

### Duplicidade de webhook

Comportamento esperado:

- Mesmo `providerId` para a mesma ordem deve retornar `duplicate=true`.
- Nao deve disparar segundo envio.

### Testes locais

```bash
go test ./cmd/api ./internal/config ./internal/server ./internal/workers
go build ./cmd/api
go test ./internal/workers -bench BenchmarkEventBus -benchmem
```

## Monitoramento

Ainda nao ha integracao Prometheus/Grafana neste repositorio.

Recomendacao para proxima etapa:

- Expor `/metrics` com Prometheus.
- Medir `webhook_ack_ms`, `buy_delivery_ms`, erros por provider e tamanho de fila.
- Dashboard Grafana com p50, p55, p95, p99.
- Alerta quando delivery ficar acima do SLO.

SLO inicial sugerido para staging:

| Metrica | Alvo inicial |
| --- | --- |
| `webhook_ack_ms p95` | < 300 ms |
| `webhook_ack_ms p99` | < 800 ms |
| `delivery_ms p95` | depende de signer/RPC, medir antes de fixar |
| `duplicate_webhook_rate` | monitorar, nao necessariamente erro |

## Rollback Operacional

Estrategia recomendada:

1. Pausar novos pagamentos no frontend ou provider se houver falha grave.
2. Manter webhooks aceitando callbacks para nao perder confirmacoes.
3. Desabilitar delivery automatico alterando configuracao do signer/worker apenas se houver risco de envio incorreto.
4. Preservar ordens em `pago_fiat` para reprocessamento controlado.
5. Corrigir deploy.
6. Reprocessar ordens pendentes por evento ou ferramenta operacional.
7. Conferir `buy_order_events`, `provider_payment_id` e `tx_hash_out` antes de qualquer reenvio.

Regra financeira: nunca apagar eventos para "corrigir" estado. Adicionar evento compensatorio ou executar migracao auditavel.
