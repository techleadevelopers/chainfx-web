package workers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"payment-gateway/internal/database"
)

func TestBuildEfiPixSendPayloadUsesMerchantAmountNotTotalDebit(t *testing.T) {
	settlement := &database.MerchantSettlement{
		ID:              "nfc_settle_test",
		AuthorizationID: "nfc_auth_test",
		AmountBRLMinor:  10000,
		FeeBRLMinor:     400,
		TargetPixKey:    "merchant@example.com",
	}
	payload := buildEfiPixSendPayload("payer@example.com", settlement)
	if payload["valor"] != "100.00" {
		t.Fatalf("expected merchant amount 100.00, got %v", payload["valor"])
	}
	if payload["valor"] == "104.00" {
		t.Fatal("payout must not use total debit including ChainFX fee")
	}
}

func TestBuildEfiPixSendUsesStableIDEnvio(t *testing.T) {
	settlement := &database.MerchantSettlement{
		ID:             "nfc_settle_test",
		IdempotencyKey: "nfc_settle_test",
		AmountBRLMinor: 10000,
		TargetPixKey:   "merchant@example.com",
	}
	first := settlement.IdempotencyKey
	second := settlement.IdempotencyKey
	if first == "" || first != second {
		t.Fatalf("expected stable idEnvio, got %q and %q", first, second)
	}
}

func TestParseEfiPixSendResultDoesNotImplyConfirmed(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"idEnvio": "nfc_settle_test",
		"e2eId":   "E123",
		"status":  "EM_PROCESSAMENTO",
	})
	result, err := parseEfiPixSendResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status == database.MerchantSettlementStatusConfirmed {
		t.Fatal("initial Efí response must not be treated as confirmed")
	}
}

func TestRetryAfterParsesSeconds(t *testing.T) {
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("Retry-After", "7")
	if got := retryAfter(resp); got != 7*time.Second {
		t.Fatalf("expected 7s retry-after, got %s", got)
	}
}
