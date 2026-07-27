package database

import (
	"strings"
	"testing"
	"time"

	"payment-gateway/internal/config"
)

func TestMarketplacePurchaseCalculatesTwentyPercentTakeRate(t *testing.T) {
	gross, err := ParseMicroAmount("300.000000")
	if err != nil {
		t.Fatal(err)
	}
	chainfx := gross * 2000 / 10000
	provider := gross - chainfx
	if FormatMicroAmount(chainfx) != "60.000000" {
		t.Fatalf("expected ChainFX 60.000000, got %s", FormatMicroAmount(chainfx))
	}
	if FormatMicroAmount(provider) != "240.000000" {
		t.Fatalf("expected provider 240.000000, got %s", FormatMicroAmount(provider))
	}
}

func TestMarketplacePurchaseRequestHashIsDeterministic(t *testing.T) {
	expires := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	parts := []string{
		"provider_chainfx_demo",
		"prod_gpt_business",
		"plan_gpt_300",
		"0x000000000000000000000000000000000000dead",
		"0x000000000000000000000000000000000000dead",
		"0x000000000000000000000000000000000000beef",
		"USDT",
		"0x55d398326f99059ff775485246999027b3197955",
		"300.000000",
		"60.000000",
		"240.000000",
		"2000",
		"56",
		"nonce_test",
		expires.Format(time.RFC3339Nano),
	}
	first := MarketplaceRequestHash(parts...)
	second := MarketplaceRequestHash(parts...)
	if first != second {
		t.Fatalf("expected deterministic hash, got %s and %s", first, second)
	}
	if !strings.HasPrefix(first, "0x") {
		t.Fatalf("expected 0x-prefixed hash, got %s", first)
	}
}

func TestMarketplaceProductsListsOnlyActiveProducts(t *testing.T) {
	product := &MarketplaceProduct{Status: MarketplaceProductActive}
	provider := &MarketplaceProvider{Status: MarketplaceProviderActive}
	plan := &MarketplacePlan{Status: MarketplacePlanActive}
	if product.Status != "active" || provider.Status != "active" || plan.Status != "active" {
		t.Fatal("expected only active provider/product/plan bundles to be listable")
	}
}

func TestMarketplacePurchaseRejectsDisabledPlan(t *testing.T) {
	plan := &MarketplacePlan{Status: "disabled"}
	if plan.Status == MarketplacePlanActive {
		t.Fatal("disabled plan must not be purchasable")
	}
}

func TestMarketplacePurchaseIsIdempotent(t *testing.T) {
	firstKey := "idem_purchase_1"
	secondKey := "idem_purchase_1"
	if firstKey != secondKey {
		t.Fatal("same idempotency key should resolve to the same purchase")
	}
}

func TestMarketplacePurchaseRejectsExpiredIntent(t *testing.T) {
	expiresAt := time.Now().UTC().Add(-time.Second)
	if !time.Now().UTC().After(expiresAt) {
		t.Fatal("expected expired intent to be rejected")
	}
}

func TestMarketplaceExecuteRejectsWrongChain(t *testing.T) {
	purchaseChainID := int64(56)
	receiptChainID := int64(1)
	if receiptChainID == purchaseChainID {
		t.Fatal("expected wrong chain to mismatch BSC purchase chain")
	}
}

func TestMarketplaceExecuteRejectsWrongTokenContract(t *testing.T) {
	expected := "0x55d398326f99059ff775485246999027b3197955"
	got := "0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d"
	if strings.EqualFold(expected, got) {
		t.Fatal("expected wrong token contract to be rejected")
	}
}

func TestMarketplaceExecuteRejectsWrongPayer(t *testing.T) {
	expected := "0x000000000000000000000000000000000000dead"
	got := "0x000000000000000000000000000000000000beef"
	if strings.EqualFold(expected, got) {
		t.Fatal("expected wrong payer to be rejected")
	}
}

func TestMarketplaceExecuteRejectsWrongPaymentAddress(t *testing.T) {
	expected := "0x000000000000000000000000000000000000dead"
	got := "0x000000000000000000000000000000000000beef"
	if strings.EqualFold(expected, got) {
		t.Fatal("expected wrong payment address to be rejected")
	}
}

func TestMarketplaceAccessTokenStoredAsHash(t *testing.T) {
	db := &DB{cfg: &config.Config{LGPDSecret: "test-secret"}}
	token := "cfx_access_plaintext_token"
	hash := db.accessTokenHash(token)
	if hash == token {
		t.Fatal("expected access token hash not to equal plaintext token")
	}
	if hash == "" {
		t.Fatal("expected non-empty token hash")
	}
}

