package bitcoin

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeBTCStore struct {
	mu                  sync.Mutex
	cfg                 *Config
	nextIndex           int
	nextByNetwork       map[string]int
	addresses           map[string]BTCAddress
	addressByUser       map[string]string
	utxos               map[string]UTXO
	utxoByOutpoint      map[string]UTXO
	txs                 map[string]BTCTransaction
	txByKey             map[string]BTCTransaction
	inputs              map[string][]BTCTransactionInput
	outputs             map[string][]BTCTransactionOutput
	activeInput         map[string]string
	claimCreates        int
	signedCount         int
	failAfterDerive     bool
	conflictAfterDerive bool
	derivedNotPersisted []BTCAddress
}

func newFakeBTCStore(cfg *Config) *fakeBTCStore {
	return &fakeBTCStore{
		cfg:            cfg,
		nextByNetwork:  make(map[string]int),
		addresses:      make(map[string]BTCAddress),
		addressByUser:  make(map[string]string),
		utxos:          make(map[string]UTXO),
		utxoByOutpoint: make(map[string]UTXO),
		txs:            make(map[string]BTCTransaction),
		txByKey:        make(map[string]BTCTransaction),
		inputs:         make(map[string][]BTCTransactionInput),
		outputs:        make(map[string][]BTCTransactionOutput),
		activeInput:    make(map[string]string),
	}
}

func (s *fakeBTCStore) mustAddress(t *testing.T, userID string, index int) BTCAddress {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.addressByUser[userID+"|"+string(s.cfg.Network)]; ok {
		return s.addresses[id]
	}
	address, _, path, err := DeriveReceiveAddress(s.cfg, uint32(index))
	if err != nil {
		t.Fatal(err)
	}
	a := BTCAddress{
		ID:              fmt.Sprintf("addr-%s-%d", userID, index),
		UserID:          userID,
		Network:         string(s.cfg.Network),
		Address:         address,
		DerivationPath:  path,
		DerivationIndex: index,
		AddressType:     AddressTypeP2WPKH,
		Status:          "active",
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	s.addresses[a.ID] = a
	s.addressByUser[userID+"|"+a.Network] = a.ID
	return a
}

func (s *fakeBTCStore) scriptForAddress(t *testing.T, address string) string {
	t.Helper()
	script, err := ScriptFromAddress(address, s.cfg.HRP())
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(script)
}

func (s *fakeBTCStore) addUTXO(u UTXO) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.utxos[u.ID] = u
	s.utxoByOutpoint[outpointKey(u.Network, u.Txid, u.Vout)] = u
}

func (s *fakeBTCStore) GetOrCreateUserAddress(_ context.Context, userID, network string, derive func(index int) (BTCAddress, error)) (*BTCAddress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.addressByUser[userID+"|"+network]; ok {
		a := s.addresses[id]
		return &a, nil
	}
	idx := s.nextByNetwork[network]
	s.nextByNetwork[network] = idx + 1
	if network == string(s.cfg.Network) && idx >= s.nextIndex {
		s.nextIndex = idx + 1
	}
	a, err := derive(idx)
	if err != nil {
		return nil, err
	}
	if s.failAfterDerive {
		s.failAfterDerive = false
		s.derivedNotPersisted = append(s.derivedNotPersisted, a)
		return nil, fmt.Errorf("injected insert failure")
	}
	if s.conflictAfterDerive {
		s.conflictAfterDerive = false
		s.derivedNotPersisted = append(s.derivedNotPersisted, a)
		existing, err := derive(idx + 1)
		if err != nil {
			return nil, err
		}
		existing.ID = "conflict-canonical-" + userID + "-" + network
		existing.DerivationIndex = idx + 1
		s.addresses[existing.ID] = existing
		s.addressByUser[existing.UserID+"|"+existing.Network] = existing.ID
		cp := existing
		return &cp, nil
	}
	s.addresses[a.ID] = a
	s.addressByUser[a.UserID+"|"+a.Network] = a.ID
	cp := a
	return &cp, nil
}

func (s *fakeBTCStore) GetUserAddress(_ context.Context, userID, network string) (*BTCAddress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.addressByUser[userID+"|"+network]
	if !ok {
		return nil, nil
	}
	a := s.addresses[id]
	return &a, nil
}

func (s *fakeBTCStore) GetAddressByID(_ context.Context, id, network string) (*BTCAddress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.addresses[id]
	if !ok || a.Network != network {
		return nil, nil
	}
	return &a, nil
}

func (s *fakeBTCStore) GetNextDerivationIndex(_ context.Context, network string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.nextByNetwork[network]
	s.nextByNetwork[network] = idx + 1
	if network == string(s.cfg.Network) && idx >= s.nextIndex {
		s.nextIndex = idx + 1
	}
	return idx, nil
}

