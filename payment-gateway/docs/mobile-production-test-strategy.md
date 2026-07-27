# ChainFX Mobile Production/Testnet Strategy

## Decisao

O app mobile pode ser testado de forma real antes de producao, mas nao basta Hardhat.

- Hardhat/local: bom para teste deterministico de contrato ERC20 mock, assinatura, nonce, saldo, transferencia e adversarial.
- Testnet publica: necessaria para validar RPC real, gas, explorer, chainId, propagacao e comportamento de wallet em rede EVM.
- Efí homologacao: necessaria apenas para PIX/PSP. Como o Pix ja foi validado, nao precisa misturar com teste on-chain.
- NFC closed-loop: nao precisa blockchain no caminho de autorizacao. Precisa backend, ledger NFC, terminal key, token HCE e testes de hold/capture/reverse.

Para staging/testnet mobile, configure uma API separada da producao com banco separado.

## Rotas mobile criticas

### Login e sessao

| Rota | Criticidade | Meta |
|---|---:|---|
| `POST /api/mobile/auth/register` | alta | cria usuario e wallet sem duplicidade |
| `POST /api/mobile/auth/login` | alta | p95 < 300 ms |
| `POST /api/mobile/auth/refresh` | alta | p95 < 200 ms |
| `GET /api/mobile/user/profile` | media | p95 < 120 ms |

Teste real:

- criar usuario novo;
- login;
- refresh;
- abrir profile;
- verificar que `wallet_address` existe;
- verificar que usuario nao consegue acessar dados de outro usuario.

### Carteira e receber

| Rota | Criticidade | Meta |
|---|---:|---|
| `GET /api/mobile/wallet/address` | alta | p95 < 100 ms |
| `GET /api/mobile/wallet/balance` | alta | p95 < 400 ms se on-chain, < 80 ms se ledger/cache |
| `GET /api/mobile/wallet/history` | media | p95 < 180 ms |
| `GET /api/mobile/wallet/tokens` | media | p95 < 120 ms |

Teste real:

- criar usuario;
- confirmar wallet unica;
- enviar mock USDT testnet para wallet do usuario;
- esperar saldo aparecer;
- abrir tela Carteira e Receber;
- validar que o endereco exibido e o mesmo do backend.

Gap atual de latencia:

`wallet/balance` consulta RPC on-chain no request. Para baixa latencia, a versao final deve usar ledger/cache interno atualizado por worker on-chain, e RPC apenas para reconciliacao.

### Enviar

| Rota | Criticidade | Meta |
|---|---:|---|
| `POST /api/mobile/wallet/transfer` | critica | p95 < 2.5 s para submissao testnet |

Pre-requisitos:

- `MOBILE_WALLET_ENCRYPTION_SECRET` configurado;
- private key custodial importada em `mobile_wallet_keys`;
- wallet com gas testnet;
- wallet com token ERC20 testnet;
- `BSC_RPC_URLS` ou `POLYGON_RPC_URLS`;
- `BSC_CHAIN_ID=97` para BSC testnet ou `POLYGON_CHAIN_ID=80002` para Polygon Amoy;
- `BSC_USDT_CONTRACT`/`POLYGON_USDT_CONTRACT` apontando para token mock testnet.

Teste real:

- configurar PIN;
- tentar transfer sem PIN, esperar `401`;
- tentar PIN errado, esperar `401`;
- transferir valor pequeno com PIN correto;
- receber `tx_hash`;
- consultar explorer/RPC;
- garantir idempotency: mesma chave nao deve duplicar envio;
- repetir com saldo insuficiente.

### Comprar

| Rota | Criticidade | Meta |
|---|---:|---|
| `POST /api/mobile/order/buy` | critica | p95 depende Efí; alvo < 900 ms em staging |
| `GET /api/mobile/order/{id}` | alta | p95 < 180 ms |
| `GET /api/mobile/ws/orders` | alta | push de status sem polling pesado |

