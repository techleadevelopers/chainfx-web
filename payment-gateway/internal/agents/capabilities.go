package agents

import (
	"context"
	"encoding/json"
	"fmt"
)

// MarketAnalysis is the structured output of AnalyzeMarket.
type MarketAnalysis struct {
	Summary    string   `json:"summary"`
	Trend      string   `json:"trend"`
	Volatility string   `json:"volatility"`
	KeyPoints  []string `json:"keyPoints"`
	Risks      []string `json:"risks"`
}

// GenerateText backs the llm_chat capability router with an OpenAI-compatible
// provider when OPENAI_API_KEY is configured.
func (c *Client) GenerateText(ctx context.Context, operation string, input map[string]any) (map[string]any, error) {
	raw, _ := json.Marshal(input)
	system := "You are ChainFX llm_chat, a provider-routed capability for AI agents. Return a concise, useful response for the requested operation."
	prompt := fmt.Sprintf("Operation: %s\nInput JSON: %s", operation, string(raw))
	text, err := c.complete(ctx, system, prompt, false)
	if err != nil {
		return nil, fmt.Errorf("llm_chat provider falhou: %w", err)
	}
	return map[string]any{
		"mode":      "real",
		"provider":  "openai_compatible",
		"operation": operation,
		"text":      text,
	}, nil
}

// AnalyzeMarket produces an AI market analysis from current rate data.
func (c *Client) AnalyzeMarket(ctx context.Context, marketData map[string]any) (*MarketAnalysis, error) {
	raw, _ := json.Marshal(marketData)
	var out MarketAnalysis
	if err := c.completeJSON(ctx, marketAnalysisSystemPrompt, string(raw), &out); err != nil {
		return nil, fmt.Errorf("análise de mercado falhou: %w", err)
	}
	return &out, nil
}

// Recommendation is the structured output of Recommend.
type Recommendation struct {
	Action          string   `json:"action"`
	Confidence      float64  `json:"confidence"`
	Reasoning       string   `json:"reasoning"`
	SuggestedAmount *float64 `json:"suggestedAmount"`
	Disclaimer      string   `json:"disclaimer"`
}

// Recommend advises buy/sell/wait based on the provided context.
func (c *Client) Recommend(ctx context.Context, tradeContext map[string]any) (*Recommendation, error) {
	raw, _ := json.Marshal(tradeContext)
	var out Recommendation
	if err := c.completeJSON(ctx, recommendationSystemPrompt, string(raw), &out); err != nil {
		return nil, fmt.Errorf("recomendação falhou: %w", err)
	}
	return &out, nil
}

// Anomaly describes a single suspicious transaction.
type Anomaly struct {
	TransactionID string `json:"transactionId"`
	Reason        string `json:"reason"`
	Severity      string `json:"severity"`
}

// AnomalyReport is the structured output of DetectAnomalies.
type AnomalyReport struct {
	Anomalies   []Anomaly `json:"anomalies"`
	OverallRisk string    `json:"overallRisk"`
	Summary     string    `json:"summary"`
}

// DetectAnomalies scans a list of transactions for suspicious patterns.
func (c *Client) DetectAnomalies(ctx context.Context, transactions []map[string]any) (*AnomalyReport, error) {
	raw, _ := json.Marshal(map[string]any{"transactions": transactions})
	var out AnomalyReport
	if err := c.completeJSON(ctx, anomalyDetectionSystemPrompt, string(raw), &out); err != nil {
		return nil, fmt.Errorf("detecção de anomalias falhou: %w", err)
	}
	return &out, nil
}

// PricePrediction is the structured output of PredictPrice.
type PricePrediction struct {
	Direction          string  `json:"direction"`
	Confidence         float64 `json:"confidence"`
	EstimatedRangeLow  float64 `json:"estimatedRangeLow"`
	EstimatedRangeHigh float64 `json:"estimatedRangeHigh"`
	Horizon            string  `json:"horizon"`
	Disclaimer         string  `json:"disclaimer"`
}

// PredictPrice projects a short-term price range from recent history.
func (c *Client) PredictPrice(ctx context.Context, history []map[string]any, horizon string) (*PricePrediction, error) {
	raw, _ := json.Marshal(map[string]any{"history": history, "horizon": horizon})
	var out PricePrediction
	if err := c.completeJSON(ctx, pricePredictionSystemPrompt, string(raw), &out); err != nil {
		return nil, fmt.Errorf("previsão de preço falhou: %w", err)
	}
	return &out, nil
}

// TransactionSummary is the structured output of SummarizeTransactions.
type TransactionSummary struct {
	Summary       string             `json:"summary"`
	TotalsByAsset map[string]float64 `json:"totalsByAsset"`
	Highlights    []string           `json:"highlights"`
}

// SummarizeTransactions produces a plain-language summary of a user's
// recent activity.
func (c *Client) SummarizeTransactions(ctx context.Context, transactions []map[string]any, period string) (*TransactionSummary, error) {
	raw, _ := json.Marshal(map[string]any{"transactions": transactions, "period": period})
	var out TransactionSummary
	if err := c.completeJSON(ctx, transactionSummarySystemPrompt, string(raw), &out); err != nil {
		return nil, fmt.Errorf("resumo de transações falhou: %w", err)
	}
	return &out, nil
}
