package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"payment-gateway/internal/security"
)

type signerProbeRequest struct {
	Samples   int `json:"samples"`
	TimeoutMS int `json:"timeoutMs"`
}

type signerProbeSample struct {
	Area      string         `json:"area"`
	Endpoint  string         `json:"endpoint"`
	Method    string         `json:"method"`
	Status    int            `json:"status"`
	OK        bool           `json:"ok"`
	Expected  []int          `json:"expected"`
	LatencyMS int64          `json:"latencyMs"`
	ErrorCode string         `json:"errorCode,omitempty"`
	Error     string         `json:"error,omitempty"`
	Hint      string         `json:"hint,omitempty"`
	Response  map[string]any `json:"response,omitempty"`
	At        string         `json:"at"`
}

type signerProbeEndpointSummary struct {
	Area      string  `json:"area"`
	Endpoint  string  `json:"endpoint"`
	Count     int     `json:"count"`
	OK        int     `json:"ok"`
	Errors    int     `json:"errors"`
	Available float64 `json:"availability"`
	P50       int64   `json:"p50"`
	P55       int64   `json:"p55"`
	P95       int64   `json:"p95"`
	P99       int64   `json:"p99"`
	Avg       int64   `json:"avg"`
	Max       int64   `json:"max"`
	LastError string  `json:"lastError,omitempty"`
}

type signerProbeSpec struct {
	area     string
	method   string
	path     string
	body     []byte
	signed   bool
	expected []int
}

type signerProbeDiagnosis struct {
	Status           string `json:"status"`
	Code             string `json:"code,omitempty"`
	Message          string `json:"message,omitempty"`
	Hint             string `json:"hint,omitempty"`
	Action           string `json:"action,omitempty"`
	Target           string `json:"target"`
	TargetKind       string `json:"targetKind"`
	Host             string `json:"host,omitempty"`
	PreflightBlocked bool   `json:"preflightBlocked"`
}

func (s *Server) handleAdminSignerProbe(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	var req signerProbeRequest
	if r.Body != nil {
		_ = decodeJSON(r, &req)
	}
	if req.Samples <= 0 {
		req.Samples = 3
	}
	if req.Samples > 25 {
		req.Samples = 25
	}
	if req.TimeoutMS <= 0 {
		req.TimeoutMS = 3500
	}
	if req.TimeoutMS > 15000 {
		req.TimeoutMS = 15000
	}

	signerURL := strings.TrimRight(strings.TrimSpace(s.cfg.SignerUrl), "/")
	if signerURL == "" {
		diagnosis := signerProbeDiagnosis{
			Status:  "misconfigured",
			Code:    "signer_url_missing",
			Message: "SIGNER_URL nao configurado no gateway",
			Hint:    "Configure SIGNER_URL no servico gateway/API.",
			Action:  "Use a URL privada do signer quando ambos os servicos estiverem no mesmo projeto/ambiente Railway.",
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          false,
			"configured":  false,
			"generatedAt": time.Now().UTC().Format(time.RFC3339Nano),
			"error":       diagnosis.Message,
			"diagnosis":   diagnosis,
		})
		return
	}

	specs := []signerProbeSpec{
		{area: "Availability", method: http.MethodGet, path: "/healthz", expected: []int{http.StatusOK}},
		{area: "Readiness", method: http.MethodGet, path: "/readyz", expected: []int{http.StatusOK}},
		{area: "Security", method: http.MethodPost, path: "/hd/transfer", body: []byte(`{}`), expected: []int{http.StatusUnauthorized}},
		{area: "Security", method: http.MethodPost, path: "/hd/contract-call", body: []byte(`{}`), expected: []int{http.StatusUnauthorized}},
	}
	if strings.TrimSpace(s.cfg.SignerHmacSecret) != "" {
		specs = append(specs, signerProbeSpec{
			area:     "Signed path",
			method:   http.MethodPost,
			path:     "/hd/transfer",
			body:     []byte(`{`),
			signed:   true,
			expected: []int{http.StatusBadRequest},
		})
	}

	client := &http.Client{Timeout: time.Duration(req.TimeoutMS) * time.Millisecond}
	diagnosis := preflightSignerTarget(r.Context(), signerURL, client.Timeout)
	if diagnosis.PreflightBlocked {
		samples := signerBlockedSamples(specs, diagnosis)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          false,
			"configured":  true,
			"generatedAt": time.Now().UTC().Format(time.RFC3339Nano),
			"target":      maskSignerTarget(signerURL),
			"diagnosis":   diagnosis,
			"samples":     samples,
			"summary":     summarizeSignerProbeSamples(samples),
			"overall":     summarizeSignerProbeEndpoint("overall", "all", samples),
		})
		return
	}

	samples := make([]signerProbeSample, 0, req.Samples*len(specs))
	for i := 0; i < req.Samples; i++ {
		for _, spec := range specs {
			samples = append(samples, s.runSignerProbe(r.Context(), client, signerURL, spec))
		}
	}

	summary := summarizeSignerProbeSamples(samples)
	ok := len(samples) > 0
	for _, sample := range samples {
		if !sample.OK {
			ok = false
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          ok,
		"configured":  true,
		"generatedAt": time.Now().UTC().Format(time.RFC3339Nano),
		"target":      maskSignerTarget(signerURL),
		"diagnosis":   diagnoseSignerSamples(signerURL, samples),
		"samples":     samples,
		"summary":     summary,
		"overall":     summarizeSignerProbeEndpoint("overall", "all", samples),
	})
}