Teste real:

- criar ordem buy Pix;
- backend deve retornar Pix copia-e-cola real;
- app nao deve inventar codigo Pix se backend falhar;
- confirmar status por `GET /api/mobile/order/{id}`;
- simular webhook Pix homolog/sandbox;
- worker deve criar envio para wallet do usuario;
- em testnet, validar txHash de envio se signer testnet estiver ligado.

### Vender

| Rota | Criticidade | Meta |
|---|---:|---|
| `POST /api/mobile/order/sell` | critica | p95 < 350 ms sem PIX automatico |
| `GET /api/mobile/order/{id}` | alta | p95 < 180 ms |

Teste real:

- criar sell;
- backend deve retornar endereco de deposito;
- app nao deve usar endereco hardcoded;
- enviar USDT testnet para endereco de deposito;
- onchain worker detecta tx;
- status vira `paid`/`awaiting_manual_pix`;
- admin faz payout manual no piloto;
- ordem vira `settled`.

### Swap

| Rota | Criticidade | Meta |
|---|---:|---|
| `POST /api/mobile/swap/quote` | critica | p95 < 150 ms |
| `POST /api/mobile/swap/execute` | critica | p95 < 300 ms se interno; maior se on-chain |
| `GET /api/mobile/swap/{id}` | alta | p95 < 150 ms |

Regra:

- app nao deve executar sem quote valida;
- quote deve ter `quote_id` e expiracao;
- execute deve rejeitar quote ausente/expirada;
- resultado deve vir de `GET /api/mobile/swap/{id}`.

Gap atual de backend:

`swap/quote` ainda retorna cotacao simples sem `quote_id` persistido. Para producao real, criar tabela `mobile_swap_quotes` com quote lock, expiracao e payload hash.

### NFC closed-loop

| Rota | Criticidade | Meta |
|---|---:|---|
| `GET /api/mobile/nfc/card` | alta | p95 < 150 ms |
| `POST /api/mobile/nfc/provision` | critica | p95 < 250 ms |
| `POST /api/nfc/authorize` | ultra critica | p50 < 100 ms, p95 < 250 ms, p99 < 500 ms |
| `POST /api/nfc/authorizations/{id}/capture` | critica | p95 < 200 ms |
| `POST /api/nfc/authorizations/{id}/reverse` | critica | p95 < 200 ms |

Teste real:

- provisionar token HCE no app;
- terminal simulator autoriza;
- validar single-use token;
- validar idempotencia por terminal;
- capture duplicado nao duplica ledger;
- reverse duplicado nao devolve saldo duas vezes;
- capture vs reverse concorrente tem apenas um vencedor;
- hold expira e devolve saldo;
- settlement PIX fica assicrono.

## Ambiente staging/testnet recomendado

Use Railway/staging separado:

```env
APP_ENV=staging
ALLOW_SIMULATIONS=false
DATABASE_URL=postgres_staging
REDIS_URL=redis_staging

BSC_RPC_URLS=https://data-seed-prebsc-1-s1.bnbchain.org:8545
BSC_CHAIN_ID=97
BSC_USDT_CONTRACT=<endereco_real_do_MockUSDT3009_deployado_na_BSC_testnet>

POLYGON_RPC_URLS=https://polygon-amoy-bor-rpc.publicnode.com
POLYGON_CHAIN_ID=80002
POLYGON_USDT_CONTRACT=<endereco_real_do_MockUSDT3009_deployado_na_Polygon_Amoy>

MOBILE_WALLET_ENCRYPTION_SECRET=...
CHAINFX_REQUIRE_API_KEY=true
CHAINFX_TEST_SECRET_KEYS=sk_test_...

NFC_ENABLED=true
NFC_TOKEN_SECRET=...
NFC_TERMINALS=merchant_demo:terminal_01:terminal_key_forte:Demo Merchant
NFC_PRICE_MAX_AGE_SEC=180
SELL_PAYOUT_MODE=manual
```

