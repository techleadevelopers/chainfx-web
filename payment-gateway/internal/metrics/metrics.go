// Package metrics provides lightweight Prometheus-compatible instrumentation
// for the ChainFX gateway. It uses only the Go standard library — no external
// dependencies — and exposes metrics in the Prometheus text exposition format
// at GET /metrics (protected by admin bearer auth in production).
//
// Metric catalogue:
//
//	chainfx_m2m_overpayment_total         counter  On-chain M2M deposits exceeding required USDT
//	chainfx_m2m_overpayment_usdt_total    counter  Cumulative excess USDT amount received
//	chainfx_mcp_tools_call_total          counter  Total /mcp/tools/call requests (labels: status)
//	chainfx_mcp_rate_limited_total        counter  Requests rejected by the per-key rate limiter
//	chainfx_penalty_box_active_bans             gauge    Active temporary bans in the local penalty box
//	chainfx_penalty_box_bans_total              counter  Temporary bans created by the penalty box
//	chainfx_penalty_box_violations_total        counter  Rate-limit violations counted by the penalty box
//	chainfx_penalty_box_escalations_total       counter  Bans after the first offense
//	chainfx_penalty_box_blocked_requests_total  counter  Requests blocked before handlers by the penalty box
//	chainfx_webhook_delivery_failure_total      counter  Webhook delivery attempts that failed
//	chainfx_onchain_confirmations_floor         gauge    Minimum confirmation floor per network (informational)
package metrics

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─── Global registry ──────────────────────────────────────────────────────────

var global = newRegistry()

// ─── Public API ───────────────────────────────────────────────────────────────

// IncOverpayment records one overpayment event and accumulates the excess USDT.
// intentID is stored in a short audit log (last 1 000 events, capped by size).
func IncOverpayment(intentID string, excessUSDT float64) {
	global.overpaymentTotal.Add(1)
	// accumulate as micro-USDT to avoid float arithmetic on an atomic
	global.overpaymentUSDTMicro.Add(int64(excessUSDT * 1_000_000))
	global.appendOverpaymentEvent(intentID, excessUSDT)
}

// IncMCPToolCall records one completed /mcp/tools/call execution.
// status is "ok" or "error".
func IncMCPToolCall(status string) {
	global.mcpToolCallTotal.Add(1)
	if status == "error" {
		global.mcpToolCallErrors.Add(1)
	}
}

// IncMCPRateLimited records one request rejected by the per-key rate limiter.
func IncMCPRateLimited() {
	global.mcpRateLimitedTotal.Add(1)
}

// SetPenaltyBoxActiveBans records the local number of active temporary bans.
func SetPenaltyBoxActiveBans(count int) {
	if count < 0 {
		count = 0
	}
	global.penaltyBoxActiveBans.Store(int64(count))
}

// IncPenaltyBoxViolation records one rate-limit violation counted by the penalty box.
func IncPenaltyBoxViolation() {
	global.penaltyBoxViolationsTotal.Add(1)
}

// IncPenaltyBoxBan records one temporary ban created by the penalty box.
func IncPenaltyBoxBan() {
	global.penaltyBoxBansTotal.Add(1)
}

// IncPenaltyBoxEscalation records a ban after the first offense.
func IncPenaltyBoxEscalation() {
	global.penaltyBoxEscalationsTotal.Add(1)
}

// IncPenaltyBoxBlockedRequest records one request blocked before application handlers.
func IncPenaltyBoxBlockedRequest() {
	global.penaltyBoxBlockedRequestsTotal.Add(1)
}

// IncWebhookFailure records one webhook delivery failure.
func IncWebhookFailure() {
	global.webhookFailureTotal.Add(1)
}

func IncNFCLiquidityRejection() {
	global.nfcLiquidityRejections.Add(1)
}

// RecordNFCAuthorization increments the per-outcome counter for an NFC
// authorization result. status must be one of: "approved", "declined",
// "requires_funding". Unknown values increment nfcAuthDeclined.
func RecordNFCAuthorization(status string) {
	switch status {
	case "approved":
		global.nfcAuthApproved.Add(1)
	case "requires_funding":
		global.nfcAuthRequiresFunding.Add(1)
		global.nfcAuthDeclined.Add(1)
	default:
		global.nfcAuthDeclined.Add(1)
	}
}

// IncNFCIdempotencyReplay records one NFC authorize request that was served
// from the idempotency cache (duplicate tap, same key, same payload).
func IncNFCIdempotencyReplay() {
	global.nfcIdempotencyReplays.Add(1)
}

