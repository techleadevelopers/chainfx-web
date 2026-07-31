package workers

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestAutoSweepOperationIDBindsEconomicPayload(t *testing.T) {
	hot := "0x1111111111111111111111111111111111111111"
	cold := "0x2222222222222222222222222222222222222222"
	token := "0x55d398326f99059fF775485246999027B3197955"
	amount := big.NewInt(123_456_000)

	id := autoSweepOperationID(hot, cold, token, "BSC", amount)
	if id == "" {
		t.Fatal("operation id vazio")
	}
	if id != autoSweepOperationID(common.HexToAddress(hot).Hex(), common.HexToAddress(cold).Hex(), common.HexToAddress(token).Hex(), "bsc", amount) {
		t.Fatal("operation id deveria canonicalizar address/network")
	}
	if id == autoSweepOperationID(hot, "0x3333333333333333333333333333333333333333", token, "BSC", amount) {
		t.Fatal("destination diferente nao pode reutilizar operation id")
	}
	if id == autoSweepOperationID(hot, cold, "0x4444444444444444444444444444444444444444", "BSC", amount) {
		t.Fatal("token diferente nao pode reutilizar operation id")
	}
	if id == autoSweepOperationID(hot, cold, token, "POLYGON", amount) {
		t.Fatal("network diferente nao pode reutilizar operation id")
	}
	if id == autoSweepOperationID(hot, cold, token, "BSC", big.NewInt(123_457_000)) {
		t.Fatal("amount diferente nao pode reutilizar operation id")
	}
}

func TestAutoSweepUSDTIntegerConversions(t *testing.T) {
	raw, err := decimalUSDTToRaw("5000.123456")
	if err != nil {
		t.Fatal(err)
	}
	if raw.String() != "5000123456" {
		t.Fatalf("raw mismatch: %s", raw)
	}
	if got := rawUSDTToDecimal(raw); got != "5000.123456" {
		t.Fatalf("decimal mismatch: %s", got)
	}
}

func TestAutoSweeperRunStatusPreservesBroadcastAmbiguity(t *testing.T) {
	if got := autoSweeperRunStatusFromSigner("broadcast_unknown"); got != "broadcast_unknown" {
		t.Fatalf("broadcast_unknown must be preserved, got %s", got)
	}
	if got := autoSweeperRunStatusFromSigner("broadcast"); got != "broadcast" {
		t.Fatalf("broadcast must be preserved, got %s", got)
	}
	if got := autoSweeperRunStatusFromSigner(""); got != "broadcast_unknown" {
		t.Fatalf("empty signer status should fail conservative, got %s", got)
	}
}