func (s *Server) runSignerProbe(parent context.Context, client *http.Client, baseURL string, spec signerProbeSpec) signerProbeSample {
	ctx, cancel := context.WithTimeout(parent, client.Timeout)
	defer cancel()
	started := time.Now()
	sample := signerProbeSample{
		Area:     spec.area,
		Endpoint: spec.path,
		Method:   spec.method,
		Expected: spec.expected,
		At:       started.UTC().Format(time.RFC3339Nano),
	}
	req, err := http.NewRequestWithContext(ctx, spec.method, baseURL+spec.path, bytes.NewReader(spec.body))
	if err != nil {
		sample.ErrorCode = "request_build_failed"
		sample.Error = err.Error()
		sample.Hint = "Verifique o formato de SIGNER_URL."
		sample.LatencyMS = time.Since(started).Milliseconds()
		return sample
	}
	req.Header.Set("Accept", "application/json")
	if len(spec.body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if spec.signed {
		security.SignRawBodyHeaders(req, s.cfg.SignerHmacSecret, spec.body)
	}

	resp, err := client.Do(req)
	sample.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		code, message, hint := classifySignerProbeError(baseURL, err)
		sample.ErrorCode = code
		sample.Error = message
		sample.Hint = hint
		return sample
	}
	defer resp.Body.Close()
	sample.Status = resp.StatusCode
	sample.OK = statusIn(resp.StatusCode, spec.expected)
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if len(raw) > 0 {
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err == nil {
			sample.Response = decoded
		} else {
			sample.Response = map[string]any{"raw": string(raw)}
		}
	}
	if !sample.OK {
		sample.ErrorCode = "unexpected_status"
		sample.Error = fmt.Sprintf("unexpected status %d", resp.StatusCode)
		sample.Hint = "A rota respondeu, mas com status diferente do esperado para este probe."
	}
	return sample
}

func preflightSignerTarget(ctx context.Context, rawURL string, timeout time.Duration) signerProbeDiagnosis {
	diagnosis := signerProbeDiagnosis{
		Status:     "ready",
		Target:     maskSignerTarget(rawURL),
		TargetKind: signerTargetKind(rawURL),
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		diagnosis.Status = "misconfigured"
		diagnosis.Code = "invalid_signer_url"
		diagnosis.Message = "SIGNER_URL invalido"
		diagnosis.Hint = "Use uma URL completa, por exemplo http://signer.railway.internal:4010."
		diagnosis.Action = "Corrija SIGNER_URL no servico gateway/API e redeploy."
		diagnosis.PreflightBlocked = true
		return diagnosis
	}
	host := parsed.Hostname()
	diagnosis.Host = host
	if timeout <= 0 || timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := net.DefaultResolver.LookupHost(lookupCtx, host); err != nil {
		code, message, hint := classifySignerProbeError(rawURL, err)
		diagnosis.Status = "unreachable"
		diagnosis.Code = code
		diagnosis.Message = message
		diagnosis.Hint = hint
		diagnosis.Action = signerProbeAction(code, host)
		diagnosis.PreflightBlocked = true
		return diagnosis
	}
	return diagnosis
}

func signerBlockedSamples(specs []signerProbeSpec, diagnosis signerProbeDiagnosis) []signerProbeSample {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	samples := make([]signerProbeSample, 0, len(specs))
	for _, spec := range specs {
		samples = append(samples, signerProbeSample{
			Area:      spec.area,
			Endpoint:  spec.path,
			Method:    spec.method,
			Status:    0,
			OK:        false,
			Expected:  spec.expected,
			ErrorCode: diagnosis.Code,
			Error:     diagnosis.Message,
			Hint:      diagnosis.Hint,
			At:        now,
		})
	}
	return samples
}

func diagnoseSignerSamples(rawURL string, samples []signerProbeSample) signerProbeDiagnosis {
	diagnosis := signerProbeDiagnosis{
		Status:     "ready",
		Target:     maskSignerTarget(rawURL),
		TargetKind: signerTargetKind(rawURL),
	}
	if parsed, err := url.Parse(rawURL); err == nil {
		diagnosis.Host = parsed.Hostname()
	}
	for _, sample := range samples {
		if sample.OK {
			continue
		}
		diagnosis.Status = "degraded"
		diagnosis.Code = sample.ErrorCode
		diagnosis.Message = sample.Error
		diagnosis.Hint = sample.Hint
		diagnosis.Action = signerProbeAction(sample.ErrorCode, diagnosis.Host)
		return diagnosis
	}
	return diagnosis
}

