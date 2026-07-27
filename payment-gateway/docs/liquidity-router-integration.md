# ChainFX Liquidity Router Integration

Documento operacional da integracao BUY/DCA com Liquidity Router no backend `payment-gateway`.

Validacao cloud registrada em 2026-07-23 contra:

```text
https://api-production-bc748.up.railway.app
```

## Objetivo

O ChainFX nao precisa manter estoque proprio de todos os ativos. O backend opera como broker/orquestrador de liquidez just-in-time:

1. O usuario escolhe `asset + network`.
2. O backend valida o par permitido.
3. O backend trava quote com taxa ChainFX e spread.
4. O usuario paga via rail fiat.
5. Apos pagamento confirmado, o worker entrega por hot wallet quando houver saldo e suporte.
6. Se hot wallet nao cobrir o par ou o saldo for insuficiente, o Liquidity Router executa via provider.
7. O backend persiste quotes, execucao, provider, status e hash/signature.

O app nao deve manter uma lista fixa como fonte de verdade. A fonte de verdade e o backend via catalogo de pares executaveis.

## Configuracao

Variaveis principais:

```env
LIQUIDITY_ROUTER_ENABLED=true
LIQUIDITY_PROVIDER_URLS=chainfx=https://adapter-url
LIQUIDITY_PROVIDER_API_KEY=<secret>
LIQUIDITY_QUOTE_TIMEOUT_MS=2500
LIQUIDITY_ALLOWED_PAIRS=USDT:BSC:0x55d398326f99059fF775485246999027B3197955:18,USDT:POLYGON:0xc2132D05D31c914a87C6611C10748AEb04B58e8F:6,BTC:BITCOIN::8,SOL:SOLANA::9
LIQUIDITY_ALLOWED_ASSETS=USDT,BTC,BNB,SOL,ETH,LINK,AVAX
LIQUIDITY_ALLOWED_NETWORKS=BSC,POLYGON,BITCOIN,SOLANA
LIQUIDITY_ROUTER_HOT_WALLET_FIRST_ASSETS=USDT
```

Regra critica: o backend libera por par, nao por simbolo isolado.

Correto:

```text
SOL:SOLANA
BTC:BITCOIN
USDT:BSC
USDT:POLYGON
LINK:BSC
```

Incorreto:

```text
SOL permitido genericamente
LINK permitido genericamente
AVAX permitido genericamente
```

O par carrega `asset`, `network`, `contract_address` e `decimals`. Isso impede entregar token certo na rede errada ou aceitar provider com contrato diferente.

## Provider BingX

O BingX route e um provider de inventario cripto, nao um on-ramp PIX. O PIX entra na ChainFX via Efí/banco; a BingX usa saldo spot preexistente da conta ChainFX, normalmente USDT, para comprar o ativo e sacar para o endereco do usuario.

Fluxo:

```text
PIX confirmado
BuySendWorker recebe buy.paid
LiquidityRouter seleciona bingx
BingX market buy: USDT -> ativo
BingX withdraw: ativo -> wallet do usuario
Backend persiste quote, execution, external_order_id, withdrawal_id/tx_hash
```

Variaveis:

```env
BINGX_ENABLED=false
BINGX_API_BASE_URL=https://open-api.bingx.com
BINGX_API_KEY=
BINGX_API_SECRET=
BINGX_RECV_WINDOW_MS=5000
BINGX_ALLOWED_ASSETS=BTC,ETH,BNB,SOL,LINK,AVAX
BINGX_ALLOWED_NETWORKS=BITCOIN,BSC,POLYGON,SOLANA,BASE,ARBITRUM,ETHEREUM
BINGX_TAKER_FEE_BPS=10
BINGX_WITHDRAW_FEE_USDT=0
BINGX_MARKET_BUY_MODE=quantity
BINGX_TRADE_ENABLED=false
BINGX_WITHDRAW_ENABLED=false
```

Ativacao segura:

1. Criar API key na BingX em API Management com permissao Spot Trading.
2. Preencher `BINGX_API_KEY` e `BINGX_API_SECRET` somente em Railway/.env.
3. Subir com `BINGX_ENABLED=true`, `BINGX_TRADE_ENABLED=false`, `BINGX_WITHDRAW_ENABLED=false` para validar quote.
4. Ligar `BINGX_TRADE_ENABLED=true` com valor pequeno e `BINGX_WITHDRAW_ENABLED=false`; a ordem fica `pending_withdrawal` apos compra spot.
5. Para saque automatico, habilitar permissao Withdraw na key, vincular o IP fixo da infra e so entao ligar `BINGX_WITHDRAW_ENABLED=true`.

Probe seguro sem trade/withdraw:

```http
POST /api/admin/liquidity/bingx/probe
Authorization: Bearer <admin-session-ou-sk_live>
X-Admin-Console-Key: <ADMIN_CONSOLE_KEY>
Content-Type: application/json

{"asset":"SOL","network":"SOLANA","cryptoAmount":0.01}
```

Esse probe chama market data publico e endpoints privados assinados de conta/balance. Ele nao chama `trade/order` nem `withdraw/apply`.

## Rotas Publicas BUY

### Catalogo

```http
GET /api/buy/pairs
```

Retorna pares executaveis para frontend web e mobile.

Campos esperados:

```json
{
  "routerEnabled": true,
  "backendEnforced": true,
  "hotWalletFirst": ["USDT"],
  "assets": ["AVAX", "BNB", "BTC", "ETH", "LINK", "SOL", "USDT"],
  "networks": ["BITCOIN", "BSC", "POLYGON", "SOLANA"],
  "pairs": []
}
```

### Quote persistido

```http
POST /api/quote
Content-Type: application/json
```

Exemplo:

```json
{
  "mode": "buy",
  "asset": "SOL",
  "network": "SOLANA",
  "fiatCurrency": "BRL",
  "paymentMethod": "pix",
  "amountFiat": 100
}
```

Resposta esperada:

```json
{
  "quoteId": "qt_...",
  "quotePersisted": true,
  "quoteLockContract": "quoteId+side+asset+network+fiatCurrency+paymentMethod+amountFiat",
  "asset": "SOL",
  "network": "SOLANA",
  "marketRate": 392.7385,
  "rate": 396.6659,
  "feeFiat": 7.49,
  "feeBreakdown": {
    "serviceBps": 550,
    "serviceFee": 5.5,
    "networkFee": 1.99,
    "totalFee": 7.49,
    "rateSpreadBps": 100
  }
}
```

O `rate` deve ser maior que `marketRate` quando ha spread. O lucro da ChainFX entra por:

- `feeFiat`;
- `feeBreakdown.serviceFee`;
- `feeBreakdown.networkFee`, quando aplicavel;
- `rateSpreadBps`;
- diferenca entre cotacao travada ao usuario e execucao real do provider.

### Criacao da compra

```http
POST /api/buy
Content-Type: application/json
```

Campos relevantes:

```json
{
  "quoteId": "qt_...",
  "asset": "SOL",
  "network": "SOLANA",
  "amountFiat": 100,
  "fiatCurrency": "BRL",
  "paymentMethod": "pix",
  "address": "<wallet interna do usuario na rede escolhida>",
  "customer": {
    "name": "Usuario Final",
    "email": "usuario@example.com",
    "cpf": "00000000000",
    "phone": "11999999999"
  }
}
```

O backend consome `quoteId` de forma atomica. Se o cliente trocar `asset`, `network`, `fiatCurrency`, `paymentMethod` ou `amountFiat`, a compra falha com `QUOTE_LOCK_INVALID`.

Os dados de CPF, email, nome e telefone pertencem ao usuario final que comprou no app. Esses dados sao usados no PSP fiat e auditoria antifraude. Eles nao devem ser enviados ao provider cripto salvo exigencia contratual/KYC do provider.

## Fluxo BUY Consolidado

Fluxo completo:

```text
Frontend/Mobile
  -> GET /api/buy/pairs
  -> POST /api/quote
  -> POST /api/buy com quoteId
  -> pagamento PIX/cartao
  -> webhook PSP confirma pagamento
  -> evento buy.paid
  -> BuySendWorker
     -> tenta Liquidity Router quando aplicavel
     -> senao hot wallet USDT:BSC
  -> buy.sent ou buy.pending_confirmation
```

Decisao do `BuySendWorker`:

- `USDT:BSC` com hot wallet suportada e saldo suficiente: envio pela hot wallet.
- `USDT:BSC` com saldo ausente/insuficiente: Liquidity Router.
- `BTC:BITCOIN`, `SOL:SOLANA`, `BNB:BSC`, `ETH:*`, `LINK:*`, `AVAX:*`: Liquidity Router quando o par esta em `LIQUIDITY_ALLOWED_PAIRS` e `LIQUIDITY_ROUTER_ENABLED=true`.
- Se nao houver router executavel nem hot wallet suportada, a order vai para erro controlado, sem cair em settlement BSC indevido.

## Fluxo DCA Consolidado

O DCA usa o mesmo caminho de entrega do BUY:

```text
DCAWorker
  -> seleciona estrategias vencidas com FOR UPDATE SKIP LOCKED
  -> valida asset + network por policy real
  -> busca wallet do usuario pela familia da rede
     - EVM: users.wallet_address
     - BITCOIN: btc_wallet_addresses
     - SOLANA: sol_wallet_addresses
     - APTOS: aptos_wallet_addresses
  -> cria buy_order interna com status pago_fiat
  -> publica buy.paid
  -> BuySendWorker entrega por hot wallet ou Liquidity Router
```

Regra: DCA so pode aceitar par que o BUY consegue executar. Se o par nao tem hot wallet nem router, a estrategia entra em DLQ/erro operacional e nao promete entrega.

## Provider Adapter Contract

Nao usar RPA, scraping ou browser automation para comprar em sites publicos. Providers entram por API oficial ou adapter proprio configurado em `LIQUIDITY_PROVIDER_URLS`.

Cada provider precisa expor:

```http
POST /quote
POST /execute
```

### POST /quote

Recebe `liquidity.Request`:

```json
{
  "OrderID": "buy_order_id",
  "UserID": "user_id",
  "Asset": "SOL",
  "Network": "SOLANA",
  "TokenContract": "",
  "TokenDecimals": 9,
  "FiatCurrency": "BRL",
  "AmountBRL": 100,
  "CryptoAmount": 0.2521,
  "QuoteLockedRate": 396.6659,
  "DestAddress": "<solana_address>",
  "CreatedAt": "2026-07-23T11:22:35Z"
}
```

Retorna `liquidity.Quote`:

```json
{
  "Provider": "transak",
  "ProviderType": "liquidity_provider",
  "ExternalQuoteID": "provider_quote_id",
  "Asset": "SOL",
  "Network": "SOLANA",
  "TokenContract": "",
  "TokenDecimals": 9,
  "DestAddress": "<solana_address>",
  "FiatCostBRL": 100,
  "ProviderFeeBRL": 1.2,
  "NetworkFeeBRL": 0.3,
  "SpreadBRL": 0,
  "TotalCostBRL": 101.5,
  "CryptoAmount": 0.2521,
  "DeliverySLASeconds": 120,
  "ReliabilityBps": 9800,
  "DirectDelivery": true
}
```

### POST /execute

Recebe:

```json
{
  "request": {},
  "quote": {}
}
```

Retorna `liquidity.Execution`:

```json
{
  "Provider": "transak",
  "ExternalOrderID": "provider_order_id",
  "Status": "submitted",
  "TxHash": "signature_or_tx_hash",
  "Asset": "SOL",
  "Network": "SOLANA",
  "TokenContract": "",
  "DestAddress": "<solana_address>",
  "DeliveredAmount": 0.2521
}
```

Status finais como `sent`, `delivered`, `confirmed` ou `settled` marcam a order como `enviado`. Outros status deixam `pendente_confirmacao`.

## Fallback E Ranking

O router consulta providers em paralelo via `QuoteAll`.

Ranking:

```text
score = TotalCostBRL + SLA penalty - reliability discount
```

Depois tenta `ExecuteBest` na ordem ranqueada:

1. Melhor quote liquida.
2. Se o provider nao implementa `/execute`, tenta o proximo.
3. Se `/execute` falha, tenta o proximo.
4. Se a execucao retorna mismatch, rejeita e tenta o proximo.
5. Se todos falham, registra evento `buy.liquidity.fallback` e retorna para hot wallet se houver suporte.

O router rejeita mismatch em:

- asset;
- network;
- token contract;
- decimals;
- destination address;
- delivered amount abaixo da tolerancia.

## Persistencia E Auditoria

Tabelas principais:

- `quotes`: quote publico persistido para `/api/quote`, consumido por `/api/buy`.
- `buy_orders`: ordem de compra e lifecycle fiat/on-chain.
- `buy_order_events`: auditoria por evento.
- `buy_liquidity_quotes`: todas as quotes retornadas pelos providers para uma order.
- `buy_liquidity_executions`: tentativa de execucao, provider, tx hash, erro e payload.
- `dca_strategies`: estrategias DCA com `token_symbol` e `network`.

Migrations relacionadas:

- `035_buy_liquidity_router.sql`: cria quotes/executions de liquidez.
- `036_buy_liquidity_pair_contract.sql`: reforca contrato/decimals no par.
- `038_quotes_network_lock.sql`: adiciona `network` ao quote lock publico.

Indice critico:

```sql
CREATE INDEX IF NOT EXISTS idx_quotes_asset_network_expires
  ON quotes(asset, network, expires_at)
  WHERE consumed_at IS NULL;
```

## Multi-Rail Wallet

Familias suportadas pela policy:

| Familia | Redes | Endereco |
| --- | --- | --- |
| EVM | BSC, Polygon, Base, Arbitrum, Ethereum | Mesmo `0x` do usuario |
| BITCOIN | Bitcoin nativo | Endereco BTC separado |
| SOLANA | Solana | Endereco base58 separado |
| APTOS | Aptos | Endereco Aptos separado |

O mobile deve buscar endereco conforme rede:

- EVM: `/api/mobile/wallet/address`
- Bitcoin: `/api/mobile/btc/address`
- Solana: `/api/mobile/sol/address`
- Aptos: `/api/mobile/aptos/address`

Para BUY via router, o destination address precisa ser da rede escolhida. Exemplo: `SOL:SOLANA` deve usar endereco Solana; `BTC:BITCOIN` deve usar endereco BTC; `USDT:BSC` deve usar endereco EVM.

## Solana

Estado integrado:

- pacote `internal/solana`;
- RPC client com failover por `SOLANA_RPC_URLS`;
- `GET /api/mobile/sol/address`;
- `GET /api/mobile/sol/balance`;
- `GET /api/mobile/sol/fee-estimate`;
- `GET /api/mobile/sol/transactions`;
- `POST /api/mobile/sol/send`;
- scanner de depositos e tracker de withdrawals quando rail configurada;
- DCA prepara endereco Solana para `SOL:SOLANA`;
- BUY via router aceita `SOL:SOLANA` quando o par esta liberado.

Limite atual: SPL tokens em Solana devem entrar em fase propria com Associated Token Account, rent e scanner SPL.

## Cloud Smoke Validado

Comando:

```powershell
cd C:\Users\Paulo\Desktop\payment-gateway
powershell -ExecutionPolicy Bypass -File scripts\liquidity_cloud_probe.ps1 -BaseUrl https://api-production-bc748.up.railway.app -RequirePersistedQuote
```

Resultado validado em 2026-07-23:

- `ok=true`;
- `routerEnabled=true`;
- `pairCount=10`;
- `hasSolana=true`;
- `hasBitcoin=true`;
- `backendEnforced=true`;
- quote `USDT:BSC` com `quoteId`, `quotePersisted=true`, taxa ChainFX e spread;
- quote `SOL:SOLANA` com `quoteId`, `quotePersisted=true`, taxa ChainFX e spread;
- `SOL:BSC` rejeitado com 400.

Esse smoke valida catalogo, quote, persistencia, taxa, spread e rejeicao de par invalido. Ele nao valida dinheiro real nem execucao final do provider, porque `/execute` so roda apos pagamento confirmado.

## Como Validar Provider Real Sem Dinheiro

Antes de usar Ramp, Transak, MoonPay, Alchemy Pay ou OTC real:

1. Subir um adapter sandbox/dry-run.
2. Configurar:

```env
LIQUIDITY_PROVIDER_URLS=sandbox=https://sandbox-adapter-url
LIQUIDITY_ROUTER_ENABLED=true
```

3. Criar quote.
4. Criar buy com `quoteId`.
5. Marcar uma order controlada como paga em ambiente de teste.
6. Confirmar que `buy_liquidity_quotes` gravou todos os providers.
7. Confirmar que `buy_liquidity_executions` gravou o provider executado.
8. Confirmar status final `enviado` ou `pendente_confirmacao`.

## Regras De Producao

- Nunca liberar token so por simbolo.
- Nunca aceitar provider que devolve outra rede, outro contrato ou outro destino.
- Nunca cair automaticamente para BSC quando a compra era `SOLANA`, `BITCOIN`, `POLYGON` ou outra rede.
- Nunca enviar CPF/email/nome para provider cripto se o contrato nao exigir.
- Nunca usar RPA/scraping para compra automatizada.
- Hot wallet primeiro apenas para ativos explicitamente configurados, hoje `USDT`.
- Router deve ser obrigatorio para ativos sem estoque proprio.
- Quote publico deve ser persistido antes da compra.
- Quote deve ser consumido uma unica vez.
- Provider `/execute` so deve rodar depois de pagamento confirmado.

## Proximos Gaps Conhecidos

- Validar `/execute` ponta a ponta com adapter sandbox/dry-run.
- Criar endpoint administrativo para status provider por order.
- Adicionar reconciliacao periodica `GET /status/{external_order_id}` quando o adapter oferecer essa rota.
- Medir latencia por provider e gravar score historico para melhorar ranking.
- Separar `LIQUIDITY_PROVIDER_API_KEY` por provider quando houver multiplos parceiros com chaves diferentes.
- Mover secrets reais para secret manager/KMS.
- Evoluir Solana SPL depois do SOL nativo.
- Aptos ainda deve ser tratado como proxima rail, nao como promessa de producao se signer/indexer nao estiver pronto.
