package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type config struct {
	APIBase        string
	Secret         string
	Count          int
	Concurrency    int
	Mode           string
	BuyIDsFile     string
	CreateBuy      bool
	AmountBRL      float64
	Address        string
	Timeout        time.Duration
	PollInterval   time.Duration
	ProviderPrefix string
	OutputJSON     string
	OutputCSV      string
}

type sample struct {
	Index        int     `json:"index"`
	BuyID        string  `json:"buyId"`
	ProviderID   string  `json:"providerId"`
	WebhookAckMS float64 `json:"webhookAckMs"`
	DeliveryMS   float64 `json:"deliveryMs,omitempty"`
	FinalStatus  string  `json:"finalStatus,omitempty"`
	HTTPStatus   int     `json:"httpStatus"`
	Error        string  `json:"error,omitempty"`
	Duplicate    bool    `json:"duplicate,omitempty"`
	StartedAt    string  `json:"startedAt"`
	CompletedAt  string  `json:"completedAt"`
}

type summary struct {
	Count       int                `json:"count"`
	Errors      int                `json:"errors"`
	Concurrency int                `json:"concurrency"`
	Mode        string             `json:"mode"`
	StartedAt   string             `json:"startedAt"`
	FinishedAt  string             `json:"finishedAt"`
	WebhookAck  percentileSummary  `json:"webhookAckMs"`
	Delivery    *percentileSummary `json:"deliveryMs,omitempty"`
	Samples     []sample           `json:"samples"`
}

type percentileSummary struct {
	Min float64 `json:"min"`
	P50 float64 `json:"p50"`
	P55 float64 `json:"p55"`
	P90 float64 `json:"p90"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
	Avg float64 `json:"avg"`
}

func main() {
	cfg := parseFlags()
	if err := validateConfig(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	client := &http.Client{Timeout: 30 * time.Second}
	started := time.Now()
	buyIDs, err := loadBuyIDs(ctx, client, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup error:", err)
		os.Exit(1)
	}
	if len(buyIDs) < cfg.Count {
		fmt.Fprintf(os.Stderr, "setup error: need %d buy ids, got %d\n", cfg.Count, len(buyIDs))
		os.Exit(1)
	}

	jobs := make(chan int)
	results := make(chan sample, cfg.Count)
	var seq atomic.Int64
	var wg sync.WaitGroup
	for worker := 0; worker < cfg.Concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results <- runOne(ctx, client, cfg, i, buyIDs[i], seq.Add(1))
			}
		}()
	}
	for i := 0; i < cfg.Count; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(results)

	var samples []sample
	for result := range results {
		samples = append(samples, result)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].Index < samples[j].Index })

	report := buildSummary(cfg, started, time.Now(), samples)
	printSummary(report)
	if cfg.OutputJSON != "" {
		if err := writeJSON(cfg.OutputJSON, report); err != nil {
			fmt.Fprintln(os.Stderr, "json output error:", err)
			os.Exit(1)
		}
	}
	if cfg.OutputCSV != "" {
		if err := writeCSV(cfg.OutputCSV, samples); err != nil {
			fmt.Fprintln(os.Stderr, "csv output error:", err)
			os.Exit(1)
		}
	}
	if report.Errors > 0 {
		os.Exit(1)
	}
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.APIBase, "api", "http://localhost:3000", "base URL da API")
	flag.StringVar(&cfg.Secret, "secret", os.Getenv("PIX_WEBHOOK_SECRET"), "segredo HMAC do webhook PIX")
	flag.IntVar(&cfg.Count, "count", 50, "numero de webhooks")
	flag.IntVar(&cfg.Concurrency, "concurrency", 8, "concorrencia")
	flag.StringVar(&cfg.Mode, "mode", "ack", "ack ou e2e")
	flag.StringVar(&cfg.BuyIDsFile, "buy-ids", "", "arquivo com buy IDs, um por linha")
	flag.BoolVar(&cfg.CreateBuy, "create-buy", false, "cria buy orders antes de disparar webhooks")
	flag.Float64Var(&cfg.AmountBRL, "amount-brl", 150, "valor BRL usado com --create-buy")
	flag.StringVar(&cfg.Address, "address", "", "wallet BSC destino usada com --create-buy")
	flag.DurationVar(&cfg.Timeout, "timeout", 2*time.Minute, "timeout total")
	flag.DurationVar(&cfg.PollInterval, "poll", 250*time.Millisecond, "intervalo de polling no modo e2e")
	flag.StringVar(&cfg.ProviderPrefix, "provider-prefix", "bench-pix", "prefixo de providerId")
	flag.StringVar(&cfg.OutputJSON, "json", "", "arquivo JSON de saida")
	flag.StringVar(&cfg.OutputCSV, "csv", "", "arquivo CSV de saida")
	flag.Parse()
	cfg.APIBase = strings.TrimRight(cfg.APIBase, "/")
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	return cfg
}

func validateConfig(cfg config) error {
	if cfg.Secret == "" {
		return fmt.Errorf("-secret ou PIX_WEBHOOK_SECRET e obrigatorio")
	}
	if cfg.Count <= 0 {
		return fmt.Errorf("-count deve ser maior que zero")
	}
	if cfg.Concurrency <= 0 {
		return fmt.Errorf("-concurrency deve ser maior que zero")
	}
	if cfg.Mode != "ack" && cfg.Mode != "e2e" {
		return fmt.Errorf("-mode deve ser ack ou e2e")
	}
	if cfg.CreateBuy && strings.TrimSpace(cfg.Address) == "" {
		return fmt.Errorf("-address e obrigatorio com --create-buy")
	}
	if !cfg.CreateBuy && cfg.BuyIDsFile == "" {
		return fmt.Errorf("use --buy-ids ou --create-buy")
	}
	return nil
}

func loadBuyIDs(ctx context.Context, client *http.Client, cfg config) ([]string, error) {
	if cfg.CreateBuy {
		ids := make([]string, 0, cfg.Count)
		for i := 0; i < cfg.Count; i++ {
			id, err := createBuy(ctx, client, cfg)
			if err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return ids, nil
	}
	raw, err := os.ReadFile(cfg.BuyIDsFile)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			ids = append(ids, line)
		}
	}
	return ids, nil
}

func createBuy(ctx context.Context, client *http.Client, cfg config) (string, error) {
	payload := map[string]any{
		"amountBRL":     cfg.AmountBRL,
		"asset":         "USDT",
		"address":       cfg.Address,
		"paymentMethod": "pix",
		"fiatCurrency":  "BRL",
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.APIBase+"/api/buy", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("create buy status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		ID    string `json:"id"`
		BuyID string `json:"buyId"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.BuyID != "" {
		return out.BuyID, nil
	}
	if out.ID != "" {
		return out.ID, nil
	}
	return "", fmt.Errorf("create buy response without id")
}