func classifySignerProbeError(rawURL string, err error) (string, string, string) {
	if err == nil {
		return "", "", ""
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) || strings.Contains(strings.ToLower(err.Error()), "no such host") {
		host := signerHost(rawURL)
		return "dns_not_found",
			fmt.Sprintf("DNS do signer nao resolveu: %s", defaultString(host, "host desconhecido")),
			"O gateway esta tentando um hostname privado que nao existe neste projeto/ambiente Railway."
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return "timeout", "Timeout ao conectar no signer", "O signer pode estar lento, nao iniciado, ou bloqueado por rede/porta."
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "connection refused") || strings.Contains(lower, "connect: refused") {
		return "connection_refused", "Conexao recusada pelo signer", "O DNS resolveu, mas nada esta escutando na porta configurada."
	}
	return "request_failed", err.Error(), "Verifique SIGNER_URL, rede privada Railway e logs do signer."
}

func signerProbeAction(code, host string) string {
	switch code {
	case "dns_not_found":
		return fmt.Sprintf("No gateway/API, ajuste SIGNER_URL para http://<nome-exato-do-servico-signer>.railway.internal:4010. Host atual: %s.", defaultString(host, "indefinido"))
	case "connection_refused":
		return "Confirme PORT=4010 no signer e que o processo esta escutando em 0.0.0.0 ou no bind esperado pelo Railway."
	case "timeout":
		return "Verifique se gateway e signer estao no mesmo projeto e ambiente Railway, e confira logs/startup do signer."
	case "invalid_signer_url":
		return "Corrija SIGNER_URL no gateway/API e faca redeploy."
	default:
		return "Confira SIGNER_URL, SIGNER_HMAC_SECRET e logs do servico signer."
	}
}

func summarizeSignerProbeSamples(samples []signerProbeSample) []signerProbeEndpointSummary {
	groups := map[string][]signerProbeSample{}
	for _, sample := range samples {
		key := sample.Area + "|" + sample.Method + " " + sample.Endpoint
		groups[key] = append(groups[key], sample)
	}
	out := make([]signerProbeEndpointSummary, 0, len(groups))
	for key, group := range groups {
		parts := strings.SplitN(key, "|", 2)
		out = append(out, summarizeSignerProbeEndpoint(parts[0], parts[1], group))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].P95 == out[j].P95 {
			return out[i].Endpoint < out[j].Endpoint
		}
		return out[i].P95 > out[j].P95
	})
	return out
}

func summarizeSignerProbeEndpoint(area, endpoint string, samples []signerProbeSample) signerProbeEndpointSummary {
	latencies := make([]int64, 0, len(samples))
	ok := 0
	lastError := ""
	for _, sample := range samples {
		latencies = append(latencies, sample.LatencyMS)
		if sample.OK {
			ok++
		} else if sample.Error != "" {
			lastError = sample.Error
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	sum := int64(0)
	for _, latency := range latencies {
		sum += latency
	}
	avg := int64(0)
	if len(latencies) > 0 {
		avg = sum / int64(len(latencies))
	}
	availability := 0.0
	if len(samples) > 0 {
		availability = (float64(ok) / float64(len(samples))) * 100
	}
	return signerProbeEndpointSummary{
		Area:      area,
		Endpoint:  endpoint,
		Count:     len(samples),
		OK:        ok,
		Errors:    len(samples) - ok,
		Available: availability,
		P50:       signerProbePercentile(latencies, 50),
		P55:       signerProbePercentile(latencies, 55),
		P95:       signerProbePercentile(latencies, 95),
		P99:       signerProbePercentile(latencies, 99),
		Avg:       avg,
		Max:       signerProbePercentile(latencies, 100),
		LastError: lastError,
	}
}

func signerProbePercentile(sortedValues []int64, p int) int64 {
	if len(sortedValues) == 0 {
		return 0
	}
	if p >= 100 {
		return sortedValues[len(sortedValues)-1]
	}
	idx := ((p * len(sortedValues)) + 99) / 100
	if idx <= 0 {
		idx = 1
	}
	if idx > len(sortedValues) {
		idx = len(sortedValues)
	}
	return sortedValues[idx-1]
}

func statusIn(status int, expected []int) bool {
	for _, value := range expected {
		if status == value {
			return true
		}
	}
	return false
}

func maskSignerTarget(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "railway.internal") || strings.Contains(raw, ".internal") {
		return "private signer"
	}
	return raw
}

func signerHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func signerTargetKind(raw string) string {
	host := strings.ToLower(signerHost(raw))
	switch {
	case host == "":
		return "unknown"
	case strings.Contains(host, "railway.internal") || strings.HasSuffix(host, ".internal"):
		return "private"
	case strings.Contains(host, "up.railway.app"):
		return "public_railway"
	default:
		return "public"
	}
}
