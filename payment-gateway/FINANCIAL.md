ChainFX — Lucro e Taxas por Serviço e Camada
1. Digital FX Payments — BUY
Serviço

Compra de USDT com PIX ou cartão.

Taxas cobradas
Faixa da compra	Taxa de serviço
Abaixo de R$ 100	7,5%
De R$ 100 até R$ 499,99	5,5%
A partir de R$ 500	4,5%

Além da taxa de serviço:

Componente	Valor
Taxa de rede	R$ 1,99
Taxa mínima total	R$ 4,99
Spread cambial	1% sobre a cotação de mercado
Fórmula
Taxa total explícita =
máximo entre:

taxa percentual + R$ 1,99

ou

R$ 4,99

Além disso, a taxa de câmbio usada na compra recebe markup de 1%.

Taxa de compra = taxa de mercado × 1,01
Exemplo: compra de R$ 500
Taxa de serviço: 4,5% de R$ 500 = R$ 22,50
Taxa de rede: R$ 1,99
Taxa explícita total: R$ 24,49
Spread de 1%: aproximadamente R$ 5,00
Receita bruta estimada da ChainFX
R$ 24,49 em taxas explícitas
+
aproximadamente R$ 5,00 em spread
=
aproximadamente R$ 29,49
Margem bruta estimada
5,90% sobre o subtotal de R$ 500

Esse valor ainda não desconta:

custo real da rede;
custo do provedor PIX;
custo de aquisição do USDT;
gas da blockchain;
custos operacionais.
2. Digital FX Payments — SELL
Serviço

Venda de USDT com recebimento em PIX BRL.

Taxa cobrada

Não existe uma taxa fixa separada. Toda a receita é obtida no spread cambial.

Valor da operação	Spread padrão
Abaixo de R$ 1.000	10%
A partir de R$ 1.000	8%
Fórmula
Taxa de venda =
cotação de mercado × (1 - spread)
Exemplo: venda de 100 USDT

Considerando:

Cotação de mercado: R$ 5,50
Spread: 10%

Cálculo:

Taxa paga ao cliente: R$ 4,95
Valor de mercado: R$ 550,00
PIX enviado: R$ 495,00
Receita bruta de spread: R$ 55,00
Exemplo: venda de 1.000 USDT

Considerando:

Cotação de mercado: R$ 5,50
Spread: 8%

Cálculo:

Valor de mercado: R$ 5.500,00
PIX enviado: R$ 5.060,00
Receita bruta de spread: R$ 440,00
Receita da ChainFX
SELL abaixo de R$ 1.000:
10% do valor de mercado

SELL acima de R$ 1.000:
8% do valor de mercado

Esse spread representa receita bruta. O lucro líquido deve descontar:

custo do PIX;
oscilação cambial;
custo de liquidez;
custos de compliance;
custos de custódia;
risco de execução.
3. Agent Liquidity Rail
Serviço

Troca de stablecoins entre agentes autônomos.

Exemplos:

USDT → USDC
USDC → USDT
Taxa cobrada
6% por execução
Fórmula
Receita ChainFX =
valor da operação × 6%
Exemplos
Volume da operação	Receita ChainFX
10 USDT	0,60 USDT
100 USDT	6 USDT
1.000 USDT	60 USDT
10.000 USDT	600 USDT
Observação

Esse é o valor bruto da taxa. O lucro líquido precisa descontar:

gas;
custo de settlement;
custo de treasury;
slippage;
custo do signer;
eventual custo de liquidez.
4. Marketplace de Capabilities
Serviço

Venda de capabilities ou planos de APIs de provedores terceiros.

Taxa cobrada
Take rate padrão da ChainFX: 20%
Fórmula
Receita ChainFX =
preço do plano × 20%
Repasse ao provider =
preço do plano × 80%
OCR
Preço: 80 USDT
Receita ChainFX: 16 USDT
Repasse ao provider: 64 USDT
GPT Business
Preço: 300 USDT
Receita ChainFX: 60 USDT
Repasse ao provider: 240 USDT
FX Capability
Preço: 400 USDT
Receita ChainFX: 80 USDT
Repasse ao provider: 320 USDT
AML
Preço: 600 USDT
Receita ChainFX: 120 USDT
Repasse ao provider: 480 USDT
Resumo
Capability	Preço	Receita ChainFX	Repasse provider
OCR	80 USDT	16 USDT	64 USDT
GPT Business	300 USDT	60 USDT	240 USDT
FX	400 USDT	80 USDT	320 USDT
AML	600 USDT	120 USDT	480 USDT
5. Capability Router
Serviço

