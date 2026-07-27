# ChainFX Commerce Bitrefill

## Arquitetura

O app mobile nunca chama a Bitrefill diretamente. O fluxo correto e:

Mobile -> ChainFX Mobile API -> Commerce Service -> Bitrefill Provider Adapter -> Bitrefill API.

O contrato publico da ChainFX permanece normalizado em `/api/mobile/commerce/*` e `/api/mobile/gift-cards/*`. IDs, JSON bruto, tokens, invoices e orders especificos do provider ficam encapsulados no adapter.

Referencias oficiais usadas:

- https://docs.bitrefill.com/docs/api-overview
- https://docs.bitrefill.com/docs/integration-flow

## Configuracao

Variaveis:

- `COMMERCE_PROVIDER=bitrefill`
- `BITREFILL_BASE_URL=https://api.bitrefill.com/v2`
- `BITREFILL_API_KEY=`
- `BITREFILL_API_ID=`
- `BITREFILL_API_SECRET=`
- `BITREFILL_TIMEOUT_SECONDS=10`
- `BITREFILL_WEBHOOK_URL=`
- `BITREFILL_WEBHOOK_SECRET=`
- `BITREFILL_DEFAULT_PAYMENT_METHOD=balance`
- `BITREFILL_LIVE_PURCHASES_ENABLED=false`
- `BITREFILL_CATALOG_SYNC_ENABLED=true`
- `BITREFILL_RECONCILIATION_ENABLED=true`

O default de `BITREFILL_LIVE_PURCHASES_ENABLED` deve continuar `false` ate go-live.

## Fluxo De Catalogo

A Bitrefill expoe produtos por `/products`, busca por `/products/search` e detalhe por `/products/{id}`. Produtos podem ter `packages` de valor fixo ou `range` de valor variavel. A ChainFX deve salvar um catalogo local em `gift_card_provider_products` e servir o app a partir do cache local.

O endpoint mobile serve primeiro o cache local e dispara sincronizacao em segundo plano quando habilitado por `BITREFILL_CATALOG_SYNC_ENABLED`. Produtos removidos do provider devem ser desativados no catalogo local, preservando historico.

## Fluxo De Quote

O frontend nao calcula taxa, spread, desconto, cotacao ou total USDT. A API cria uma quote com:

- produto interno;
- quantidade;
- valor BRL em minor units, sem `float64`;
- taxa ChainFX;
- cotacao USDT/BRL;
- total USDT em micros;
- expiracao;
- metodo de pagamento.

Quotes devem ser imutaveis e consumidas uma vez.

## Fluxo De Pedido

Com saldo USDT:

1. Validar quote e KYC.
2. Validar idempotencia.
3. Bloquear saldo interno.
4. Criar `mobile_gift_card_orders`.
5. Inserir `commerce_outbox_events`.
6. Commit.
7. Worker compra no provider fora da transacao.

Quando `BITREFILL_LIVE_PURCHASES_ENABLED=false`, a API rejeita novas compras reais antes de bloquear saldo do usuario.

Com PIX:

1. Criar order `awaiting_payment`.
2. Gerar cobranca PIX ChainFX.
3. Comprar no provider somente depois do webhook bancario confirmar pagamento.

## Ledger

O saldo do usuario e custodial. Compras nao devem movimentar blockchain por pedido. Use lock/capture/release:

- lock: debita `available_usdt_micro` e credita `locked_usdt_micro`;
- capture: remove do locked quando o voucher foi entregue;
- release: devolve available se houver falha definitiva antes da entrega.

Todos os valores internos do fluxo commerce usam inteiros:

- BRL: centavos/minor units;
- USDT: micros;
- percentuais: basis points.

## Bitrefill

Personal API usa Bearer token. Business API usa Basic auth com API ID e Secret. Para revenda e catalogo completo, a Bitrefill indica Business API.

Compras usam invoices. Para produtos fixos, enviar `package_id`; para produtos variaveis, enviar `value`. `auto_pay=true` usa saldo da conta Bitrefill. Timeout nao e falha definitiva: marque como `provider_unknown` e reconcilie.

O worker de compra consome `commerce.purchase.requested`, chama o provider fora da transacao PostgreSQL e grava tentativas em `gift_card_provider_attempts`. Em timeout ou estado ambiguo, o pedido fica `provider_unknown` e deve ser reconciliado antes de qualquer nova tentativa de compra.

## Webhook

O webhook Bitrefill deve persistir evento bruto em `gift_card_webhook_events` e responder rapido. Nao marcar pedido como entregue confiando apenas no payload. A reconciliacao deve consultar invoice/order server-to-server antes de capturar saldo e liberar voucher.

O endpoint atual persiste o evento e deixa a conciliacao fazer a decisao final. Caso a conta Bitrefill nao ofereca assinatura criptografica, use `BITREFILL_WEBHOOK_SECRET` em URL nao previsivel e confirme o estado por chamada server-to-server.

## Entrega

Voucher, PIN e link ficam criptografados em `gift_card_deliveries`. Listagens retornam apenas status e mascaras. O valor completo deve ser entregue apenas ao dono autenticado via endpoint dedicado.

O endpoint `/api/mobile/commerce/orders/{id}/delivery` retorna o voucher completo apenas para o usuario autenticado dono do pedido.

## Limitacoes Pendentes

- Circuit breaker e metricas Prometheus especificas ainda precisam ser plugados na infraestrutura global de observabilidade.
- Cache Redis de catalogo e lock distribuido de sync dependem da camada Redis padrao do deploy.
- Reconciliacao fina por invoice ainda depende dos recursos liberados na conta Bitrefill/Business API.
- PIX para gift cards deve comprar somente apos webhook Efí confirmar o pagamento.
- Testes de concorrencia, refund real e `provider_unknown` com Bitrefill real devem ser feitos em sandbox antes de go-live.

## Go-Live

Checklist:

1. Rotacionar chaves expostas.
2. Confirmar Business API se a operacao for revenda.
3. Sincronizar catalogo BR.
4. Validar produtos de teste.
5. Ativar webhook com segredo forte.
6. Rodar testes de idempotencia, timeout, 429 e saldo baixo.
7. Confirmar saldo operacional Bitrefill.
8. Ativar `BITREFILL_LIVE_PURCHASES_ENABLED=true`.
9. Monitorar provider, ledger e reconciliacao.

## Rollback

Voltar `BITREFILL_LIVE_PURCHASES_ENABLED=false`, pausar worker de compra e manter reconciliacao ligada para pedidos ja criados.