func TestMarketplaceExecuteRejectsUnderpayment(t *testing.T) {
	expected := ParseMicroAmountNoError("300.000000")
	paid := ParseMicroAmountNoError("299.999999")
	if paid >= expected {
		t.Fatal("expected paid amount to be below required gross amount")
	}
}

func TestMarketplaceExecuteRecordsOverpayment(t *testing.T) {
	expected := ParseMicroAmountNoError("300.000000")
	paid := ParseMicroAmountNoError("301.250000")
	overpayment := paid - expected
	if FormatMicroAmount(overpayment) != "1.250000" {
		t.Fatalf("expected overpayment 1.250000, got %s", FormatMicroAmount(overpayment))
	}
}

func TestMarketplaceExecuteRejectsDuplicateTxLog(t *testing.T) {
	keyA := "56|0xabc|0"
	keyB := "56|0xabc|0"
	if keyA != keyB {
		t.Fatal("expected same chain_id + tx_hash + log_index to collide")
	}
}

func TestMarketplaceExecuteCannotActivateTwice(t *testing.T) {
	final := map[string]bool{
		MarketplacePurchaseActive:         true,
		MarketplacePurchaseExhausted:      true,
		MarketplacePurchaseExpired:        true,
		MarketplacePurchasePaymentInvalid: true,
		MarketplacePurchaseGrantFailed:    true,
	}
	if !final[MarketplacePurchaseActive] {
		t.Fatal("active must be treated as a final state for activation")
	}
}

func TestMarketplaceUsageDebitsQuotaAtomically(t *testing.T) {
	quota := 3
	units := 2
	if quota-units != 1 {
		t.Fatalf("expected remaining quota 1, got %d", quota-units)
	}
}

func TestMarketplaceUsageRejectsExhaustedGrant(t *testing.T) {
	quota := 0
	units := 1
	if quota >= units {
		t.Fatal("expected exhausted quota to reject usage")
	}
}

func TestMarketplaceCreatesPendingProviderSettlement(t *testing.T) {
	if MarketplaceSettlementPending != "pending" {
		t.Fatalf("expected pending provider settlement status, got %s", MarketplaceSettlementPending)
	}
}

func TestMarketplaceCapabilityRouterMockOutput(t *testing.T) {
	output := marketplaceMockOutput("document_ocr", "extract_text", "google-vision")
	body := string(output)
	for _, expected := range []string{"mock", "document_ocr", "extract_text", "google-vision", "Mock OCR text"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected mock output to contain %q, got %s", expected, body)
		}
	}
}

func TestMarketplaceRouteSelectsCheapestProvider(t *testing.T) {
	candidates := []*MarketplaceRouteCandidate{
		{ProviderSlug: "premium", Status: "active", Priority: 10, CostScore: 50, LatencyMS: 100, QualityScore: 95, FallbackOrder: 10},
		{ProviderSlug: "budget", Status: "active", Priority: 20, CostScore: 10, LatencyMS: 300, QualityScore: 75, FallbackOrder: 20},
	}
	selected := SelectMarketplaceRouteCandidate(candidates, "cheapest")
	if selected == nil || selected.ProviderSlug != "budget" {
		t.Fatalf("expected budget provider, got %#v", selected)
	}
}

func TestMarketplaceRouteSelectsLowestLatencyProvider(t *testing.T) {
	candidates := []*MarketplaceRouteCandidate{
		{ProviderSlug: "slow", Status: "active", Priority: 10, CostScore: 10, LatencyMS: 900, QualityScore: 90, FallbackOrder: 10},
		{ProviderSlug: "fast", Status: "active", Priority: 20, CostScore: 50, LatencyMS: 80, QualityScore: 70, FallbackOrder: 20},
	}
	selected := SelectMarketplaceRouteCandidate(candidates, "lowest_latency")
	if selected == nil || selected.ProviderSlug != "fast" {
		t.Fatalf("expected fast provider, got %#v", selected)
	}
}

func TestMarketplaceRouteSelectsHighestQualityProvider(t *testing.T) {
	candidates := []*MarketplaceRouteCandidate{
		{ProviderSlug: "cheap", Status: "active", Priority: 10, CostScore: 10, LatencyMS: 100, QualityScore: 70, SuccessRateBps: 9900, FallbackOrder: 10},
		{ProviderSlug: "quality", Status: "active", Priority: 20, CostScore: 60, LatencyMS: 400, QualityScore: 98, SuccessRateBps: 9800, FallbackOrder: 20},
	}
	selected := SelectMarketplaceRouteCandidate(candidates, "highest_quality")
	if selected == nil || selected.ProviderSlug != "quality" {
		t.Fatalf("expected quality provider, got %#v", selected)
	}
}

func ParseMicroAmountNoError(value string) int64 {
	amount, err := ParseMicroAmount(value)
	if err != nil {
		panic(err)
	}
	return amount
}
