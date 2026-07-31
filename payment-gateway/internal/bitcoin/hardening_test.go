package bitcoin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSendSameIdempotencyKeyConcurrentCreatesOneEconomicTx(t *testing.T) {
	svc, store, provider := newHardeningTestService(t)
	user := "user-a"
	addr := store.mustAddress(t, user, 0)
	store.addUTXO(UTXO{
		ID:              "utxo-1",
		Network:         string(Regtest),
		UserID:          user,
		WalletAddressID: addr.ID,
		Txid:            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Vout:            0,
		ValueSats:       1_000_000,
		ScriptPubKey:    store.scriptForAddress(t, addr.Address),
		Status:          UTXOStatusConfirmed,
		Confirmations:   3,
	})
	dest := store.mustAddress(t, "external", 1).Address

	req := SendRequest{UserID: user, ToAddress: dest, AmountSats: 300_000, FeeRateSatVB: 50, IdempotencyKey: "same-key"}
	var wg sync.WaitGroup
	results := make([]SendResult, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.Send(context.Background(), req)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("send %d failed: %v", i, err)
		}
	}
	if store.claimCreates != 1 || store.signedCount != 1 || provider.broadcastCount != 1 {
		t.Fatalf("want one claim/sign/broadcast, got claims=%d signed=%d broadcasts=%d", store.claimCreates, store.signedCount, provider.broadcastCount)
	}
	if results[0].TxID != "" && results[1].TxID != "" && results[0].TxID != results[1].TxID {
		t.Fatalf("same idempotency key returned different txids: %+v %+v", results[0], results[1])
	}
}

func TestDifferentSendsCannotReserveSameUTXO(t *testing.T) {
	svc, store, provider := newHardeningTestService(t)
	user := "user-a"
	addr := store.mustAddress(t, user, 0)
	store.addUTXO(UTXO{
		ID:              "utxo-1",
		Network:         string(Regtest),
		UserID:          user,
		WalletAddressID: addr.ID,
		Txid:            "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Vout:            0,
		ValueSats:       400_000,
		ScriptPubKey:    store.scriptForAddress(t, addr.Address),
		Status:          UTXOStatusConfirmed,
		Confirmations:   3,
	})
	dest := store.mustAddress(t, "external", 1).Address

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, key := range []string{"key-a", "key-b"} {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			_, errs[i] = svc.Send(context.Background(), SendRequest{
				UserID: user, ToAddress: dest, AmountSats: 300_000, FeeRateSatVB: 50, IdempotencyKey: key,
			})
		}(i, key)
	}
	wg.Wait()
	success := 0
	for _, err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrDoubleSpend) && !errors.Is(err, ErrNoUTXOs) && !errors.Is(err, ErrInsufficientFunds) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 1 || provider.broadcastCount != 1 {
		t.Fatalf("want one successful spend/broadcast, success=%d broadcasts=%d errs=%v", success, provider.broadcastCount, errs)
	}
}

