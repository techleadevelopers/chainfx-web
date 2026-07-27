package eip712

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestPrepareAndVerifyM2MIntent(t *testing.T) {
	key, err := crypto.HexToECDSA(strings.Repeat("01", 32))
	if err != nil {
		t.Fatal(err)
	}
	signer := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	domain := Domain{
		Name:              "ChainFX",
		Version:           "1",
		ChainID:           56,
		VerifyingContract: "0x0000000000000000000000000000000000000001",
	}
	intent := Intent{
		IntentType:     TypeM2MIntent,
		Payer:          signer,
		Recipient:      "0x0000000000000000000000000000000000000002",
		Asset:          "0x0000000000000000000000000000000000000003",
		Amount:         "1000000000000000000",
		FeeBps:         100,
		Nonce:          "m2m-test-nonce",
		Deadline:       uint64(time.Now().Add(time.Hour).Unix()),
		IdempotencyKey: "idem-test",
	}
	prepared, err := Prepare(domain, intent, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := decodeFixedHex(prepared.Digest, 32)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := crypto.Sign(digest, key)
	if err != nil {
		t.Fatal(err)
	}
	verification, err := Verify(domain, intent, "0x"+commonHex(sig), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !verification.Valid {
		t.Fatalf("expected valid signature, recovered=%s expected=%s", verification.RecoveredSigner, verification.ExpectedSigner)
	}
	if verification.RecoveredSigner != signer {
		t.Fatalf("unexpected signer: %s", verification.RecoveredSigner)
	}
}

func TestBuildEIPCalldataSelectors(t *testing.T) {
	sig := "0x" + strings.Repeat("11", 64) + "1b"
	permit, err := BuildEIP2612PermitCalldata(
		"0x0000000000000000000000000000000000000001",
		"0x0000000000000000000000000000000000000002",
		big.NewInt(100),
		123,
		sig,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(permit, "0xd505accf") {
		t.Fatalf("unexpected permit selector: %s", permit[:10])
	}
	twa, err := BuildEIP3009TransferWithAuthorizationCalldata(
		"0x0000000000000000000000000000000000000001",
		"0x0000000000000000000000000000000000000002",
		big.NewInt(100),
		0,
		123,
		"nonce",
		sig,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(twa, "0xe3ee160e") {
		t.Fatalf("unexpected transferWithAuthorization selector: %s", twa[:10])
	}
}

func commonHex(raw []byte) string {
	const table = "0123456789abcdef"
	out := make([]byte, len(raw)*2)
	for i, b := range raw {
		out[i*2] = table[b>>4]
		out[i*2+1] = table[b&0x0f]
	}
	return string(out)
}
