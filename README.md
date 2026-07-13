
<div align="center"><img src="https://res.cloudinary.com/limpeja/image/upload/v1770993671/swap_1_mvctri.png" alt="swap Logo" width="280"></div>

## Swap buy and sell instans Cripto or Pix ↔ BRL com UX Instantânea
## Visão Executiva
Swappy financial é uma stack de on/off-ramp focada em “Sell” (usuário envia cripto, recebe PIX). Mantemos um backend enxuto (Express) endurecido e preparado para evoluir com workers e filas: API pública para criação/consulta de ordens, worker on-chain para detectar depósitos e worker PIX para liquidar payouts. Tudo validado com schema de ambiente, limites configuráveis e cache de preço.

# Product Interface Finance

<div align="center">
  <img src="https://res.cloudinary.com/limpeja/image/upload/v1783058055/bf895cd7-96a7-4264-8cae-aa51a6d24e58.png" alt="Swappy Logo" width="1024" />
</div>

---

## 📱 ChainFX — Instant Digital FX Payments

**Swappy** é uma plataforma financeira web que permite comprar e vender criptomoedas de forma instantânea e segura. Com integração via PIX, você pode realizar transações em segundos com total confiabilidade.

### ✨ Diferenciais da Plataforma

- ⚡ **Compre e venda cripto instantaneamente** via PIX
- 🔒 **Transações seguras** e sem complicações
- 👥 **950.000+ usuários** confiam na Swappy
- 💳 **30+ opções** de pagamento locais
- 🪙 **100+ criptomoedas** disponíveis

---

## 🛒 Fluxo de Compra (Buy) - Step 1

### Informe o valor e visualize a cotação

<div align="center">
  <img src="https://res.cloudinary.com/limpeja/image/upload/v1783058374/compra-removebg-preview_ikab4t.png" alt="Swappy - Tela de Compra" width="600" />
</div>

**Como funciona:**

1. Selecione a moeda que deseja pagar (BRL)
2. Informe o valor que deseja comprar
3. Visualize a cotação atualizada em tempo real
4. Confirme a quantidade de cripto que irá receber

---

## 💳 Fluxo de Pagamento - Step 2

### Insira sua wallet e escolha o método de pagamento

<div align="center">
  <img src="https://res.cloudinary.com/limpeja/image/upload/v1783058436/sp2-removebg-preview_iarh45.png" alt="Swappy - Tela de Pagamento" width="600" />
</div>

**Como funciona:**

1. **Informe sua Wallet** - Cole o endereço da sua carteira (ETH, BTC, USDT)
2. **Escolha o método de pagamento**:
   - 💰 **PIX** - Instantâneo e sem taxas extras
   - 💳 **VISA** - Cartão de crédito internacional
   - 💳 **Mastercard** - Cartão de crédito internacional
3. **Confirme a transação** e receba suas criptos em segundos

---

## 🔄 Fluxo de Venda (Sell)

### Venda suas criptos e receba em reais

1. Selecione a criptomoeda que deseja vender
2. Informe a quantidade
3. Escolha o método de recebimento (PIX)
4. Confirme a transação e receba em sua conta

---

## Capabilidades Principais
- **Fluxo Sell seguro**: status inicial `aguardando_deposito`, cotação travada com TTL, validação de endereço (BTC/ETH) e limites min/max configuráveis.
- **Validação robusta**: Zod em payloads e schema de ambiente; CORS restrito, rate limit e Helmet habilitados.
- **Cotações com cache**: worker de preço com TTL curto (CoinGecko).
- **Filas / eventos (stub)**: event bus em memória já publica `order.created` → `onchain.detected` → `payout.settled`, pronto para migrar para SQS/PubSub/Kafka.
- **Persistência preparada**: PostgreSQL com tabelas `orders`, `order_events`, `payouts` (schema incluso), fallback in-memory apenas para demo.
- **Workers stubs**: on-chain, payout e price-cache prontos para plugar lógica real.
- **Hardening inicial**: Helmet, rate limit geral e por rota de criação, CORS restrito, logger Pino; endpoints de depósito/payout com auditoria em DB.

## Arquitetura de Alto Nível
- **Frontend**: HTML/CSS/JS (Vite ou servidor estático).
- **Backend (monolito + workers)**:
  - API Express: cria/consulta ordens, trava cotação, valida inputs.
  - Worker on-chain (stub): consumirá fila, validará depósitos e confirmações.
  - Worker PIX (stub): consumirá payout.requested e chamará provedor PIX.
  - Worker price-cache: cacheia preço em memória (TTL curto).
- **Banco**: PostgreSQL (orders/eventos/payouts) + Redis/filas (a integrar) para cache/locks.
- **Segurança**: Helmet, rate limit configurável, CORS por domínio, webhook com segredo opcional, schema de env obrigatório.

