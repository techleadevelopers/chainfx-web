# ChainFX Tap NFC Production Audit

Data: 2026-07-19

## Regra central

Autorizar rapido, persistir corretamente e liquidar de forma assincrona.

O ChainFX Tap e um trilho NFC proprio, fechado, sem Visa/Mastercard:

```text
Mobile HCE
  -> Terminal ChainFX
  -> API ChainFX
  -> autorizacao/hold no ledger interno USDT
  -> capture/reverse
  -> settlement merchant via Efi/PIX
  -> conciliacao
```

## Status atual

### O que esta alinhado

- O token NFC e local e verificavel por HMAC, sem chamada externa no authorize.
- O app mobile provisiona token curto e de uso unico.
- A autorizacao usa preco em memoria via `PriceWorker.GetPrice("BRL")`.
- A autorizacao nao chama blockchain.
- A autorizacao nao chama Efi/PIX.
- O ledger NFC e local em Postgres: `available_usdt_micro` e `locked_usdt_micro`.
- O hold e criado em transacao de banco.
- O token e revogado dentro da transacao de autorizacao.
- Capture e reverse sao transacionais.
- Eventos NFC entram no bus e nos webhooks:
  - `nfc.capture.completed`
  - `nfc.authorization.reversed`

### Correcoes aplicadas nesta auditoria

- `GET /api/nfc/authorizations/{id}` nao publica mais evento financeiro.
- `POST /api/nfc/authorizations/{id}/capture` publica `nfc.capture.completed`.
- `POST /api/nfc/authorizations/{id}/reverse` publica `nfc.authorization.reversed`.

## Fluxo critico de autorizacao

Implementacao atual:

```text
HTTP request
  -> NFC enabled/secret check
  -> ChainFX API key auth
  -> JSON decode
  -> BRL amount parse
  -> max amount check
  -> HMAC token verify
  -> memory price read
  -> DB transaction
     -> idempotency lookup
     -> token SELECT FOR UPDATE
     -> token revoke
     -> balance SELECT FOR UPDATE
     -> balance hold
     -> authorization insert
  -> response
```

Isso esta tecnicamente certo para baixa latencia porque o caminho sincronico fica limitado a CPU local + uma transacao Postgres curta.

## Gaps de producao

### Alta prioridade

1. Terminal/merchant registry real

Status: implementado para o caminho de autorizacao.

O backend agora possui `nfc_merchants` e `nfc_terminals`, com API key de terminal guardada como hash SHA-256. O bootstrap operacional pode ser feito por `NFC_TERMINALS`:

```env
NFC_TERMINALS=merchant_demo:terminal_01:chave-forte-do-terminal:Demo Merchant
```

Modelo de politica:

```text
merchant_id
terminal_id
api_key_hash SHA-256
status
max_amount_brl
daily_limit_brl
settlement_pix_key
settlement_document
risk_policy_version
```

O authorize deve validar se o `terminal_id` pertence ao `merchant_id` e se a chave usada tem permissao para aquele terminal.

2. Idempotencia deve ser por terminal

Status: implementado.

`nfc_authorizations.idempotency_key` deixa de ser globalmente unico e passa a ter indice unico por terminal:

```sql
UNIQUE (terminal_id, idempotency_key)
```

O replay retorna a mesma autorizacao somente quando terminal, merchant, wallet, rede, external_ref, valor BRL e valor USDT requerido batem. Payload diferente retorna conflito.

3. Outbox duravel no capture

Atualmente o capture publica evento em memoria depois do commit. Isso e melhor do que liquidar PIX no caminho sincronico, mas ainda nao e evidencia financeira duravel para settlement.

Recomendado:

```text
capture transaction
  -> debit locked USDT
  -> mark authorization captured
  -> insert merchant_settlement_obligation
  -> insert outbox event
commit
worker reads outbox
  -> Efi/PIX
  -> receipt
  -> reconciliation
```

4. Expiracao automatica de holds

Status: implementado.

`NFCExpirationWorker` varre holds aprovados vencidos com `FOR UPDATE SKIP LOCKED`, devolve `locked_usdt_micro` para `available_usdt_micro` e marca a autorizacao como `expired`.

5. Funding real do ledger NFC

`nfc_wallet_balances` precisa ser alimentado por evento reconciliavel:

```text
deposito on-chain confirmado
ou saldo custodial liberado
ou funding manual auditado
```

O endpoint sandbox/fund deve continuar bloqueado em producao.

### Media prioridade

6. Staleness explicito da cotacao

Status: implementado.