func runOne(ctx context.Context, client *http.Client, cfg config, index int, buyID string, seq int64) sample {
	started := time.Now()
	providerID := fmt.Sprintf("%s-%d-%d", cfg.ProviderPrefix, time.Now().UnixNano(), seq)
	result := sample{Index: index, BuyID: buyID, ProviderID: providerID, StartedAt: started.Format(time.RFC3339Nano)}

	ackMS, status, duplicate, err := sendWebhook(ctx, client, cfg, buyID, providerID)
	result.WebhookAckMS = ackMS
	result.HTTPStatus = status
	result.Duplicate = duplicate
	if err != nil {
		result.Error = err.Error()
		result.CompletedAt = time.Now().Format(time.RFC3339Nano)
		return result
	}
	if cfg.Mode == "e2e" {
		deliveryMS, finalStatus, err := waitDelivery(ctx, client, cfg, buyID, started)
		result.DeliveryMS = deliveryMS
		result.FinalStatus = finalStatus
		if err != nil {
			result.Error = err.Error()
		}
	}
	result.CompletedAt = time.Now().Format(time.RFC3339Nano)
	return result
}

func sendWebhook(ctx context.Context, client *http.Client, cfg config, buyID, providerID string) (float64, int, bool, error) {
	payload := map[string]string{
		"buyId":      buyID,
		"status":     "concluido",
		"providerId": providerID,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.APIBase+"/api/pix/webhook/buy", bytes.NewReader(body))
	if err != nil {
		return 0, 0, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-pagbank-signature", signHMAC(cfg.Secret, body))
	start := time.Now()
	resp, err := client.Do(req)
	elapsed := float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		return elapsed, 0, false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	duplicate, _ := decoded["duplicate"].(bool)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return elapsed, resp.StatusCode, duplicate, fmt.Errorf("webhook status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return elapsed, resp.StatusCode, duplicate, nil
}

func waitDelivery(ctx context.Context, client *http.Client, cfg config, buyID string, started time.Time) (float64, string, error) {
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return float64(time.Since(started).Microseconds()) / 1000, "", ctx.Err()
		case <-ticker.C:
			status, err := getBuyStatus(ctx, client, cfg, buyID)
			if err != nil {
				return float64(time.Since(started).Microseconds()) / 1000, status, err
			}
			switch status {
			case "enviado", "delivered", "confirmado":
				return float64(time.Since(started).Microseconds()) / 1000, status, nil
			case "erro":
				return float64(time.Since(started).Microseconds()) / 1000, status, fmt.Errorf("buy order entered erro")
			}
		}
	}
}