// IncNFCCapture records one successful NFC capture operation.
func IncNFCCapture() {
	global.nfcCaptureTotal.Add(1)
}

// IncNFCReverse records one successful NFC reversal operation.
func IncNFCReverse() {
	global.nfcReverseTotal.Add(1)
}

// ── Gas Station ───────────────────────────────────────────────────────────────

// IncPaymasterRelay records one successfully submitted relay request.
func IncPaymasterRelay() {
	global.paymasterRelayTotal.Add(1)
}

// IncPaymasterRelayError records one relay that entered the DLQ.
func IncPaymasterRelayError() {
	global.paymasterRelayErrors.Add(1)
}

// IncPaymasterFeeUsdt accumulates the USDT fee earned on a relay.
func IncPaymasterFeeUsdt(feeUsdt float64) {
	global.paymasterFeeUsdtMicro.Add(int64(feeUsdt * 1_000_000))
}

// ── Auto-Sweeper ──────────────────────────────────────────────────────────────

// IncAutoSweeperRun records one Auto-Sweeper tick.
func IncAutoSweeperRun() {
	global.autosweeperRunsTotal.Add(1)
}

// IncAutoSweeperError records one failed Auto-Sweeper execution.
func IncAutoSweeperError() {
	global.autosweeperErrors.Add(1)
}

// IncAutoSweeperSwept accumulates the USDT amount moved in a successful sweep.
func IncAutoSweeperSwept(usdtAmount float64) {
	global.autosweeperSweptMicro.Add(int64(usdtAmount * 1_000_000))
}

// SetOnchainConfirmationFloor records the effective floor per network (informational).
func SetOnchainConfirmationFloor(network string, floor uint64) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.onchainFloors[strings.ToUpper(network)] = floor
}

type NFCSettlementSnapshot struct {
	Counts                     map[string]int64
	AnomalyCounts              map[string]int64
	QueueAgeSeconds            float64
	SubmitLatencySeconds       float64
	ConfirmationLatencySeconds float64
	EndToEndSeconds            float64
	TreasurySnapshotAgeSeconds float64
	EfiBalanceBRL              float64
	EfiPendingBRL              float64
	EfiSubmittedBRL            float64
	EfiReservedBRL             float64
	EfiMinBufferBRL            float64
	EfiAvailableRealBRL        float64
	ReconciliationLastSuccess  float64
	ReconciliationDuration     float64
}

func ObserveHTTPRequest(method, route string, status int, duration time.Duration) {
	global.observeHTTP(method, route, status, duration)
}

func ObserveHTTPStage(method, route, stage string, duration time.Duration) {
	global.observeHTTPStage(method, route, stage, duration)
}

func IncInternalHTTPLoopback(source, target string) {
	global.incInternalHTTPLoopback(source, target)
}

func RoutePattern(method, path, muxPattern string) string {
	muxPattern = strings.TrimSpace(muxPattern)
	if muxPattern != "" {
		if i := strings.IndexByte(muxPattern, ' '); i >= 0 {
			return strings.TrimSpace(muxPattern[i+1:])
		}
		return muxPattern
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "unknown"
	}
	if path == "/" {
		return "/"
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if dynamicRouteSegment(part) {
			parts[i] = "{id}"
		}
	}
	return "/" + strings.Join(parts, "/")
}

func SetNFCSettlementSnapshot(snapshot NFCSettlementSnapshot) {
	global.mu.Lock()
	defer global.mu.Unlock()
	if snapshot.Counts == nil {
		snapshot.Counts = map[string]int64{}
	}
	if snapshot.AnomalyCounts == nil {
		snapshot.AnomalyCounts = map[string]int64{}
	}
	global.nfcSettlement = snapshot
}

// Handler returns an HTTP handler that serves Prometheus text format metrics.
// Wire it at GET /metrics behind your admin auth middleware.
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(global.render()))
	}
}

// ─── Registry ─────────────────────────────────────────────────────────────────