`PriceWorker` expoe `PriceSnapshot` com `UpdatedAt`, e o authorize rejeita cotacao velha usando:

```text
NFC_PRICE_MAX_AGE_SEC=30
```

Se a cotacao estiver velha:

```text
PRICE_STALE -> decline/fail closed
```

7. Risk leve no caminho sincronico

Hoje existe limite global por valor (`NFC_MAX_AMOUNT_BRL`). Falta:

- limite diario por wallet;
- limite diario por terminal;
- quantidade de taps por minuto;
- bloqueio por device comprometido;
- merchant/terminal status.

Essas regras devem ser locais/cacheadas e rapidas.

8. Metricas por etapa

Status: parcialmente implementado via `Server-Timing` para diagnostico HTTP.

O header inclui etapas como:

```text
terminal_auth
json_decode
amount_parse
token_validation
price_lookup
risk_validation
authorization_lookup
db_transaction
ledger_capture
ledger_reverse
server_total
```

Ainda falta exportar histogramas Prometheus persistentes para SLO.

## Metas de latencia

Para a rota `POST /api/nfc/authorize`:

```text
p50 < 100 ms
p95 < 250 ms
p99 < 500 ms
timeout total <= 500 ms
```

Timeouts internos recomendados:

```text
DB transaction: 100-200 ms
Redis/rate limit: 20-50 ms
request total: 500 ms
```

## Ordem recomendada de implementacao

1. Terminal/merchant registry real com API key por terminal.
2. Idempotencia por `(terminal_id, idempotency_key)` com payload replay check.
3. Worker de expiracao/reversao de holds vencidos.
4. Outbox duravel + tabela de `merchant_settlement_obligations`.
5. Worker Efi/PIX com concorrencia limitada e retry/DLQ.
6. Staleness de cotacao no authorize.
7. Risk leve cacheado no authorize.
8. Metricas por etapa e teste de carga 10/50/100 autorizacoes concorrentes.

## Testes staff-level

### Unidade e contrato

```powershell
go test -race ./internal/nfc ./internal/database ./internal/server ./internal/workers -count=1
```

Cobre:

- APDU/TLV malformado;
- token expirado/tamperado;
- registry de terminal;
- idempotencia por terminal;
- freshness de cotacao;
- expiracao de holds.

### RPA de produto NFC

Modo nao mutavel:

```powershell
node tests\nfc_product_rpa.js
```

Modo sandbox/staging mutavel:

```powershell
$env:NFC_RPA_RUN_MUTATING="true"
$env:NFC_RPA_BASE_URL="https://api-staging.chainfx.com"
$env:NFC_RPA_CHAINFX_API_KEY="sk_test_..."
$env:NFC_RPA_TERMINAL_KEY="terminal_key..."
$env:NFC_RPA_MERCHANT_ID="merchant_demo"
$env:NFC_RPA_TERMINAL_ID="terminal_01"
$env:NFC_RPA_WALLET="0x..."
$env:NFC_RPA_ITERATIONS="10"
node tests\nfc_product_rpa.js
```

O script valida:

- endpoints de terminal exigem credencial;
- provisionamento;
- funding sandbox, quando habilitado;
- authorize;
- replay com mesma idempotency key;
- mismatch de idempotencia;
- capture duplicado controlado;
- reverse;
- isolamento por chave errada;
- percentis de latencia;
- amostras de `Server-Timing`.

### Carga k6

```powershell
k6 run tests\nfc_authorize_load.js
```

Variaveis:

```powershell
$env:NFC_BASE_URL="https://api-staging.chainfx.com"
$env:NFC_CHAINFX_API_KEY="sk_test_..."
$env:NFC_TERMINAL_KEY="terminal_key..."
$env:NFC_MERCHANT_ID="merchant_demo"
$env:NFC_TERMINAL_ID="terminal_01"
$env:NFC_WALLET="0x..."
$env:NFC_K6_RATE="50"
$env:NFC_K6_DURATION="5m"
```

Thresholds iniciais:

```text
nfc_authorize_latency p50 < 100ms
nfc_authorize_latency p95 < 250ms
nfc_authorize_latency p99 < 500ms
nfc_failed_requests < 0.1%
```

## Conclusao

O fluxo NFC atual faz sentido para piloto tecnico: autoriza sem blockchain/Efi no caminho quente, usa token curto, ledger local e hold transacional.

Para dinheiro real em producao, os bloqueadores sao: registry real de terminal/merchant, outbox duravel de settlement, expiracao automatica de holds, idempotencia por terminal com payload check e conciliacao PIX/Efi contra o ledger USDT.
