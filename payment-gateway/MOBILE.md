# ChainFX Mobile

## Produto

O modulo mobile expoe a API do app ChainFX para usuarios finais comprarem, venderem e acompanharem stablecoins, carteiras, KYC, DCA, swaps, notificacoes e webhooks.

O mobile e a camada humana do gateway:

- compra de cripto com PIX/cartao;
- venda de USDT para PIX;
- carteira BSC do usuario;
- cotacao e catalogo multi-asset;
- KYC e limites por nivel;
- estrategias DCA;
- notificacoes e webhooks do usuario;
- status operacional via health check.

O mobile nao substitui o MCP/agent rail. Ele atende usuario final em app React Native; o MCP atende agentes autonomos e integracoes maquina-maquina.

## Fluxos Principais

### Autenticacao

1. `POST /api/mobile/auth/register`
2. `POST /api/mobile/auth/login`
3. Cliente usa `Authorization: Bearer <accessToken>`
4. `POST /api/mobile/auth/refresh` renova sessao
5. `POST /api/mobile/auth/logout` revoga refresh token salvo

O refresh token e salvo no banco como digest SHA-256 do token completo. Isso evita o limite de 72 bytes do bcrypt em JWTs longos e permite revogacao no logout.

### Compra

1. App chama `POST /api/mobile/order/buy`.
2. Handler mobile encaminha para `/api/buy`.
3. Ordem e associada ao `user_id`.
4. Usuario acompanha em `/api/mobile/order/{id}` ou `/api/mobile/orders`.

### Venda / PIX

1. App chama `POST /api/mobile/order/sell` ou `POST /api/mobile/pix/generate`.
2. Handler encaminha para `/api/order`.
3. On-chain worker monitora deposito USDT.
4. Payout PIX e executado pelo worker principal.

### Carteira

`POST /api/mobile/wallet/generate` agora registra uma wallet EVM criada no cliente/agente:

```json
{ "wallet_address": "0x742d35Cc6634C0532925a3b844Bc454e4438f44e" }
```

O backend valida o endereco, salva o checksum address e nunca gera nem retorna private key.

### KYC

Ha dois fluxos:

- legado: `/api/mobile/user/kyc` e `/api/mobile/user/kyc/status`;
- v2 assincrono: `/api/mobile/kyc/submit`, `/api/mobile/kyc/status`, `/api/mobile/kyc/history`, `/api/mobile/kyc/limits`.

O worker `KYCWorker` consome eventos `kyc.submitted` e aprova niveis 1 e 2 automaticamente; nivel 3 fica em revisao.

## Rotas

### Publicas

- `GET /api/mobile/health`
- `GET /api/mobile/assets`
- `GET /api/mobile/assets/{symbol}`
- `GET /api/mobile/assets/{symbol}/rate`
- `GET /api/mobile/countries`
- `GET /api/mobile/countries/detect`
- `GET /api/mobile/countries/{code}`
- `GET /api/mobile/countries/{code}/rails`
- `GET /api/mobile/webhooks/events`
- `GET /api/mobile/ws/price`

### Autenticadas

- Auth: `POST /api/mobile/auth/logout`
- User: `GET|PUT /api/mobile/user/profile`
- KYC: `POST /api/mobile/user/kyc`, `GET /api/mobile/user/kyc/status`, `POST /api/mobile/kyc/submit`, `GET /api/mobile/kyc/status`, `GET /api/mobile/kyc/history`, `GET /api/mobile/kyc/limits`
- Wallet: `GET /api/mobile/wallet/balance`, `GET /api/mobile/wallet/tokens`, `GET /api/mobile/wallet/address`, `POST /api/mobile/wallet/generate`, `GET /api/mobile/wallet/history`
- Orders: `POST /api/mobile/order/buy`, `POST /api/mobile/order/sell`, `POST /api/mobile/order/swap`, `GET /api/mobile/order/{id}`, `GET /api/mobile/orders`, `POST /api/mobile/order/cancel`
- PIX: `POST /api/mobile/pix/generate`, `GET /api/mobile/pix/status/{id}`, `POST /api/mobile/pix/copy`
- DCA: `POST /api/mobile/dca/create`, `GET /api/mobile/dca/strategies`, `PUT /api/mobile/dca/{id}`, `DELETE /api/mobile/dca/{id}`, `GET /api/mobile/dca/{id}/status`
- Security: `POST /api/mobile/security/pin`, `POST /api/mobile/security/biometry`, `POST /api/mobile/security/2fa`, `GET /api/mobile/security/devices`, `DELETE /api/mobile/security/device`
- Contracts read-only: `GET /api/mobile/contracts/vault`, `GET /api/mobile/contracts/delegate`
- Contracts bloqueados no mobile: `POST /api/mobile/contracts/payout`, `POST /api/mobile/contracts/pause`, `POST /api/mobile/contracts/unpause` retornam `403` ate existir escopo admin/internal.
- Notifications: `GET /api/mobile/notifications`, `PUT /api/mobile/notifications/read`, `DELETE /api/mobile/notifications/{id}`, `POST /api/mobile/notifications/token`
- Swap: `POST /api/mobile/swap/quote`, `POST /api/mobile/swap/execute`, `GET /api/mobile/swap/{id}`, `GET /api/mobile/swaps`
- User webhooks: `POST /api/mobile/webhooks/subscribe`, `GET /api/mobile/webhooks`, `DELETE /api/mobile/webhooks/{id}`, `PUT /api/mobile/webhooks/{id}/toggle`
- WebSocket: `/api/mobile/ws/orders`, `/api/mobile/ws/notifications`