type registry struct {
	// Counters (thread-safe via sync/atomic)
	overpaymentTotal       atomic.Int64
	overpaymentUSDTMicro   atomic.Int64 // micro-USDT (×1_000_000)
	mcpToolCallTotal       atomic.Int64
	mcpToolCallErrors      atomic.Int64
	mcpRateLimitedTotal    atomic.Int64
	webhookFailureTotal    atomic.Int64
	nfcLiquidityRejections atomic.Int64
	nfcAuthApproved        atomic.Int64 // authorizations that held balance
	nfcAuthDeclined        atomic.Int64 // authorizations declined (any reason)
	nfcAuthRequiresFunding atomic.Int64 // authorizations declined for low balance
	nfcIdempotencyReplays  atomic.Int64 // authorize requests answered from idempotency cache
	nfcCaptureTotal        atomic.Int64
	nfcReverseTotal        atomic.Int64

	penaltyBoxActiveBans           atomic.Int64
	penaltyBoxBansTotal            atomic.Int64
	penaltyBoxViolationsTotal      atomic.Int64
	penaltyBoxEscalationsTotal     atomic.Int64
	penaltyBoxBlockedRequestsTotal atomic.Int64

	// Gas Station (Paymaster) counters
	paymasterRelayTotal   atomic.Int64
	paymasterRelayErrors  atomic.Int64
	paymasterFeeUsdtMicro atomic.Int64 // cumulative fee in micro-USDT

	// Auto-Sweeper counters
	autosweeperRunsTotal  atomic.Int64
	autosweeperErrors     atomic.Int64
	autosweeperSweptMicro atomic.Int64 // cumulative swept USDT in micro-USDT

	// Structured state (protected by mu)
	mu            sync.RWMutex
	nfcSettlement NFCSettlementSnapshot
	onchainFloors map[string]uint64     // network → min confirmations
	opLog         []overpaymentLogEntry // ring buffer, max 1 000 entries
	httpDurations map[httpMetricKey]*histogram
	httpStages    map[httpStageMetricKey]*histogram
	loopbacks     map[loopbackMetricKey]uint64
	startedAt     time.Time
}

type httpMetricKey struct {
	Method string
	Route  string
	Status string
}

type httpStageMetricKey struct {
	Method string
	Route  string
	Stage  string
}

type loopbackMetricKey struct {
	Source string
	Target string
}

type histogram struct {
	Buckets []uint64
	Count   uint64
	Sum     float64
}

var httpDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type overpaymentLogEntry struct {
	IntentID   string
	ExcessUSDT float64
	At         time.Time
}

func newRegistry() *registry {
	return &registry{
		onchainFloors: make(map[string]uint64),
		httpDurations: make(map[httpMetricKey]*histogram),
		httpStages:    make(map[httpStageMetricKey]*histogram),
		loopbacks:     make(map[loopbackMetricKey]uint64),
		startedAt:     time.Now(),
	}
}

func (reg *registry) observeHTTP(method, route string, status int, duration time.Duration) {
	method = strings.ToUpper(strings.TrimSpace(method))
	route = sanitizeMetricLabel(route, "unknown")
	statusClass := "unknown"
	if status > 0 {
		statusClass = fmt.Sprintf("%dxx", status/100)
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	key := httpMetricKey{Method: method, Route: route, Status: statusClass}
	h := reg.httpDurations[key]
	if h == nil {
		h = &histogram{Buckets: make([]uint64, len(httpDurationBuckets))}
		reg.httpDurations[key] = h
	}
	h.observe(duration.Seconds())
}

func (reg *registry) observeHTTPStage(method, route, stage string, duration time.Duration) {
	method = strings.ToUpper(strings.TrimSpace(method))
	route = sanitizeMetricLabel(route, "unknown")
	stage = sanitizeMetricLabel(stage, "unknown")
	reg.mu.Lock()
	defer reg.mu.Unlock()
	key := httpStageMetricKey{Method: method, Route: route, Stage: stage}
	h := reg.httpStages[key]
	if h == nil {
		h = &histogram{Buckets: make([]uint64, len(httpDurationBuckets))}
		reg.httpStages[key] = h
	}
	h.observe(duration.Seconds())
}

func (reg *registry) incInternalHTTPLoopback(source, target string) {
	source = sanitizeMetricLabel(source, "unknown")
	target = sanitizeMetricLabel(target, "unknown")
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.loopbacks[loopbackMetricKey{Source: source, Target: target}]++
}

func (h *histogram) observe(seconds float64) {
	for i, bucket := range httpDurationBuckets {
		if seconds <= bucket {
			h.Buckets[i]++
		}
	}
	h.Count++
	h.Sum += seconds
}

func (h *histogram) clone() histogram {
	if h == nil {
		return histogram{}
	}
	out := histogram{Count: h.Count, Sum: h.Sum}
	out.Buckets = append([]uint64(nil), h.Buckets...)
	return out
}

func sanitizeMetricLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if len(value) > 160 {
		return value[:160]
	}
	return value
}

