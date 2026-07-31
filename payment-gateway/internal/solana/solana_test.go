package solana

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
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

func TestSendSameIdempotencyConcurrentCreatesOneEconomicTransfer(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	rpc := &fakeRPC{balance: 1_000_000_000, fee: 5000, blockhash: base58Encode(bytesOf(32, 8)), lastValid: 100}
	svc := testService(store, rpc)
	to := base58Encode(bytesOf(32, 9))
	req := SendRequest{UserID: "user-a", ToAddress: to, AmountLamports: 100_000, IdempotencyKey: "same-key"}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.Send(ctx, req)
		}()
	}
	wg.Wait()
	if rpc.sendCount != 1 {
		t.Fatalf("sendTransaction count=%d want 1", rpc.sendCount)
	}
	if got := store.withdrawalCount(); got != 1 {
		t.Fatalf("withdrawal rows=%d want 1", got)
	}
	if got := store.signedCount(); got != 1 {
		t.Fatalf("signed rows=%d want 1", got)
	}
}

func TestSendSameIdempotencyDifferentPayloadConflicts(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	rpc := &fakeRPC{balance: 1_000_000_000, fee: 5000, blockhash: base58Encode(bytesOf(32, 8)), lastValid: 100}
	svc := testService(store, rpc)
	to := base58Encode(bytesOf(32, 9))
	_, err := svc.Send(ctx, SendRequest{UserID: "user-a", ToAddress: to, AmountLamports: 100_000, IdempotencyKey: "same-key"})
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	_, err = svc.Send(ctx, SendRequest{UserID: "user-a", ToAddress: to, AmountLamports: 200_000, IdempotencyKey: "same-key"})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("err=%v want ErrIdempotencyConflict", err)
	}
}

func TestSubmitUnknownPersistsSignedTransactionAndDoesNotReleaseReservation(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	rpc := &fakeRPC{balance: 1_000_000_000, fee: 5000, blockhash: base58Encode(bytesOf(32, 8)), lastValid: 100, sendErr: fmt.Errorf("wrapped: %w", ErrSubmitUnknown)}
	svc := testService(store, rpc)
	to := base58Encode(bytesOf(32, 9))
	result, err := svc.Send(ctx, SendRequest{UserID: "user-a", ToAddress: to, AmountLamports: 100_000, IdempotencyKey: "ambiguous"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	tx := store.onlyWithdrawal()
	if result.Status != StatusSubmitUnknown || tx.Status != StatusSubmitUnknown {
		t.Fatalf("status result=%s db=%s want submit_unknown", result.Status, tx.Status)
	}
	if tx.Signature == "" || tx.SignedRawTx == "" || tx.ReservedLamports == 0 {
		t.Fatalf("signed recovery fields missing: %+v", tx)
	}
}

func TestRecoveryRebroadcastsExactSignedTransaction(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	rpc := &fakeRPC{balance: 1_000_000_000, fee: 5000, blockhash: base58Encode(bytesOf(32, 8)), lastValid: 100, blockHeight: 50}
	svc := testService(store, rpc)
	to := base58Encode(bytesOf(32, 9))
	key, _ := svc.derivePrivateKey("user-a")
	raw, _, _ := BuildSOLTransfer(key, to, rpc.blockhash, 100_000)
	sig, _ := SignatureFromSignedTransaction(raw)
	store.put(Transaction{ID: "op1", UserID: "user-a", Signature: sig, Direction: DirectionWithdrawal, AmountRaw: "100000", Status: StatusSigned, SignedRawTx: base64.StdEncoding.EncodeToString(raw), LastValidBlockHeight: 100})
	_, err := svc.TrackWithdrawals(ctx)
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	if rpc.sendCount != 1 || string(rpc.lastRaw) != string(raw) {
		t.Fatalf("rebroadcast did not use exact raw tx")
	}
	if got := store.onlyWithdrawal().Status; got != StatusSubmitted {
		t.Fatalf("status=%s want submitted", got)
	}
}

func TestExpiredBlockhashDoesNotBlindResign(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	rpc := &fakeRPC{balance: 1_000_000_000, fee: 5000, blockhash: base58Encode(bytesOf(32, 8)), lastValid: 100, blockHeight: 101}
	svc := testService(store, rpc)
	to := base58Encode(bytesOf(32, 9))
	key, _ := svc.derivePrivateKey("user-a")
	raw, _, _ := BuildSOLTransfer(key, to, rpc.blockhash, 100_000)
	sig, _ := SignatureFromSignedTransaction(raw)
	store.put(Transaction{ID: "op1", UserID: "user-a", Signature: sig, Direction: DirectionWithdrawal, AmountRaw: "100000", Status: StatusSigned, SignedRawTx: base64.StdEncoding.EncodeToString(raw), LastValidBlockHeight: 100})
	_, _ = svc.TrackWithdrawals(ctx)
	if rpc.sendCount != 0 {
		t.Fatalf("send count=%d want 0", rpc.sendCount)
	}
	if got := store.onlyWithdrawal().Status; got != StatusRebuildRequired {
		t.Fatalf("status=%s want rebuild_required", got)
	}
}

func TestReceiveReprocessConcurrentCreditsOnce(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	addr := Address{UserID: "user-a", Address: base58Encode(bytesOf(32, 7))}
	store.addresses[addr.UserID] = addr
	rpc := &fakeRPC{
		signatures: []SignatureInfo{{Signature: "sig-dep", Slot: 10, ConfirmationStatus: "confirmed"}},
		transactions: map[string]map[string]any{"sig-dep": {
			"meta":        map[string]any{"preBalances": []any{int64(10), int64(0)}, "postBalances": []any{int64(10), int64(12345)}},
			"transaction": map[string]any{"message": map[string]any{"accountKeys": []any{base58Encode(bytesOf(32, 1)), addr.Address}}},
		}},
	}
	svc := testService(store, rpc)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.SyncAddress(ctx, addr)
		}()
	}
	wg.Wait()
	if got := store.depositCount(); got != 1 {
		t.Fatalf("deposit rows=%d want 1", got)
	}
}

