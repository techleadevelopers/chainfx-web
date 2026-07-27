package liquidity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type HTTPProvider struct {
	ProviderName string
	BaseURL      string
	APIKey       string
	Client       *http.Client
}

func (p *HTTPProvider) Name() string {
	return strings.TrimSpace(firstNonEmpty(p.ProviderName, "http_provider"))
}

func (p *HTTPProvider) Quote(ctx context.Context, req Request) (Quote, error) {
	var out Quote
	if err := p.post(ctx, "/quote", req, &out); err != nil {
		return Quote{}, err
	}
	return out, nil
}

func (p *HTTPProvider) Execute(ctx context.Context, req Request, quote Quote) (Execution, error) {
	payload := map[string]any{"request": req, "quote": quote}
	var out Execution
	if err := p.post(ctx, "/execute", payload, &out); err != nil {
		return Execution{}, err
	}
	return out, nil
}

func (p *HTTPProvider) post(ctx context.Context, path string, payload any, out any) error {
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		return fmt.Errorf("liquidity provider %s sem base url", p.Name())
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ChainFX-Liquidity-Provider", p.Name())
	if strings.TrimSpace(p.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(p.APIKey))
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("liquidity provider %s status %d", p.Name(), resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
