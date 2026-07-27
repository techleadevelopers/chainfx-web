package solana

import (
	"crypto/ed25519"
	"testing"
)

func TestBase58AddressRoundTrip(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	key := ed25519.NewKeyFromSeed(seed)
	address := base58Encode(key.Public().(ed25519.PublicKey))
	if err := ValidateAddress(address); err != nil {
		t.Fatalf("ValidateAddress(%q): %v", address, err)
	}
	raw, err := base58Decode(address)
	if err != nil {
		t.Fatalf("base58Decode: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("decoded len=%d want 32", len(raw))
	}
}

func TestBuildSOLTransfer(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 7)
	}
	key := ed25519.NewKeyFromSeed(seed)
	to := base58Encode(bytesOf(32, 9))
	blockhash := base58Encode(bytesOf(32, 3))
	tx, msg, err := BuildSOLTransfer(key, to, blockhash, 12345)
	if err != nil {
		t.Fatalf("BuildSOLTransfer: %v", err)
	}
	if len(tx) <= len(msg) || len(msg) == 0 {
		t.Fatalf("unexpected tx/message sizes tx=%d msg=%d", len(tx), len(msg))
	}
	if tx[0] != 1 {
		t.Fatalf("signature count=%d want 1", tx[0])
	}
	if !ed25519.Verify(key.Public().(ed25519.PublicKey), msg, tx[1:65]) {
		t.Fatal("signature does not verify against message")
	}
}

func bytesOf(n int, value byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = value
	}
	return out
}