func testService(store *fakeStore, rpc *fakeRPC) *Service {
	return &Service{
		cfg:  Config{Enabled: true, WithdrawalsEnabled: true, MinConfirmations: 1, DerivationSecret: "01234567890123456789012345678901"},
		repo: store,
		rpc:  rpc,
	}
}

type fakeRPC struct {
	mu           sync.Mutex
	balance      int64
	fee          int64
	blockhash    string
	lastValid    int64
	blockHeight  int64
	sendErr      error
	sendCount    int
	lastRaw      []byte
	signatures   []SignatureInfo
	transactions map[string]map[string]any
	statuses     map[string]string
}

func (f *fakeRPC) GetBalance(context.Context, string) (int64, error) { return f.balance, nil }
func (f *fakeRPC) GetLatestBlockhash(context.Context) (string, int64, error) {
	return f.blockhash, f.lastValid, nil
}
func (f *fakeRPC) GetFeeForMessage(context.Context, []byte) (int64, error) { return f.fee, nil }
func (f *fakeRPC) SendTransaction(_ context.Context, tx []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCount++
	f.lastRaw = append([]byte(nil), tx...)
	sig, _ := SignatureFromSignedTransaction(tx)
	if f.sendErr != nil {
		return "", f.sendErr
	}
	return sig, nil
}
func (f *fakeRPC) GetSignaturesForAddress(context.Context, string, string, int) ([]SignatureInfo, error) {
	return f.signatures, nil
}
func (f *fakeRPC) GetTransaction(_ context.Context, signature string) (map[string]any, error) {
	return f.transactions[signature], nil
}
func (f *fakeRPC) GetSignatureStatuses(_ context.Context, sigs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, sig := range sigs {
		if f.statuses != nil {
			out[sig] = f.statuses[sig]
		}
		if out[sig] == "" {
			out[sig] = StatusPending
		}
	}
	return out, nil
}
func (f *fakeRPC) GetBlockHeight(context.Context) (int64, error) { return f.blockHeight, nil }

type fakeStore struct {
	mu        sync.Mutex
	addresses map[string]Address
	txs       map[string]*Transaction
	byIDKey   map[string]string
	byReceive map[string]string
}