func getBuyStatus(ctx context.Context, client *http.Client, cfg config, buyID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.APIBase+"/api/buy/"+buyID, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("get buy status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.Status, nil
}

func signHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func buildSummary(cfg config, started, finished time.Time, samples []sample) summary {
	errors := 0
	var ackValues []float64
	var deliveryValues []float64
	for _, s := range samples {
		if s.Error != "" {
			errors++
		}
		if s.WebhookAckMS > 0 {
			ackValues = append(ackValues, s.WebhookAckMS)
		}
		if s.DeliveryMS > 0 {
			deliveryValues = append(deliveryValues, s.DeliveryMS)
		}
	}
	report := summary{
		Count:       len(samples),
		Errors:      errors,
		Concurrency: cfg.Concurrency,
		Mode:        cfg.Mode,
		StartedAt:   started.Format(time.RFC3339Nano),
		FinishedAt:  finished.Format(time.RFC3339Nano),
		WebhookAck:  summarize(ackValues),
		Samples:     samples,
	}
	if len(deliveryValues) > 0 {
		delivery := summarize(deliveryValues)
		report.Delivery = &delivery
	}
	return report
}

func summarize(values []float64) percentileSummary {
	if len(values) == 0 {
		return percentileSummary{}
	}
	sort.Float64s(values)
	total := 0.0
	for _, value := range values {
		total += value
	}
	return percentileSummary{
		Min: values[0],
		P50: percentile(values, 0.50),
		P55: percentile(values, 0.55),
		P90: percentile(values, 0.90),
		P95: percentile(values, 0.95),
		P99: percentile(values, 0.99),
		Max: values[len(values)-1],
		Avg: total / float64(len(values)),
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := p * float64(len(sorted)-1)
	low := int(math.Floor(rank))
	high := int(math.Ceil(rank))
	if low == high {
		return sorted[low]
	}
	weight := rank - float64(low)
	return sorted[low]*(1-weight) + sorted[high]*weight
}

func printSummary(report summary) {
	fmt.Printf("flow=%s count=%d concurrency=%d errors=%d\n", report.Mode, report.Count, report.Concurrency, report.Errors)
	fmt.Printf("webhook_ack_ms min=%.2f p50=%.2f p55=%.2f p90=%.2f p95=%.2f p99=%.2f max=%.2f avg=%.2f\n",
		report.WebhookAck.Min, report.WebhookAck.P50, report.WebhookAck.P55, report.WebhookAck.P90, report.WebhookAck.P95, report.WebhookAck.P99, report.WebhookAck.Max, report.WebhookAck.Avg)
	if report.Delivery != nil {
		fmt.Printf("delivery_ms min=%.2f p50=%.2f p55=%.2f p90=%.2f p95=%.2f p99=%.2f max=%.2f avg=%.2f\n",
			report.Delivery.Min, report.Delivery.P50, report.Delivery.P55, report.Delivery.P90, report.Delivery.P95, report.Delivery.P99, report.Delivery.Max, report.Delivery.Avg)
	}
}

func writeJSON(path string, report summary) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0600)
}

func writeCSV(path string, samples []sample) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{"index", "buy_id", "provider_id", "webhook_ack_ms", "delivery_ms", "final_status", "http_status", "duplicate", "error", "started_at", "completed_at"}); err != nil {
		return err
	}
	for _, s := range samples {
		row := []string{
			fmt.Sprintf("%d", s.Index),
			s.BuyID,
			s.ProviderID,
			fmt.Sprintf("%.3f", s.WebhookAckMS),
			fmt.Sprintf("%.3f", s.DeliveryMS),
			s.FinalStatus,
			fmt.Sprintf("%d", s.HTTPStatus),
			fmt.Sprintf("%t", s.Duplicate),
			s.Error,
			s.StartedAt,
			s.CompletedAt,
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	return writer.Error()
}
