package solana

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
)

const (
	solTestSecretV1 = "01234567890123456789012345678901"
	solTestSecretV2 = "abcdefghijklmnopqrstuvwxyz123456"
)

func TestSolanaDerivationKeyringRotationKeepsExistingWallet(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	rpc := &fakeRPC{balance: 1_000_000_000, fee: 5000, blockhash: base58Encode(bytesOf(32, 8)), lastValid: 100}
	svcV1 := testServiceWithKeys(store, rpc, "v1", map[string]string{"v1": solTestSecretV1})

	addrA, err := svcV1.GetOrCreateAddress(ctx, "user-a")
	if err != nil {
		t.Fatalf("create user-a address: %v", err)
	}
	if addrA.DerivationKeyID != "v1" {
		t.Fatalf("derivation key id=%s want v1", addrA.DerivationKeyID)
	}

	svcV2 := testServiceWithKeys(store, rpc, "v2", map[string]string{"v1": solTestSecretV1, "v2": solTestSecretV2})
	gotA, err := svcV2.GetOrCreateAddress(ctx, "user-a")
	if err != nil {
		t.Fatalf("get user-a with rotated keyring: %v", err)
	}
	if gotA.Address != addrA.Address || gotA.DerivationKeyID != "v1" {
		t.Fatalf("existing address changed: got %+v want %+v", gotA, addrA)
	}

	to := base58Encode(bytesOf(32, 9))
	if _, err := svcV2.Send(ctx, SendRequest{UserID: "user-a", ToAddress: to, AmountLamports: 100_000, IdempotencyKey: "send-a"}); err != nil {
		t.Fatalf("send user-a after rotation: %v", err)
	}
	tx := store.onlyWithdrawal()
	if tx.FeePayer != addrA.Address {
		t.Fatalf("fee payer=%s want persisted address %s", tx.FeePayer, addrA.Address)
	}
	if got := feePayerFromSignedSOLTx(t, rpc.lastRaw); got != addrA.Address {
		t.Fatalf("signed tx fee payer=%s want %s", got, addrA.Address)
	}

	addrB, err := svcV2.GetOrCreateAddress(ctx, "user-b")
	if err != nil {
		t.Fatalf("create user-b address: %v", err)
	}
	if addrB.DerivationKeyID != "v2" {
		t.Fatalf("user-b derivation key id=%s want v2", addrB.DerivationKeyID)
	}
	if addrB.Address == addrA.Address {
		t.Fatal("user-a and user-b derived same SOL address")
	}
}

func TestSolanaSendFailsClosedWhenOldDerivationKeyIsMissing(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svcV1 := testServiceWithKeys(store, &fakeRPC{}, "v1", map[string]string{"v1": solTestSecretV1})
	addrA, err := svcV1.GetOrCreateAddress(ctx, "user-a")
	if err != nil {
		t.Fatalf("create user-a address: %v", err)
	}

	rpc := &fakeRPC{balance: 1_000_000_000, fee: 5000, blockhash: base58Encode(bytesOf(32, 8)), lastValid: 100}
	svcV2Only := testServiceWithKeys(store, rpc, "v2", map[string]string{"v2": solTestSecretV2})
	_, err = svcV2Only.Send(ctx, SendRequest{UserID: "user-a", ToAddress: base58Encode(bytesOf(32, 9)), AmountLamports: 100_000, IdempotencyKey: "missing-v1"})
	if !errors.Is(err, ErrWalletKeyUnavailable) {
		t.Fatalf("err=%v want ErrWalletKeyUnavailable", err)
	}
	tx := store.onlyWithdrawal()
	if tx.SourceAddress != "" || tx.ReservedLamports != 0 || tx.Status != StatusCreated {
		t.Fatalf("withdrawal was reserved despite missing key: %+v addr=%+v", tx, addrA)
	}
	if rpc.sendCount != 0 {
		t.Fatalf("send count=%d want 0", rpc.sendCount)
	}
}

func TestSolanaSendRejectsWrongSecretForPersistedKeyID(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svcV1 := testServiceWithKeys(store, &fakeRPC{}, "v1", map[string]string{"v1": solTestSecretV1})
	if _, err := svcV1.GetOrCreateAddress(ctx, "user-a"); err != nil {
		t.Fatalf("create user-a address: %v", err)
	}

	rpc := &fakeRPC{balance: 1_000_000_000, fee: 5000, blockhash: base58Encode(bytesOf(32, 8)), lastValid: 100}
	svcWrong := testServiceWithKeys(store, rpc, "v1", map[string]string{"v1": solTestSecretV2})
	_, err := svcWrong.Send(ctx, SendRequest{UserID: "user-a", ToAddress: base58Encode(bytesOf(32, 9)), AmountLamports: 100_000, IdempotencyKey: "wrong-secret"})
	if !errors.Is(err, ErrSignerKeyMismatch) {
		t.Fatalf("err=%v want ErrSignerKeyMismatch", err)
	}
	tx := store.onlyWithdrawal()
	if tx.SourceAddress != "" || tx.ReservedLamports != 0 || tx.Status != StatusCreated {
		t.Fatalf("withdrawal was reserved despite signer mismatch: %+v", tx)
	}
	if rpc.sendCount != 0 {
		t.Fatalf("send count=%d want 0", rpc.sendCount)
	}
}

func TestSolanaLegacyDerivationKeyIDIsExplicit(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svc := testServiceWithKeys(store, &fakeRPC{balance: 1_000_000_000, fee: 5000, blockhash: base58Encode(bytesOf(32, 8)), lastValid: 100}, "v1", map[string]string{"legacy": solTestSecretV1, "v1": solTestSecretV1})
	key, _ := derivePrivateKeyWithSecret("user-a", solTestSecretV1)
	addr := Address{ID: "addr-user-a", UserID: "user-a", Network: Network, Address: base58Encode(key.Public().(ed25519.PublicKey)), Status: "active"}
	store.addresses["user-a"] = addr

	if _, err := svc.Send(ctx, SendRequest{UserID: "user-a", ToAddress: base58Encode(bytesOf(32, 9)), AmountLamports: 100_000, IdempotencyKey: "legacy"}); err != nil {
		t.Fatalf("send legacy address: %v", err)
	}
}

func testServiceWithKeys(store *fakeStore, rpc *fakeRPC, activeID string, keys map[string]string) *Service {
	copied := map[string]string{}
	for id, secret := range keys {
		copied[id] = secret
	}
	return &Service{
		cfg: Config{
			Enabled:               true,
			WithdrawalsEnabled:    true,
			MinConfirmations:      1,
			ActiveDerivationKeyID: activeID,
			LegacyDerivationKeyID: "legacy",
		},
		keyring: &derivationKeyring{activeID: activeID, keys: copied, legacyID: "legacy"},
		repo:    store,
		rpc:     rpc,
	}
}

func feePayerFromSignedSOLTx(t *testing.T, raw []byte) string {
	t.Helper()
	if len(raw) < 101 || raw[0] != 1 || raw[68] != 3 {
		t.Fatalf("unexpected signed tx layout len=%d", len(raw))
	}
	return base58Encode(raw[69:101])
}