func dynamicRouteSegment(segment string) bool {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return false
	}
	lower := strings.ToLower(segment)
	if strings.HasPrefix(lower, "0x") && len(segment) == 42 {
		return true
	}
	if len(segment) >= 18 {
		return true
	}
	digits := 0
	hexish := 0
	for _, r := range segment {
		if r >= '0' && r <= '9' {
			digits++
			hexish++
			continue
		}
		if (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || r == '-' || r == '_' {
			hexish++
		}
	}
	return digits == len(segment) || (len(segment) >= 8 && hexish == len(segment))
}

func writeHistogram(b *strings.Builder, name string, labels map[string]string, h histogram) {
	for i, bucket := range httpDurationBuckets {
		labels["le"] = fmt.Sprintf("%g", bucket)
		fmt.Fprintf(b, "%s_bucket%s %d\n", name, formatLabels(labels), h.Buckets[i])
	}
	labels["le"] = "+Inf"
	fmt.Fprintf(b, "%s_bucket%s %d\n", name, formatLabels(labels), h.Count)
	delete(labels, "le")
	fmt.Fprintf(b, "%s_sum%s %.6f\n", name, formatLabels(labels), h.Sum)
	fmt.Fprintf(b, "%s_count%s %d\n", name, formatLabels(labels), h.Count)
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	order := []string{"method", "route", "status", "stage", "source", "target", "le"}
	parts := make([]string, 0, len(labels))
	seen := make(map[string]bool, len(labels))
	for _, key := range order {
		value, ok := labels[key]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf(`%s=%q`, key, value))
		seen[key] = true
	}
	for key, value := range labels {
		if seen[key] {
			continue
		}
		parts = append(parts, fmt.Sprintf(`%s=%q`, key, value))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (reg *registry) appendOverpaymentEvent(intentID string, excess float64) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	const maxLog = 1000
	if len(reg.opLog) >= maxLog {
		// drop oldest
		reg.opLog = reg.opLog[1:]
	}
	reg.opLog = append(reg.opLog, overpaymentLogEntry{
		IntentID:   intentID,
		ExcessUSDT: excess,
		At:         time.Now().UTC(),
	})
}

