package transactions

import (
	"math/big"
	"testing"
)

func TestSettlementOperationIDIsDeterministicAndDomainSeparated(t *testing.T) {
	vault := "0x1000000000000000000000000000000000000001"
	intent := "settlement_intent_1"
	token := "0x2000000000000000000000000000000000000002"
	recipient := "0x3000000000000000000000000000000000000003"
	amount := big.NewInt(1_000_000)

	first, err := SettlementOperationID(56, vault, intent, token, recipient, amount)
	if err != nil {
		t.Fatalf("SettlementOperationID returned error: %v", err)
	}
	second, err := SettlementOperationID(56, vault, intent, token, recipient, amount)
	if err != nil {
		t.Fatalf("SettlementOperationID returned error on second call: %v", err)
	}
	if first != second {
		t.Fatalf("expected deterministic operation id")
	}

	polygon, err := SettlementOperationID(137, vault, intent, token, recipient, amount)
	if err != nil {
		t.Fatalf("SettlementOperationID returned polygon error: %v", err)
	}
	if polygon == first {
		t.Fatalf("expected chainID to domain-separate operation id")
	}

	otherAmount, err := SettlementOperationID(56, vault, intent, token, recipient, big.NewInt(2_000_000))
	if err != nil {
		t.Fatalf("SettlementOperationID returned amount error: %v", err)
	}
	if otherAmount == first {
		t.Fatalf("expected amount to domain-separate operation id")
	}
}

func TestSettlementOperationIDRejectsInvalidInput(t *testing.T) {
	if _, err := SettlementOperationID(0, "", "", "", "", nil); err == nil {
		t.Fatalf("expected invalid input error")
	}
}

func TestSettlementOperationIDMatchesHardhatVector(t *testing.T) {
	got, err := SettlementOperationID(
		31337,
		"0x1111111111111111111111111111111111111111",
		"settlement-001",
		"0x2222222222222222222222222222222222222222",
		"0x3333333333333333333333333333333333333333",
		big.NewInt(10000000),
	)
	if err != nil {
		t.Fatalf("SettlementOperationID returned error: %v", err)
	}
	const expected = "0xddf2d849f366d3663412542b335fe30a92e2e01b0869ddf5d24f9b68eaf6ad52"
	if got.Hex() != expected {
		t.Fatalf("operationId mismatch: got %s want %s", got.Hex(), expected)
	}
}

func TestTokenAmountRawUsesNetworkDecimals(t *testing.T) {
	bsc, err := TokenAmountRaw("USDT", "BSC", "1.25")
	if err != nil {
		t.Fatalf("TokenAmountRaw BSC returned error: %v", err)
	}
	if bsc.String() != "1250000000000000000" {
		t.Fatalf("unexpected BSC raw amount: %s", bsc)
	}
	polygon, err := TokenAmountRaw("USDT", "POLYGON", "1.25")
	if err != nil {
		t.Fatalf("TokenAmountRaw Polygon returned error: %v", err)
	}
	if polygon.String() != "1250000" {
		t.Fatalf("unexpected Polygon raw amount: %s", polygon)
	}
}