## Security & Key Management (Web3/Fintech focus)
- **Key isolation by network**:  
  - BSC signer em `signer/` (BEP20) usa `SIGNER_PRIVATE_KEY` / `BSC_XPRV`; roda separado.  
  - BSC/EVM signer em `bsc-signer/` (BEP20) usa `EVM_PRIVATE_KEY`; roda em serviço próprio.  
  - Nunca reuse chave entre redes; deployer ≠ treasury; use chaves hot mínimas e transfira ownership/tesouraria para multisig/hardware.
- **HMAC + anti-replay**: rotas de signing exigem `x-signer-hmac`, `x-ts`, `x-nonce` (Zod valida). Janela configurável (`HMAC_MAX_SKEW_SEC`), replay guard, idempotência com Postgres (fallback in-memory).
- **Allowlists**: `ALLOW_DEST`, `ALLOW_TOKEN_CONTRACTS` limitam destinos e tokens assináveis (habilite em produção).
- **Secrets**: `.env` sempre fora de VCS; exemplos em `.env.example`. Prefira injetar via variables do ambiente/secret manager.
- **Deploy safety (BSC)**: use deployer dedicado no `.env` (pouco BNB); após deploy, chame `setTreasury`/`transferOwnership` para multisig; remova/rotate o deployer.  
- **Deploy safety (BSC)**: signer hot separado; não armazene seed, só pk; `BSC_XPRV` opcional para derivação HD (guardado só em ambiente seguro).
- **Transport**: colocar os signers atrás de TLS/reverse proxy; habilitar rate limit/WAF no ingress.

## Signing Services
- **BSC signer** (`signer/server.js`):  
  - `POST /sign/transfer` (BEP20) e `POST /sign/hd/transfer` (derivação HD).  
  - Env: `SIGNER_PRIVATE_KEY`, `SIGNER_HMAC_SECRET`, `BSC_FULLNODE_URL`, `ALLOW_*`, opcional `DATABASE_URL`.  
- **BSC signer** (`bsc-signer/server.js`):  
  - `POST /evm/sign/transfer` (BEP20/ERC20) e `GET /evm/health`.  
  - Env: `EVM_PRIVATE_KEY`, `RPC_URL` (default BSC), `HMAC_SECRET`, `ALLOW_*`, opcional `DATABASE_URL`.  
  - Dockerfile próprio em `bsc-signer/` para deploy isolado.

## Como Rodar (Dev/Homologação)
1) Instale dependências  
   ```bash
   npm install
   ```
2) Configure `.env` a partir de `.env.example` (use RPC de testnet e chave sem fundos).
3) Inicialize o schema no Postgres (ajuste `DATABASE_URL`):  
   ```bash
   psql "$DATABASE_URL" -f server/schema.sql
   ```
4) Suba backend  
   ```bash
   node server/server.js
   ```
5) Suba frontend  
   - via Vite: `npm run dev`  
   - ou servidor estático: `npx http-server .` (ou Live Server do VS Code).

Frontend acessa `http://localhost:3000/api/price` por padrão; defina `window.API_BASE` no console se usar outra origem.

## Agent Console e Developer Console

Workspaces adicionados no frontend Vite:

- `http://localhost:5173/app/agent/`
- `http://localhost:5173/app/developer/`

Arquivos principais:

- `app/agent/index.html`: Agent Console.
- `app/developer/index.html`: Developer Console.
- `app/shared/console.css`: design system compartilhado, alinhado ao visual da `index.html`.
- `app/shared/console.js`: cliente HTTP, render, forms, API Explorer e actions.
- `vite.config.js`: entradas Vite `agentConsole` e `developerConsole`.

### Backend esperado

Por padrão, os consoles usam:

```text
http://localhost:8080
```

O backend pode ser alterado no topo da interface pelo campo `API base URL`. A API key deve ser enviada como Bearer no campo `API key`.

Endpoints consumidos:

- `GET /app/agent/summary`
- `GET /app/developer/summary`
- `POST /agent/connect`
- `GET /agent/{id}/policy`
- `PATCH /agent/{id}/policy`
- `GET /developer/projects`
- `POST /developer/projects`
- `PATCH /developer/projects/{id}`
- `GET /developer/api-keys`
- `POST /developer/projects/{id}/api-keys`
- `POST /developer/api-keys/{id}/rotate`
- `POST /developer/api-keys/{id}/disabled`
- `POST /developer/api-keys/{id}/revoked`

### Funcionalidade entregue

Agent Console:

- overview de agentes, saldo, gasto, quota e settlements;
- lista de agents;
- marketplace visual de capabilities;
- purchases, executions, wallet, usage/costs e settlements;
- criação de agente com policy real;
- envio de `allowedAssets`, `allowedCapabilities`, `allowedProviders`, `permissions`, limites e fallback;
- leitura de policy real no resumo.