func newFakeStore() *fakeStore {
	return &fakeStore{addresses: map[string]Address{}, txs: map[string]*Transaction{}, byIDKey: map[string]string{}, byReceive: map[string]string{}}
}
func (f *fakeStore) ensureSchema(context.Context) error { return nil }
func (f *fakeStore) getAddress(_ context.Context, userID string) (*Address, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if addr, ok := f.addresses[userID]; ok {
		return &addr, nil
	}
	return nil, nil
}
func (f *fakeStore) insertAddress(_ context.Context, userID, address, keyID string) (*Address, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	addr := Address{ID: "addr-" + userID, UserID: userID, Network: Network, Address: address, DerivationKeyID: keyID, Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.addresses[userID] = addr
	return &addr, nil
}
func (f *fakeStore) listActiveAddresses(context.Context) ([]Address, error) { return nil, nil }
func (f *fakeStore) transactionByIdempotency(_ context.Context, userID, key string) (*Transaction, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id := f.byIDKey[userID+"|"+key]; id != "" {
		tx := *f.txs[id]
		return &tx, tx.RequestHash, nil
	}
	return nil, "", nil
}
func (f *fakeStore) claimWithdrawal(_ context.Context, req SendRequest) (*Transaction, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mapKey := req.UserID + "|" + req.IdempotencyKey
	if id := f.byIDKey[mapKey]; id != "" {
		tx := *f.txs[id]
		return &tx, false, nil
	}
	tx := &Transaction{ID: fmt.Sprintf("tx-%d", len(f.txs)+1), OperationID: "op-" + req.IdempotencyKey, UserID: req.UserID, Network: Network, Direction: DirectionWithdrawal, AmountRaw: lamportsString(req.AmountLamports), Status: StatusCreated, IdempotencyKey: req.IdempotencyKey, RequestHash: req.RequestHash, DestinationAddress: req.ToAddress}
	f.txs[tx.ID] = tx
	f.byIDKey[mapKey] = tx.ID
	copy := *tx
	return &copy, true, nil
}
func (f *fakeStore) reserveWithdrawal(_ context.Context, txID, userID, source string, amount, fee, balance int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var reserved int64
	for _, tx := range f.txs {
		if tx.UserID == userID && tx.Direction == DirectionWithdrawal && activeReservationStatus(tx.Status) {
			reserved += tx.ReservedLamports
		}
	}
	need := amount + fee
	tx := f.txs[txID]
	if tx == nil {
		return fmt.Errorf("missing tx")
	}
	if balance-reserved < need {
		tx.Status = StatusFailedBeforeSubmit
		return ErrInsufficientFunds
	}
	if tx.Status != StatusCreated {
		return fmt.Errorf("reservation conflict")
	}
	tx.Status = StatusReserved
	tx.SourceAddress = source
	tx.FeeLamports = fee
	tx.ReservedLamports = need
	return nil
}
func (f *fakeStore) persistSigned(_ context.Context, txID, signature string, rawTx []byte, feePayer, blockhash string, lastValidBlockHeight int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	tx := f.txs[txID]
	tx.Signature = signature
	tx.SignedRawTx = base64.StdEncoding.EncodeToString(rawTx)
	tx.FeePayer = feePayer
	tx.RecentBlockhash = blockhash
	tx.LastValidBlockHeight = lastValidBlockHeight
	tx.Status = StatusSigned
	return nil
}
func (f *fakeStore) markSubmitStatus(_ context.Context, signature, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tx := range f.txs {
		if tx.Signature == signature {
			tx.Status = status
		}
	}
	return nil
}
func (f *fakeStore) listUserTransactions(context.Context, string, int) ([]Transaction, error) {
	return nil, nil
}
func (f *fakeStore) getUserTransaction(context.Context, string, string) (*Transaction, error) {
	return nil, nil
}
func (f *fakeStore) pendingWithdrawals(context.Context) ([]Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Transaction
	for _, tx := range f.txs {
		if tx.Direction == DirectionWithdrawal && (tx.Status == StatusSigned || tx.Status == StatusSubmitUnknown || tx.Status == StatusSubmitted) {
			out = append(out, *tx)
		}
	}
	return out, nil
}
func (f *fakeStore) updateTransactionStatus(_ context.Context, signature, status string, _ int) error {
	return f.markSubmitStatus(context.Background(), signature, status)
}
func (f *fakeStore) seenReceiveKey(_ context.Context, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.byReceive[key]
	return ok, nil
}
func (f *fakeStore) insertTransaction(_ context.Context, tx Transaction, _ map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := depositReceiveKey(tx)
	if key != "" {
		if _, exists := f.byReceive[key]; exists {
			return nil
		}
		f.byReceive[key] = tx.ID
	}
	tx.ID = fmt.Sprintf("tx-%d", len(f.txs)+1)
	f.txs[tx.ID] = &tx
	return nil
}
func (f *fakeStore) withdrawalCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, tx := range f.txs {
		if tx.Direction == DirectionWithdrawal {
			n++
		}
	}
	return n
}
func (f *fakeStore) signedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, tx := range f.txs {
		if tx.Direction == DirectionWithdrawal && tx.Signature != "" && tx.SignedRawTx != "" {
			n++
		}
	}
	return n
}
func (f *fakeStore) depositCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, tx := range f.txs {
		if tx.Direction == DirectionDeposit {
			n++
		}
	}
	return n
}
func (f *fakeStore) onlyWithdrawal() Transaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tx := range f.txs {
		if tx.Direction == DirectionWithdrawal {
			return *tx
		}
	}
	return Transaction{}
}

func (f *fakeStore) put(tx Transaction) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txs[tx.ID] = &tx
}

func activeReservationStatus(status string) bool {
	return status == StatusCreated || status == StatusReserved || status == StatusSigned || status == StatusSubmitted || status == StatusSubmitUnknown
}