func TestAmbiguousBroadcastKeepsReservedAndRecoveryDoesNotRebuild(t *testing.T) {
	svc, store, provider := newHardeningTestService(t)
	provider.broadcastUnknown = true
	user := "user-a"
	addr := store.mustAddress(t, user, 0)
	store.addUTXO(UTXO{
		ID:              "utxo-1",
		Network:         string(Regtest),
		UserID:          user,
		WalletAddressID: addr.ID,
		Txid:            "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Vout:            0,
		ValueSats:       1_000_000,
		ScriptPubKey:    store.scriptForAddress(t, addr.Address),
		Status:          UTXOStatusConfirmed,
		Confirmations:   3,
	})
	dest := store.mustAddress(t, "external", 1).Address

	result, err := svc.Send(context.Background(), SendRequest{
		UserID: user, ToAddress: dest, AmountSats: 300_000, FeeRateSatVB: 50, IdempotencyKey: "ambiguous",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.Status != TxStatusBroadcastUnknown {
		t.Fatalf("want broadcast_unknown, got %+v", result)
	}
	tx := store.txByKey[user+"|ambiguous"]
	if tx.RawTxHash == "" || tx.Txid == "" || tx.Status != TxStatusBroadcastUnknown {
		t.Fatalf("signed tx not persisted correctly: %+v", tx)
	}
	if store.utxos["utxo-1"].Status != UTXOStatusReserved {
		t.Fatalf("broadcast_unknown must keep UTXO reserved, got %s", store.utxos["utxo-1"].Status)
	}

	provider.broadcastUnknown = false
	provider.txs[tx.Txid] = &ProviderTxStatus{Txid: tx.Txid}
	provider.txs[tx.Txid].Status.Confirmed = true
	provider.txs[tx.Txid].Status.BlockHeight = 100
	_, err = svc.ReconcileSignedOrBroadcastUnknown(context.Background(), tx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if provider.broadcastCount != 1 {
		t.Fatalf("recovery found tx and must not rebroadcast, broadcasts=%d", provider.broadcastCount)
	}
	if store.signedCount != 1 {
		t.Fatalf("recovery must not rebuild/resign, signedCount=%d", store.signedCount)
	}
}

func TestReceiveHonorsMinConfirmationsOnReprocess(t *testing.T) {
	svc, store, provider := newHardeningTestService(t)
	addr := store.mustAddress(t, "user-a", 0)
	txid := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	for _, tc := range []struct {
		height int64
		want   string
	}{
		{0, UTXOStatusPending},
		{100, UTXOStatusPending},
		{99, UTXOStatusPending},
		{98, UTXOStatusConfirmed},
	} {
		provider.height = 100
		utxo := ProviderUTXO{Txid: txid, Vout: 0, Value: 100_000}
		if tc.height > 0 {
			utxo.Status.Confirmed = true
			utxo.Status.BlockHeight = tc.height
		}
		provider.addressUTXOs[addr.Address] = []ProviderUTXO{utxo}
		if _, _, err := svc.SyncAddressUTXOsWithEvents(context.Background(), addr); err != nil {
			t.Fatalf("sync height=%d: %v", tc.height, err)
		}
		got := store.utxoByOutpoint[string(Regtest)+"|"+txid+"|0"].Status
		if got != tc.want {
			t.Fatalf("height=%d status=%s want %s", tc.height, got, tc.want)
		}
	}
}

func newHardeningTestService(t *testing.T) (*Service, *fakeBTCStore, *fakeBTCProvider) {
	t.Helper()
	seed := sha256.Sum256([]byte("chainfx-btc-hardening-test-seed"))
	master, err := NewMasterKeyForNetwork(seed[:], Regtest)
	if err != nil {
		t.Fatal(err)
	}
	xpriv, err := DeriveAccountXPriv(master, Regtest)
	if err != nil {
		t.Fatal(err)
	}
	xpub := xpriv.Neuter()
	key := sha256.Sum256([]byte("chainfx-btc-hardening-test-encryption"))
	encrypted := encryptForTest(t, key[:], []byte(xpriv.String()))
	cfg := &Config{
		Enabled:             true,
		Network:             Regtest,
		XPub:                xpub.String(),
		EncryptedSeed:       hex.EncodeToString(encrypted),
		EncryptionKey:       hex.EncodeToString(key[:]),
		MinConfirmations:    3,
		FeeTargetBlocks:     3,
		MinFeeRateSatVB:     1,
		MaxFeeRateSatVB:     200,
		DustLimitSats:       546,
		WithdrawalsEnabled:  true,
		DepositScanInterval: time.Second,
		TxScanInterval:      time.Second,
	}
	store := newFakeBTCStore(cfg)
	provider := &fakeBTCProvider{
		feeRate:      50,
		height:       100,
		txs:          make(map[string]*ProviderTxStatus),
		addressUTXOs: make(map[string][]ProviderUTXO),
	}
	return &Service{cfg: cfg, provider: provider, repo: store}, store, provider
}

func encryptForTest(t *testing.T, key, plaintext []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	return append(nonce, gcm.Seal(nil, nonce, plaintext, nil)...)
}

type fakeBTCProvider struct {
	mu               sync.Mutex
	feeRate          int64
	height           int64
	broadcastUnknown bool
	broadcastCount   int
	lastRaw          string
	txs              map[string]*ProviderTxStatus
	addressUTXOs     map[string][]ProviderUTXO
}

func (p *fakeBTCProvider) GetAddressUTXOs(_ context.Context, address string) ([]ProviderUTXO, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]ProviderUTXO(nil), p.addressUTXOs[address]...)
	return out, nil
}

func (p *fakeBTCProvider) GetTransaction(_ context.Context, txid string) (*ProviderTxStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tx, ok := p.txs[txid]
	if !ok {
		return nil, errors.New("provider: não encontrado")
	}
	cp := *tx
	return &cp, nil
}

func (p *fakeBTCProvider) EstimateFeeRate(context.Context, int) (int64, error) { return p.feeRate, nil }

func (p *fakeBTCProvider) BroadcastTransaction(_ context.Context, rawTxHex string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.broadcastCount++
	p.lastRaw = rawTxHex
	txid, err := TxIDFromRawSignedHex(rawTxHex)
	if err != nil {
		return "", err
	}
	if p.broadcastUnknown {
		p.txs[txid] = &ProviderTxStatus{Txid: txid}
		return "", ErrBroadcastUnknown
	}
	p.txs[txid] = &ProviderTxStatus{Txid: txid}
	return txid, nil
}

func (p *fakeBTCProvider) GetCurrentBlockHeight(context.Context) (int64, error) {
	return p.height, nil
}