func (s *fakeBTCStore) AllocateAddress(_ context.Context, a BTCAddress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addresses[a.ID] = a
	s.addressByUser[a.UserID+"|"+a.Network] = a.ID
	return nil
}

func (s *fakeBTCStore) GetActiveUTXOsByAddress(_ context.Context, walletAddressID, network string) ([]UTXO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []UTXO
	for _, u := range s.utxos {
		if u.WalletAddressID == walletAddressID && u.Network == network &&
			(u.Status == UTXOStatusPending || u.Status == UTXOStatusReorgPending || u.Status == UTXOStatusConfirmed) {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *fakeBTCStore) UpsertUTXO(_ context.Context, u UTXO, minConfirmations int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := outpointKey(u.Network, u.Txid, u.Vout)
	if existing, ok := s.utxoByOutpoint[key]; ok {
		if existing.Status == UTXOStatusReserved || existing.Status == UTXOStatusSpent {
			if u.Confirmations < minConfirmations {
				existing.Status = UTXOStatusManualReview
			}
		} else if u.Confirmations >= minConfirmations {
			existing.Status = UTXOStatusConfirmed
		} else if existing.Status == UTXOStatusConfirmed {
			existing.Status = UTXOStatusReorgPending
		} else {
			existing.Status = UTXOStatusPending
		}
		existing.Confirmations = u.Confirmations
		s.utxos[existing.ID] = existing
		s.utxoByOutpoint[key] = existing
		return nil
	}
	if u.Confirmations >= minConfirmations {
		u.Status = UTXOStatusConfirmed
	} else {
		u.Status = UTXOStatusPending
	}
	s.utxos[u.ID] = u
	s.utxoByOutpoint[key] = u
	return nil
}

func (s *fakeBTCStore) MarkUTXOOrphaned(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.utxos[id]
	u.Status = UTXOStatusOrphaned
	s.utxos[id] = u
	return nil
}

func (s *fakeBTCStore) GetBalance(_ context.Context, userID, network string) (Balance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b Balance
	for _, u := range s.utxos {
		if u.UserID != userID || u.Network != network {
			continue
		}
		switch u.Status {
		case UTXOStatusConfirmed:
			b.ConfirmedSats += u.ValueSats
		case UTXOStatusPending:
			b.PendingSats += u.ValueSats
		case UTXOStatusReserved:
			b.ReservedSats += u.ValueSats
		}
	}
	b.AvailableSats = b.ConfirmedSats
	b.TotalSats = b.ConfirmedSats + b.PendingSats
	return b, nil
}

func (s *fakeBTCStore) GetTodayWithdrawalSats(context.Context, string, string) (int64, error) {
	return 0, nil
}

func (s *fakeBTCStore) GetConfirmedUTXOs(_ context.Context, userID, network string) ([]UTXO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []UTXO
	for _, u := range s.utxos {
		if u.UserID == userID && u.Network == network && u.Status == UTXOStatusConfirmed {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *fakeBTCStore) ClaimTransaction(_ context.Context, t BTCTransaction) (*BTCTransaction, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := t.UserID + "|" + t.IdempotencyKey
	if existing, ok := s.txByKey[key]; ok {
		cp := existing
		return &cp, false, nil
	}
	t.Status = TxStatusCreated
	t.CreatedAt = time.Now()
	s.txs[t.ID] = t
	s.txByKey[key] = t
	s.claimCreates++
	cp := t
	return &cp, true, nil
}

func (s *fakeBTCStore) PersistSpendPlan(_ context.Context, txID string, inputs []BTCTransactionInput, outputs []BTCTransactionOutput, feeSats, feeRate int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, in := range inputs {
		u := s.utxos[in.UTXOID]
		if u.Status != UTXOStatusConfirmed {
			return ErrDoubleSpend
		}
		if _, ok := s.activeInput[in.UTXOID]; ok {
			return ErrDoubleSpend
		}
	}
	for _, in := range inputs {
		u := s.utxos[in.UTXOID]
		u.Status = UTXOStatusReserved
		s.utxos[in.UTXOID] = u
		s.utxoByOutpoint[outpointKey(u.Network, u.Txid, u.Vout)] = u
		s.activeInput[in.UTXOID] = txID
	}
	tx := s.txs[txID]
	tx.Status = TxStatusBuilding
	tx.FeeSats = feeSats
	tx.FeeRateSatVByte = feeRate
	tx.UpdatedAt = time.Now()
	s.txs[txID] = tx
	s.txByKey[tx.UserID+"|"+tx.IdempotencyKey] = tx
	s.inputs[txID] = append([]BTCTransactionInput(nil), inputs...)
	s.outputs[txID] = append([]BTCTransactionOutput(nil), outputs...)
	return nil
}

func (s *fakeBTCStore) UpdateTransactionSigned(_ context.Context, id, txid, rawHex string, feeSats, feeRate int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := s.txs[id]
	tx.Status = TxStatusSigned
	tx.Txid = txid
	tx.RawTxHash = rawHex
	tx.FeeSats = feeSats
	tx.FeeRateSatVByte = feeRate
	now := time.Now()
	tx.SignedAt = &now
	tx.UpdatedAt = now
	s.txs[id] = tx
	s.txByKey[tx.UserID+"|"+tx.IdempotencyKey] = tx
	s.signedCount++
	return nil
}

func (s *fakeBTCStore) UpdateTransactionStatus(_ context.Context, id, status, code, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := s.txs[id]
	if tx.Status == TxStatusConfirmed {
		return nil
	}
	tx.Status = status
	tx.ErrorCode = code
	tx.ErrorMessage = message
	tx.UpdatedAt = time.Now()
	s.txs[id] = tx
	s.txByKey[tx.UserID+"|"+tx.IdempotencyKey] = tx
	return nil
}

func (s *fakeBTCStore) UpdateTransactionError(ctx context.Context, id, code, message, status string) error {
	return s.UpdateTransactionStatus(ctx, id, status, code, message)
}

func (s *fakeBTCStore) UpdateTransactionConfirmations(_ context.Context, id, status string, confs int, blockHeight int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := s.txs[id]
	if tx.Status == TxStatusConfirmed {
		return nil
	}
	tx.Status = status
	tx.Confirmations = confs
	tx.BlockHeight = blockHeight
	if status == TxStatusConfirmed {
		now := time.Now()
		tx.ConfirmedAt = &now
	}
	tx.UpdatedAt = time.Now()
	s.txs[id] = tx
	s.txByKey[tx.UserID+"|"+tx.IdempotencyKey] = tx
	return nil
}

func (s *fakeBTCStore) ReleaseSpend(_ context.Context, txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, in := range s.inputs[txID] {
		u := s.utxos[in.UTXOID]
		if u.Status == UTXOStatusReserved {
			u.Status = UTXOStatusConfirmed
			s.utxos[in.UTXOID] = u
			s.utxoByOutpoint[outpointKey(u.Network, u.Txid, u.Vout)] = u
		}
		delete(s.activeInput, in.UTXOID)
	}
	return nil
}

func (s *fakeBTCStore) MarkUTXOsSpent(_ context.Context, spentByTxid string, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		u := s.utxos[id]
		u.Status = UTXOStatusSpent
		u.SpentByTxid = spentByTxid
		s.utxos[id] = u
		s.utxoByOutpoint[outpointKey(u.Network, u.Txid, u.Vout)] = u
	}
	return nil
}

func (s *fakeBTCStore) MarkTransactionUTXOsSpent(_ context.Context, txID, spentByTxid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, in := range s.inputs[txID] {
		u := s.utxos[in.UTXOID]
		u.Status = UTXOStatusSpent
		u.SpentByTxid = spentByTxid
		s.utxos[in.UTXOID] = u
		s.utxoByOutpoint[outpointKey(u.Network, u.Txid, u.Vout)] = u
	}
	return nil
}

func (s *fakeBTCStore) GetPendingTransactions(_ context.Context, network string) ([]BTCTransaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []BTCTransaction
	for _, tx := range s.txs {
		if tx.Network == network && (tx.Status == TxStatusSigned || tx.Status == TxStatusBroadcastUnknown || tx.Status == TxStatusBroadcast || tx.Status == TxStatusPending) {
			out = append(out, tx)
		}
	}
	return out, nil
}

func (s *fakeBTCStore) GetAllActiveAddresses(_ context.Context, network string) ([]BTCAddress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []BTCAddress
	for _, a := range s.addresses {
		if a.Network == network && a.Status == "active" {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *fakeBTCStore) activeAddressesFor(userID, network string) []BTCAddress {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []BTCAddress
	for _, a := range s.addresses {
		if a.UserID == userID && a.Network == network && a.Status == "active" {
			out = append(out, a)
		}
	}
	return out
}

func (s *fakeBTCStore) ListUserTransactions(_ context.Context, userID, network string, limit int) ([]BTCTransaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []BTCTransaction
	for _, tx := range s.txs {
		if tx.UserID == userID && tx.Network == network {
			out = append(out, tx)
		}
	}
	return out, nil
}

func (s *fakeBTCStore) GetTransactionByTxid(_ context.Context, txid, network string) (*BTCTransaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tx := range s.txs {
		if tx.Txid == txid && tx.Network == network {
			cp := tx
			return &cp, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (s *fakeBTCStore) UpdateWalletState(context.Context, string, int64) error { return nil }

func outpointKey(network, txid string, vout uint32) string {
	return network + "|" + txid + "|" + strconv.Itoa(int(vout))
}