// render produces a Prometheus text-format snapshot.
func (reg *registry) render() string {
	reg.mu.RLock()
	floors := make(map[string]uint64, len(reg.onchainFloors))
	for k, v := range reg.onchainFloors {
		floors[k] = v
	}
	nfcSettlement := reg.nfcSettlement
	nfcCounts := make(map[string]int64, len(nfcSettlement.Counts))
	for k, v := range nfcSettlement.Counts {
		nfcCounts[k] = v
	}
	nfcAnomalies := make(map[string]int64, len(nfcSettlement.AnomalyCounts))
	for k, v := range nfcSettlement.AnomalyCounts {
		nfcAnomalies[k] = v
	}
	httpDurations := make(map[httpMetricKey]histogram, len(reg.httpDurations))
	for k, v := range reg.httpDurations {
		httpDurations[k] = v.clone()
	}
	httpStages := make(map[httpStageMetricKey]histogram, len(reg.httpStages))
	for k, v := range reg.httpStages {
		httpStages[k] = v.clone()
	}
	loopbacks := make(map[loopbackMetricKey]uint64, len(reg.loopbacks))
	for k, v := range reg.loopbacks {
		loopbacks[k] = v
	}
	reg.mu.RUnlock()

	uptimeSec := time.Since(reg.startedAt).Seconds()

	var b strings.Builder

	b.WriteString("# HELP chainfx_http_request_duration_seconds HTTP request duration by route, method and status class.\n")
	b.WriteString("# TYPE chainfx_http_request_duration_seconds histogram\n")
	for key, h := range httpDurations {
		writeHistogram(&b, "chainfx_http_request_duration_seconds", map[string]string{
			"method": key.Method,
			"route":  key.Route,
			"status": key.Status,
		}, h)
	}

	b.WriteString("# HELP chainfx_http_stage_duration_seconds HTTP handler stage duration by route and method.\n")
	b.WriteString("# TYPE chainfx_http_stage_duration_seconds histogram\n")
	for key, h := range httpStages {
		writeHistogram(&b, "chainfx_http_stage_duration_seconds", map[string]string{
			"method": key.Method,
			"route":  key.Route,
			"stage":  key.Stage,
		}, h)
	}

	b.WriteString("# HELP chainfx_internal_http_loopback_total Internal HTTP loopback requests still used by adapter surfaces.\n")
	b.WriteString("# TYPE chainfx_internal_http_loopback_total counter\n")
	for key, count := range loopbacks {
		fmt.Fprintf(&b, "chainfx_internal_http_loopback_total%s %d\n", formatLabels(map[string]string{
			"source": key.Source,
			"target": key.Target,
		}), count)
	}

	// ── chainfx_m2m_overpayment_total ──────────────────────────────────────
	b.WriteString("# HELP chainfx_m2m_overpayment_total Total on-chain deposits exceeding the required M2M intent amount.\n")
	b.WriteString("# TYPE chainfx_m2m_overpayment_total counter\n")
	fmt.Fprintf(&b, "chainfx_m2m_overpayment_total %d\n", reg.overpaymentTotal.Load())

	b.WriteString("# HELP chainfx_m2m_overpayment_usdt_total Cumulative excess USDT received across all overpayments.\n")
	b.WriteString("# TYPE chainfx_m2m_overpayment_usdt_total counter\n")
	microUsdt := reg.overpaymentUSDTMicro.Load()
	fmt.Fprintf(&b, "chainfx_m2m_overpayment_usdt_total %.6f\n", float64(microUsdt)/1_000_000)

	// ── chainfx_mcp_tools_call_total ───────────────────────────────────────
	b.WriteString("# HELP chainfx_mcp_tools_call_total Total /mcp/tools/call requests processed.\n")
	b.WriteString("# TYPE chainfx_mcp_tools_call_total counter\n")
	total := reg.mcpToolCallTotal.Load()
	errors := reg.mcpToolCallErrors.Load()
	fmt.Fprintf(&b, "chainfx_mcp_tools_call_total{status=\"ok\"} %d\n", total-errors)
	fmt.Fprintf(&b, "chainfx_mcp_tools_call_total{status=\"error\"} %d\n", errors)

	// ── chainfx_mcp_rate_limited_total ─────────────────────────────────────
	b.WriteString("# HELP chainfx_mcp_rate_limited_total Requests rejected by the MCP per-API-key rate limiter.\n")
	b.WriteString("# TYPE chainfx_mcp_rate_limited_total counter\n")
	fmt.Fprintf(&b, "chainfx_mcp_rate_limited_total %d\n", reg.mcpRateLimitedTotal.Load())

	b.WriteString("# HELP chainfx_penalty_box_active_bans Active temporary bans in this API instance.\n")
	b.WriteString("# TYPE chainfx_penalty_box_active_bans gauge\n")
	fmt.Fprintf(&b, "chainfx_penalty_box_active_bans %d\n", reg.penaltyBoxActiveBans.Load())

	b.WriteString("# HELP chainfx_penalty_box_bans_total Temporary bans created by the penalty box.\n")
	b.WriteString("# TYPE chainfx_penalty_box_bans_total counter\n")
	fmt.Fprintf(&b, "chainfx_penalty_box_bans_total %d\n", reg.penaltyBoxBansTotal.Load())

	b.WriteString("# HELP chainfx_penalty_box_violations_total Rate-limit violations counted by the penalty box.\n")
	b.WriteString("# TYPE chainfx_penalty_box_violations_total counter\n")
	fmt.Fprintf(&b, "chainfx_penalty_box_violations_total %d\n", reg.penaltyBoxViolationsTotal.Load())

	b.WriteString("# HELP chainfx_penalty_box_escalations_total Bans after the first offense.\n")
	b.WriteString("# TYPE chainfx_penalty_box_escalations_total counter\n")
	fmt.Fprintf(&b, "chainfx_penalty_box_escalations_total %d\n", reg.penaltyBoxEscalationsTotal.Load())

	b.WriteString("# HELP chainfx_penalty_box_blocked_requests_total Requests blocked before application handlers by the penalty box.\n")
	b.WriteString("# TYPE chainfx_penalty_box_blocked_requests_total counter\n")
	fmt.Fprintf(&b, "chainfx_penalty_box_blocked_requests_total %d\n", reg.penaltyBoxBlockedRequestsTotal.Load())

	// ── chainfx_webhook_delivery_failure_total ─────────────────────────────
	b.WriteString("# HELP chainfx_webhook_delivery_failure_total Webhook HTTP delivery attempts that returned a non-2xx status or timed out.\n")
	b.WriteString("# TYPE chainfx_webhook_delivery_failure_total counter\n")
	fmt.Fprintf(&b, "chainfx_webhook_delivery_failure_total %d\n", reg.webhookFailureTotal.Load())

	// ── chainfx_onchain_confirmation_floor ─────────────────────────────────
	b.WriteString("# HELP chainfx_onchain_confirmation_floor Effective minimum block confirmation floor per network (safety override).\n")
	b.WriteString("# TYPE chainfx_onchain_confirmation_floor gauge\n")
	for network, floor := range floors {
		fmt.Fprintf(&b, "chainfx_onchain_confirmation_floor{network=%q} %d\n", network, floor)
	}

	// ── chainfx_paymaster_relay_total ──────────────────────────────────────
	b.WriteString("# HELP nfc_settlement_status_total Current NFC merchant settlements by status.\n")
	b.WriteString("# TYPE nfc_settlement_status_total gauge\n")
	for _, status := range []string{"PENDING", "SUBMITTED", "SUBMISSION_UNKNOWN", "CONFIRMED", "REJECTED", "MANUAL_REVIEW"} {
		fmt.Fprintf(&b, "nfc_settlement_%s_total %d\n", strings.ToLower(status), nfcCounts[status])
	}
	fmt.Fprintf(&b, "nfc_settlement_unknown_total %d\n", nfcCounts["SUBMISSION_UNKNOWN"])
	b.WriteString("# HELP nfc_settlement_queue_age_seconds Age in seconds of the oldest active NFC settlement queue item.\n")
	b.WriteString("# TYPE nfc_settlement_queue_age_seconds gauge\n")
	fmt.Fprintf(&b, "nfc_settlement_queue_age_seconds %.2f\n", nfcSettlement.QueueAgeSeconds)
	b.WriteString("# HELP nfc_settlement_submit_latency_seconds Average seconds from settlement creation to provider submission.\n")
	b.WriteString("# TYPE nfc_settlement_submit_latency_seconds gauge\n")
	fmt.Fprintf(&b, "nfc_settlement_submit_latency_seconds %.2f\n", nfcSettlement.SubmitLatencySeconds)
	b.WriteString("# HELP nfc_settlement_confirmation_latency_seconds Average seconds from provider submission to confirmation.\n")
	b.WriteString("# TYPE nfc_settlement_confirmation_latency_seconds gauge\n")
	fmt.Fprintf(&b, "nfc_settlement_confirmation_latency_seconds %.2f\n", nfcSettlement.ConfirmationLatencySeconds)
	b.WriteString("# HELP nfc_settlement_end_to_end_seconds Average seconds from settlement creation to confirmation.\n")
	b.WriteString("# TYPE nfc_settlement_end_to_end_seconds gauge\n")
	fmt.Fprintf(&b, "nfc_settlement_end_to_end_seconds %.2f\n", nfcSettlement.EndToEndSeconds)
	b.WriteString("# HELP nfc_treasury_snapshot_age_seconds Age in seconds of the latest persisted NFC treasury snapshot.\n")
	b.WriteString("# TYPE nfc_treasury_snapshot_age_seconds gauge\n")
	fmt.Fprintf(&b, "nfc_treasury_snapshot_age_seconds %.2f\n", nfcSettlement.TreasurySnapshotAgeSeconds)
	b.WriteString("# HELP nfc_treasury_effective_available_brl Effective BRL available for new NFC authorizations after reservations, outflow and buffer.\n")
	b.WriteString("# TYPE nfc_treasury_effective_available_brl gauge\n")
	fmt.Fprintf(&b, "nfc_treasury_effective_available_brl %.2f\n", nfcSettlement.EfiAvailableRealBRL)
	b.WriteString("# HELP nfc_treasury_reserved_brl Active BRL liquidity reservations created by NFC authorizations.\n")
	b.WriteString("# TYPE nfc_treasury_reserved_brl gauge\n")
	fmt.Fprintf(&b, "nfc_treasury_reserved_brl %.2f\n", nfcSettlement.EfiReservedBRL)
	b.WriteString("# HELP nfc_treasury_liquidity_rejections_total NFC authorizations rejected by BRL treasury liquidity policy.\n")
	b.WriteString("# TYPE nfc_treasury_liquidity_rejections_total counter\n")
	fmt.Fprintf(&b, "nfc_treasury_liquidity_rejections_total %d\n", reg.nfcLiquidityRejections.Load())
	b.WriteString("# HELP nfc_authorization_approved_total NFC authorizations where a balance hold was created.\n")
	b.WriteString("# TYPE nfc_authorization_approved_total counter\n")
	fmt.Fprintf(&b, "nfc_authorization_approved_total %d\n", reg.nfcAuthApproved.Load())
	b.WriteString("# HELP nfc_authorization_declined_total NFC authorizations declined for any reason (includes requires_funding).\n")
	b.WriteString("# TYPE nfc_authorization_declined_total counter\n")
	fmt.Fprintf(&b, "nfc_authorization_declined_total %d\n", reg.nfcAuthDeclined.Load())
	b.WriteString("# HELP nfc_authorization_requires_funding_total NFC authorizations declined due to insufficient USDT balance.\n")
	b.WriteString("# TYPE nfc_authorization_requires_funding_total counter\n")
	fmt.Fprintf(&b, "nfc_authorization_requires_funding_total %d\n", reg.nfcAuthRequiresFunding.Load())
	b.WriteString("# HELP nfc_idempotency_replay_total NFC authorize requests answered from the idempotency cache (duplicate tap).\n")
	b.WriteString("# TYPE nfc_idempotency_replay_total counter\n")
	fmt.Fprintf(&b, "nfc_idempotency_replay_total %d\n", reg.nfcIdempotencyReplays.Load())
	b.WriteString("# HELP nfc_capture_total Successful NFC capture operations.\n")
	b.WriteString("# TYPE nfc_capture_total counter\n")
	fmt.Fprintf(&b, "nfc_capture_total %d\n", reg.nfcCaptureTotal.Load())
	b.WriteString("# HELP nfc_reverse_total Successful NFC reversal operations.\n")
	b.WriteString("# TYPE nfc_reverse_total counter\n")
	fmt.Fprintf(&b, "nfc_reverse_total %d\n", reg.nfcReverseTotal.Load())
	b.WriteString("# HELP nfc_settlement_anomalies_total Current open NFC settlement reconciliation anomalies by type.\n")
	b.WriteString("# TYPE nfc_settlement_anomalies_total gauge\n")
	for anomalyType, count := range nfcAnomalies {
		fmt.Fprintf(&b, "nfc_settlement_anomalies_total{type=%q} %d\n", anomalyType, count)
	}
	b.WriteString("# HELP nfc_reconciliation_last_success_timestamp Unix timestamp of the last successful NFC settlement reconciliation run.\n")
	b.WriteString("# TYPE nfc_reconciliation_last_success_timestamp gauge\n")
	fmt.Fprintf(&b, "nfc_reconciliation_last_success_timestamp %.0f\n", nfcSettlement.ReconciliationLastSuccess)
	b.WriteString("# HELP nfc_reconciliation_duration_seconds Duration of the last successful NFC settlement reconciliation run.\n")
	b.WriteString("# TYPE nfc_reconciliation_duration_seconds gauge\n")
	fmt.Fprintf(&b, "nfc_reconciliation_duration_seconds %.3f\n", nfcSettlement.ReconciliationDuration)
	b.WriteString("# HELP nfc_efi_treasury_brl Operational Efi treasury BRL snapshot.\n")
	b.WriteString("# TYPE nfc_efi_treasury_brl gauge\n")
	fmt.Fprintf(&b, "nfc_efi_treasury_brl{kind=\"balance\"} %.2f\n", nfcSettlement.EfiBalanceBRL)
	fmt.Fprintf(&b, "nfc_efi_treasury_brl{kind=\"pending\"} %.2f\n", nfcSettlement.EfiPendingBRL)
	fmt.Fprintf(&b, "nfc_efi_treasury_brl{kind=\"submitted\"} %.2f\n", nfcSettlement.EfiSubmittedBRL)
	fmt.Fprintf(&b, "nfc_efi_treasury_brl{kind=\"reserved\"} %.2f\n", nfcSettlement.EfiReservedBRL)
	fmt.Fprintf(&b, "nfc_efi_treasury_brl{kind=\"min_buffer\"} %.2f\n", nfcSettlement.EfiMinBufferBRL)
	fmt.Fprintf(&b, "nfc_efi_treasury_brl{kind=\"available_real\"} %.2f\n", nfcSettlement.EfiAvailableRealBRL)

	b.WriteString("# HELP chainfx_paymaster_relay_total Gas Station relay requests submitted successfully.\n")
	b.WriteString("# TYPE chainfx_paymaster_relay_total counter\n")
	fmt.Fprintf(&b, "chainfx_paymaster_relay_total %d\n", reg.paymasterRelayTotal.Load())

	b.WriteString("# HELP chainfx_paymaster_relay_errors_total Relay requests that exhausted retries and entered DLQ.\n")
	b.WriteString("# TYPE chainfx_paymaster_relay_errors_total counter\n")
	fmt.Fprintf(&b, "chainfx_paymaster_relay_errors_total %d\n", reg.paymasterRelayErrors.Load())

	b.WriteString("# HELP chainfx_paymaster_fee_usdt_total Cumulative USDT fee earned by the Gas Station.\n")
	b.WriteString("# TYPE chainfx_paymaster_fee_usdt_total counter\n")
	fmt.Fprintf(&b, "chainfx_paymaster_fee_usdt_total %.6f\n", float64(reg.paymasterFeeUsdtMicro.Load())/1_000_000)

	// ── chainfx_autosweeper_* ───────────────────────────────────────────────
	b.WriteString("# HELP chainfx_autosweeper_runs_total Auto-Sweeper ticker executions.\n")
	b.WriteString("# TYPE chainfx_autosweeper_runs_total counter\n")
	fmt.Fprintf(&b, "chainfx_autosweeper_runs_total %d\n", reg.autosweeperRunsTotal.Load())

	b.WriteString("# HELP chainfx_autosweeper_errors_total Auto-Sweeper executions that failed.\n")
	b.WriteString("# TYPE chainfx_autosweeper_errors_total counter\n")
	fmt.Fprintf(&b, "chainfx_autosweeper_errors_total %d\n", reg.autosweeperErrors.Load())

	b.WriteString("# HELP chainfx_autosweeper_swept_usdt_total Cumulative USDT moved from hot to cold wallet.\n")
	b.WriteString("# TYPE chainfx_autosweeper_swept_usdt_total counter\n")
	fmt.Fprintf(&b, "chainfx_autosweeper_swept_usdt_total %.6f\n", float64(reg.autosweeperSweptMicro.Load())/1_000_000)

	// ── chainfx_process_uptime_seconds ─────────────────────────────────────
	b.WriteString("# HELP chainfx_process_uptime_seconds Seconds since the API process started.\n")
	b.WriteString("# TYPE chainfx_process_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "chainfx_process_uptime_seconds %.2f\n", uptimeSec)

	return b.String()
}

// ─── Recent overpayment log (for /app/risk or alerting integrations) ──────────

// OverpaymentEvent is a structured record of a single overpayment.
type OverpaymentEvent struct {
	IntentID   string    `json:"intent_id"`
	ExcessUSDT float64   `json:"excess_usdt"`
	At         time.Time `json:"at"`
}

// RecentOverpayments returns up to n recent overpayment events (newest last).
// Safe for concurrent access.
func RecentOverpayments(n int) []OverpaymentEvent {
	global.mu.RLock()
	defer global.mu.RUnlock()
	src := global.opLog
	if n <= 0 || n > len(src) {
		n = len(src)
	}
	start := len(src) - n
	out := make([]OverpaymentEvent, n)
	for i, e := range src[start:] {
		out[i] = OverpaymentEvent{IntentID: e.IntentID, ExcessUSDT: e.ExcessUSDT, At: e.At}
	}
	return out
}

// HasPendingOverpayments returns true when any overpayment has been detected
// since the process started. Used as a health/alert gate.
func HasPendingOverpayments() bool {
	return global.overpaymentTotal.Load() > 0
}

// Snapshot returns a JSON-serializable summary of current counters.
// Intended for the /app/risk dashboard endpoint.
func Snapshot() map[string]any {
	microUsdt := global.overpaymentUSDTMicro.Load()
	global.mu.RLock()
	nfcSettlement := global.nfcSettlement
	global.mu.RUnlock()
	return map[string]any{
		"overpayment_count":       global.overpaymentTotal.Load(),
		"overpayment_usdt":        float64(microUsdt) / 1_000_000,
		"mcp_calls_total":         global.mcpToolCallTotal.Load(),
		"mcp_calls_errors":        global.mcpToolCallErrors.Load(),
		"mcp_rate_limited":        global.mcpRateLimitedTotal.Load(),
		"penalty_box_active_bans": global.penaltyBoxActiveBans.Load(),
		"penalty_box_bans":        global.penaltyBoxBansTotal.Load(),
		"penalty_box_violations":  global.penaltyBoxViolationsTotal.Load(),
		"penalty_box_escalations": global.penaltyBoxEscalationsTotal.Load(),
		"penalty_box_blocked":     global.penaltyBoxBlockedRequestsTotal.Load(),
		"webhook_failures":        global.webhookFailureTotal.Load(),
		"paymaster_relays":        global.paymasterRelayTotal.Load(),
		"paymaster_relay_errors":  global.paymasterRelayErrors.Load(),
		"paymaster_fee_usdt":      float64(global.paymasterFeeUsdtMicro.Load()) / 1_000_000,
		"autosweeper_runs":        global.autosweeperRunsTotal.Load(),
		"autosweeper_errors":      global.autosweeperErrors.Load(),
		"autosweeper_swept_usdt":  float64(global.autosweeperSweptMicro.Load()) / 1_000_000,
		"nfc_settlement":          nfcSettlement,
		"uptime_seconds":          time.Since(global.startedAt).Seconds(),
	}
}