Para gerar estes dois enderecos reais, use o contrato `contracts/src/mocks/MockUSDT3009.sol`:

```powershell
cd C:\Users\Paulo\Desktop\payment-gateway\contracts

# A carteira de deploy configurada hoje e:
# 0x7082e0646B203D0bfccA71B4f80890CEeaad7014
#
# Ela precisa receber BNB testnet antes deste comando.
npm run deploy:eip-probe-usdt:bsc-testnet

# Ela precisa receber POL/MATIC testnet na Amoy antes deste comando.
npm run deploy:eip-probe-usdt:amoy
```

O script valida o `chainId` antes do deploy. Use no `.env` de staging apenas o endereco impresso em `BSC_USDT_CONTRACT=` e `POLYGON_USDT_CONTRACT=`.

## Sequencia de teste real

1. `go test ./...`
2. `go test -race ./internal/mobile ./internal/nfc ./internal/workers`
3. Deploy API staging.
4. Criar usuario mobile novo.
5. Importar private key custodial de teste:

```powershell
$env:MOBILE_WALLET_IMPORT_PRIVATE_KEY="0x_test_private_key"
go run ./cmd/import-mobile-wallet-key --email "teste@chainfx.com" --address "0x_wallet_testnet"
```

6. Enviar gas testnet para a wallet.
7. Enviar mock USDT testnet para a wallet.
8. Rodar fluxo Receber/Carteira/Enviar no app.
9. Rodar Buy/Sell com valores pequenos e Pix homolog se necessario.
10. Rodar NFC RPA:

```powershell
$env:NFC_RPA_RUN_MUTATING="true"
$env:NFC_RPA_BASE_URL="https://api-staging..."
$env:NFC_RPA_CHAINFX_API_KEY="sk_test_..."
$env:NFC_RPA_TERMINAL_KEY="terminal_key_forte"
$env:NFC_RPA_MERCHANT_ID="merchant_demo"
$env:NFC_RPA_TERMINAL_ID="terminal_01"
$env:NFC_RPA_WALLET="0x_wallet_testnet"
$env:NFC_RPA_ITERATIONS="10"
node tests\nfc_product_rpa.js
```

11. Rodar carga NFC:

```powershell
$env:NFC_BASE_URL="https://api-staging..."
$env:NFC_CHAINFX_API_KEY="sk_test_..."
$env:NFC_TERMINAL_KEY="terminal_key_forte"
$env:NFC_MERCHANT_ID="merchant_demo"
$env:NFC_TERMINAL_ID="terminal_01"
$env:NFC_WALLET="0x_wallet_testnet"
$env:NFC_K6_RATE="50"
$env:NFC_K6_DURATION="5m"
k6 run tests\nfc_authorize_load.js
```

## Gate para liberar piloto

Bloqueia piloto se:

- wallet criada sem chave custodial quando fluxo e custodial;
- transfer aceita sem PIN quando PIN existe;
- saldo negativo;
- idempotency duplicando transferencia, capture, reverse ou sell;
- `POST /api/mobile/order/sell` nao retorna endereco de deposito;
- `POST /api/mobile/order/buy` nao retorna Pix copia-e-cola;
- NFC authorize p95 > 250 ms;
- capture/reverse duplicam ledger;
- onchain worker nao detecta deposito testnet;
- tracking nao reflete status real do backend.

## Melhorias de baixa latencia por prioridade

1. Wallet balance por ledger/cache interno, nao RPC por request.
2. Remover loopback HTTP de `mobile/order/buy`, `mobile/order/sell`, `mobile/order/{id}`.
3. Persistir quote real para swap com expiracao e payload hash.
4. WebSocket/SSE de order status em vez de polling mobile.
5. RPC clients persistentes para envio custodial.
6. Outbox duravel para eventos financeiros.
7. Metricas por rota: p50/p95/p99, DB wait, RPC wait, worker lag.