Developer Console:

- overview técnico;
- CRUD inicial de Projects;
- criação de API Keys reais por projeto;
- secret exibido uma única vez;
- rotate, disable e revoke de API key;
- listagem de scopes, status e usage;
- MCP connection config;
- API Explorer inicial;
- provider publish form visual;
- billing/webhooks/logs visuais.

### Build

```bash
npm run build
```

Saídas geradas:

- `dist/app/agent/index.html`
- `dist/app/developer/index.html`

### E2E/MCP

Os testes E2E/MCP ficam no backend Go (`payment-gateway/tests/e2e`) e são opt-in:

```env
RUN_E2E_TESTS=false
RUN_TESTNET_PAYMENT_TESTS=false
RUN_LIVE_PAYMENT_TESTS=false
E2E_BASE_URL=http://localhost:8080
E2E_API_KEY=
E2E_AGENT_WALLET=0x0000000000000000000000000000000000001001
E2E_PAYER_WALLET=0x0000000000000000000000000000000000001001
E2E_DEST_WALLET=0x0000000000000000000000000000000000001001
E2E_PAYMENT_ASSET=USDT
E2E_PIX_KEY=e2e@example.com
E2E_TEST_WALLET_PRIVATE_KEY=
E2E_TEST_TX_HASH=
E2E_TEST_LOG_INDEX=0
LIVE_PAYMENT_MAX_USD=1.00
LIVE_PAYMENT_CONFIRMATION_REQUIRED=true
```

Rodar E2E:

```powershell
$env:RUN_E2E_TESTS="true"
$env:E2E_API_KEY="sk_test_cfx_..."
go test ./tests/e2e -v
```

## Configuração (.env)
```
RPC_URL=...              # RPC da rede (use sepolia/goerli para testes)
HOT_WALLET_KEY=...       # Chave privada da hot wallet (NÃO usar fundos reais)
TOKEN_ADDRESS=...        # Contrato ERC-20 (ex.: WBTC em testnet)
TOKEN_DECIMALS=8         # Decimais do token (WBTC=8)
ALLOWED_ORIGINS=*        # Whitelist CORS (use domínios em produção)
WEBHOOK_SECRET=...       # Segredo para confirmar pagamentos
DATABASE_URL=postgres://user:pass@host:5432/db
ORDER_MIN_BRL=10
ORDER_MAX_BRL=100000
RATE_LIMIT_WINDOW_MS=60000
RATE_LIMIT_MAX=100
```

## Fluxos de Usuário
- **Buy (BRL → Cripto)**  
  1) Usuário informa valor em BRL.  
  2) Seleciona método de pagamento (PIX/cartão).  
  3) Informa endereço cripto (ETH ou BTC).  
  4) Recebe ordem + QR/PIX; polling até `concluída`.

- **Sell (Cripto → BRL via PIX)**  
  1) Usuário informa valor em BTC/USDT (campo Pay).  
  2) Informa CPF e Chave PIX.  
  3) Etapa final exibe endereço de recebimento; após depósito confirmado (worker on-chain), backend liquida via PIX (worker payout) e marca `concluída`.

## Endpoints REST
- `GET /api/price` — retorna preço BTC em BRL (CoinGecko).
- `POST /api/order` — cria ordem `{ amountBRL, address, paymentMethod, pixCpf?, pixPhone? }` com trava de cotação e status `aguardando_deposito`.
- `GET /api/order/:id` — status da ordem.
- `POST /api/order/:id/confirm` — webhook/confirmador (requer `x-webhook-secret` se definido).

## Notas de Segurança (para produção)
- Usar KMS/HSM ou custodial para a chave; hot wallet exposta não é aceitável em prod.
- Restringir CORS, habilitar WAF e rate limiting; auth HMAC/JWT em webhooks internos.
- Confirmar apenas após valor ≥ esperado e confirmações on-chain configuráveis; decimais por ativo/rede.
- Segredos em Vault/Secrets Manager; TLS end-to-end; logs estruturados e métricas.

## Roadmap Sugerido
- Conectar fila gerenciada (SQS/PubSub/Kafka) e Redis para cache/locks.
- Worker on-chain real (BTC/ETH/USDT) e integração PIX oficial com webhooks assinados.
- SSE/WebSocket para status (substituir polling).
- Painel operacional + alertas (saldo hot wallet, divergência, filas).
- Testes unit/integration + E2E em testnet (depósito → confirmações → payout sandbox).

## Time-to-Value
- Em modo demo (testnet) a jornada completa roda em minutos.
- Em produção, basta plugar provedor PIX e monitor on-chain para ter um on/off-ramp auditável.