Roteamento e execução de uma capability escolhida por um agente.

Taxa cobrada
6% sobre a execução
Fórmula
Receita ChainFX =
valor pago pelo agente × 6%
Exemplos
Execução	Receita ChainFX
5 USDT	0,30 USDT
25 USDT	1,50 USDT
100 USDT	6 USDT
500 USDT	30 USDT

Essa taxa pode coexistir com o take rate do marketplace apenas quando os contratos comerciais permitirem. Caso contrário, existe risco de dupla cobrança.

6. API Access
Produto: API Credit Basic
Preço
10 USDT
Quota
10.000 requests
Receita bruta
10 USDT por pacote
Receita por request
10 USDT ÷ 10.000
=
0,001 USDT por request
Exemplos
Pacotes vendidos	Requests concedidos	Receita
1	10.000	10 USDT
10	100.000	100 USDT
100	1.000.000	1.000 USDT
Lucro real
Receita líquida =
10 USDT
-
custo de infraestrutura
-
custo de provider
-
custo de processamento
7. MCP Access
Produto: ChainFX MCP Basic
Preço
10 USDT
Quota
10.000 tool calls
Validade
30 dias
Receita bruta
10 USDT por acesso
Receita por tool call
10 USDT ÷ 10.000
=
0,001 USDT por chamada
Exemplos
Acessos vendidos	Tool calls disponíveis	Receita
1	10.000	10 USDT
50	500.000	500 USDT
1.000	10.000.000	10.000 USDT
8. Quota Metering e Usage
Serviço

Cobrança por consumo de requests ou execução de trabalho pago.

No estado atual, o metering controla a quota adquirida. Ele não representa necessariamente uma taxa adicional independente.

Receita atual

A receita nasce na venda do pacote:

API Credit Basic:
10 USDT por 10.000 requests

MCP Basic:
10 USDT por 10.000 tool calls
Monetização futura possível
Modelo	Exemplo
Pay-per-request	0,001 USDT
Pay-per-tool-call	0,001 USDT
Pacote de 1.000 chamadas	1 USDT
Pacote de 100.000 chamadas	100 USDT
Excedente de quota	valor configurável

Não deve ser contabilizado como nova fonte de receita quando apenas debita uma quota já comprada.

Resumo consolidado
Camada	Serviço	Taxa cobrada	Receita ChainFX
Core FX	BUY abaixo de R$ 100	7,5% + R$ 1,99 + 1% spread	Variável
Core FX	BUY de R$ 100 a R$ 499,99	5,5% + R$ 1,99 + 1% spread	Variável
Core FX	BUY a partir de R$ 500	4,5% + R$ 1,99 + 1% spread	Variável
Core FX	SELL abaixo de R$ 1.000	10% spread	10% do valor de mercado
Core FX	SELL a partir de R$ 1.000	8% spread	8% do valor de mercado
Agent Rail	Liquidity trade	6%	6% do volume
Marketplace	Venda de capability	20% take rate	20% do plano
Capability Router	Execução roteada	6%	6% da execução
API Access	10.000 requests	10 USDT	10 USDT
MCP Access	10.000 tool calls	10 USDT	10 USDT
Metering	Consumo de quota	Sem taxa adicional atual	Receita reconhecida na venda do pacote

9. Gas Station / Paymaster
Serviço

Relay de transações EVM com gas patrocinado/abstraído, quote em USDT e split de taxa.

Arquitetura financeira

- `internal/paymaster/estimator.go` estima custo base via RPC e aplica surcharge/floor/cap.
- `internal/paymaster/token_relayer.go` calcula `feeAmount = total * spreadBps / 10000` e `netAmount = total - feeAmount` usando aritmética inteira.
- `gas_relay_requests` persiste status, tentativas, DLQ e chaves de idempotência.
- `auto_sweeper_runs` registra sweeps da hot wallet para auditoria operacional.

Receita

Receita ChainFX =
feeAmount cobrado no relay
-
custo real de gas
-
custo do signer/RPC

Controles

- idempotência por `sig_hash`;
- rate limit por tier (`sk_test_*`, `sk_live_*`);
- retry com exponential backoff e jitter;
- DLQ persistida depois das tentativas;
- k6 em `tests/paymaster_stress.js` para validar spike, colisão de idempotência e SLOs.
