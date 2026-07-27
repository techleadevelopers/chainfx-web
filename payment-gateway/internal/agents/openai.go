// Package agents integrates OpenAI-compatible chat models to provide
// AI-assisted capabilities for the platform: market analysis, buy/sell
// recommendations, anomaly detection, price prediction and transaction
// summaries. It also backs LangGraph-style tool calling by exposing plain
// Go functions that the MCP server (internal/mcp) can register as tools.
package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/config"
)

// Client talks to an OpenAI-compatible Chat Completions API.
type Client struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

// NewClient builds a Client from app configuration. Returns nil (safe to
// call methods on it? no) — callers must check Configured() before use.
func NewClient(cfg *config.Config) *Client {
	return &Client{
		apiKey:  cfg.OpenAIAPIKey,
		model:   cfg.OpenAIModel,
		baseURL: strings.TrimRight(cfg.OpenAIBaseURL, "/"),
		http:    &http.Client{Timeout: 45 * time.Second},
	}
}

// Configured reports whether an API key is available.
func (c *Client) Configured() bool {
	return c != nil && strings.TrimSpace(c.apiKey) != ""
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    float64       `json:"temperature,omitempty"`
	ResponseFormat *responseFmt  `json:"response_format,omitempty"`
}

type responseFmt struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// complete sends a single-turn chat completion request and returns the
// assistant's raw text content.
func (c *Client) complete(ctx context.Context, systemPrompt, userPrompt string, jsonMode bool) (string, error) {
	if !c.Configured() {
		return "", fmt.Errorf("OPENAI_API_KEY não configurado")
	}
	body := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.3,
	}
	if jsonMode {
		body.ResponseFormat = &responseFmt{Type: "json_object"}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("resposta inválida da OpenAI: %w", err)
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("erro OpenAI: %s", parsed.Error.Message)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("OpenAI retornou status %d", resp.StatusCode)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("OpenAI não retornou nenhuma resposta")
	}
	return parsed.Choices[0].Message.Content, nil
}

// completeJSON runs complete() in JSON mode and decodes the result into out.
func (c *Client) completeJSON(ctx context.Context, systemPrompt, userPrompt string, out any) error {
	text, err := c.complete(ctx, systemPrompt, userPrompt, true)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(text), out)
}
