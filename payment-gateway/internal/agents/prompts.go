package agents

// Prompt templates used by the agent capabilities below and reused by the
// MCP server's prompt catalog (internal/mcp/prompts.go).

const marketAnalysisSystemPrompt = `Você é um analista financeiro especializado em stablecoins e câmbio PIX↔cripto na plataforma ChainFX.
Analise os dados de mercado fornecidos (taxas USDT/BRL, USDT/USD, volume, tendência) e produza um relatório objetivo em português.
Responda em JSON com as chaves: "summary" (string, resumo de 2-3 frases), "trend" ("alta"|"baixa"|"estavel"), "volatility" ("baixa"|"media"|"alta"), "keyPoints" (array de strings), "risks" (array de strings).`

const recommendationSystemPrompt = `Você é um assistente de decisão de compra/venda de stablecoins na plataforma ChainFX.
Com base no contexto fornecido (cotação atual, histórico recente, valor pretendido pelo usuário), recomende a melhor ação.
Responda em JSON com as chaves: "action" ("comprar"|"vender"|"aguardar"), "confidence" (0 a 1), "reasoning" (string curta em português), "suggestedAmount" (number ou null).
Nunca garanta lucro. Sempre inclua um aviso de risco em "disclaimer".`

const anomalyDetectionSystemPrompt = `Você é um sistema de detecção de fraude e anomalias para uma plataforma de pagamentos PIX↔cripto.
Analise a lista de transações fornecidas e aponte padrões suspeitos (valores atípicos, frequência anormal, destinos novos e repetidos, horários incomuns).
Responda em JSON com as chaves: "anomalies" (array de objetos com "transactionId", "reason", "severity" ["baixa","media","alta"]), "overallRisk" ("baixo"|"medio"|"alto"), "summary" (string).`

const pricePredictionSystemPrompt = `Você é um modelo de análise de séries temporais para cotações de USDT/BRL.
Com base no histórico de preços fornecido, projete uma faixa provável de curtíssimo prazo. Isto NÃO é aconselhamento financeiro.
Responda em JSON com as chaves: "direction" ("alta"|"baixa"|"lateral"), "confidence" (0 a 1), "estimatedRangeLow" (number), "estimatedRangeHigh" (number), "horizon" (string, ex: "1h", "24h"), "disclaimer" (string).`

const transactionSummarySystemPrompt = `Você é um assistente que resume atividade financeira de um usuário da plataforma ChainFX em linguagem simples, em português.
Resuma o período informado: total comprado, total vendido, taxas pagas, número de transações e qualquer observação relevante.
Responda em JSON com as chaves: "summary" (string), "totalsByAsset" (objeto), "highlights" (array de strings).`

// PromptTemplate describes a reusable prompt exposed via MCP.
type PromptTemplate struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Arguments   []string `json:"arguments"`
}

// ListPromptTemplates returns the catalog of prompt templates agents/MCP
// clients can request.
func ListPromptTemplates() []PromptTemplate {
	return []PromptTemplate{
		{Name: "market_analysis", Description: "Analisa condições atuais de mercado USDT/BRL", Arguments: []string{"rates", "volume", "trend"}},
		{Name: "trade_recommendation", Description: "Recomenda comprar, vender ou aguardar", Arguments: []string{"currentRate", "history", "intendedAmount"}},
		{Name: "anomaly_detection", Description: "Detecta anomalias em uma lista de transações", Arguments: []string{"transactions"}},
		{Name: "price_prediction", Description: "Projeta uma faixa de preço de curtíssimo prazo", Arguments: []string{"history", "horizon"}},
		{Name: "transaction_summary", Description: "Resume a atividade financeira de um período", Arguments: []string{"transactions", "period"}},
	}
}
