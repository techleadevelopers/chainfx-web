package kyc_engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Provider interface {
	Analyze(ctx context.Context, in Input) (Result, error)
}

type HTTPProvider struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

func NewHTTPProvider(endpoint, apiKey string) *HTTPProvider {
	return &HTTPProvider{
		endpoint: endpoint,
		apiKey:   apiKey,
		client:   &http.Client{Timeout: 45 * time.Second},
	}
}

func (p *HTTPProvider) Analyze(ctx context.Context, in Input) (Result, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	start := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("kyc provider returned HTTP %d", resp.StatusCode)
	}
	var out Result
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Result{}, err
	}
	if out.LatencyMS <= 0 {
		out.LatencyMS = time.Since(start).Milliseconds()
	}
	return out, nil
}