### Provider/Webhook

- `POST /api/mobile/pix/confirm`

Essa rota e publica porque e pensada como webhook de provider. Ela deve permanecer protegida indiretamente pela validacao/HMAC do handler interno `/api/pix/webhook`.

## Idempotencia

As rotas financeiras abaixo usam `Idempotency-Key`:

- `POST /api/mobile/order/buy`
- `POST /api/mobile/order/sell`
- `POST /api/mobile/order/swap`
- `POST /api/mobile/pix/generate`
- `POST /api/mobile/dca/create`
- `POST /api/mobile/swap/execute`

Se o cliente nao enviar a chave, o servidor gera uma e devolve nos headers. Operacoes 2xx sao marcadas como `completed`; falhas sao marcadas como `failed` para permitir retry.

## Configuracao

- `MOBILE_JWT_SECRET`: segredo HS256 do access token, minimo 32 chars.
- `MOBILE_REFRESH_SECRET`: segredo HS256 do refresh token, minimo 32 chars recomendado.
- `MOBILE_JWT_EXPIRES_MIN`: TTL do access token, default 15.
- `MOBILE_REFRESH_EXPIRES_DAYS`: TTL do refresh token, default 7.
- `FCM_SERVER_KEY`: push notification.
- `ALLOWED_ORIGINS`: origens permitidas.

Em producao, o servidor entra em panic se `MOBILE_JWT_SECRET` ou `MOBILE_REFRESH_SECRET` estiver usando valores default.

Antes de deployar as rotas financeiras com idempotencia, aplique `migrations/009_mobile_operation_ids.sql` na base cloud. Sem essa tabela, o middleware falha fechado com `503`.

## Auditoria Tecnica

### Corrigido neste modulo

- `auth/refresh`: refresh token nao usa mais bcrypt no JWT inteiro; handlers tambem retornam erro se a persistencia da sessao falhar.
- `wallet/generate`: backend nao gera nem retorna private key; apenas registra endereco EVM client-side.
- `contracts/payout`, `contracts/pause`, `contracts/unpause`: bloqueados para JWT mobile comum com `403`.
- `settings`: `GET`, `PUT` e `settings/limits` usam persistencia via tabela `settings`.
- `idempotency`: middleware agora finaliza status `completed`/`failed` e foi aplicado nas rotas financeiras mobile.
- `schema`: `operation_ids` foi adicionado em `schema_mobile_base.sql` e na migracao `009_mobile_operation_ids.sql`.

### Riscos Restantes

- Criar autenticacao admin/internal propria para mutacoes de contrato, em namespace fora do app mobile.
- Adicionar testes Go especificos para auth refresh, wallet registration, settings persistence e idempotency replay.
- Reexecutar E2E na URL cloud apos deploy dessa versao.

## Testes em Producao

Base testada anteriormente:

```text
https://stablecoin-payment-gateway-production-3ee2.up.railway.app
```

Resultado observado em 2026-07-13 antes deste patch:

- `GET /api/mobile/health`: `200`, ok.
- `GET /api/mobile/assets`: `200`, retornou assets.
- `GET /api/mobile/user/profile` sem token: `401`, ok.
- `POST /api/mobile/auth/register`: `201`, criou usuario e tokens.
- `GET /api/mobile/user/profile` com token: `200`.
- `GET /api/mobile/wallet/address`: `200`.
- `GET /api/mobile/settings`: `200`, ainda mock antes do patch.
- `GET /api/mobile/kyc/limits`: `200`.
- `POST /api/mobile/swap/quote`: `200`.
- `POST /api/mobile/security/pin` com PIN curto: `400`.
- `POST /api/mobile/auth/refresh`: `401`, bug confirmado antes do patch.
- `POST /api/mobile/auth/logout`: `200`.

Depois do deploy deste patch, `auth/refresh` deve retornar `200` para token recem emitido e `wallet/generate` deve exigir `wallet_address`.
