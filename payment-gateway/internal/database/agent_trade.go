package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

const (
	AgentTradeStatusPending = "pending"
	AgentTradeStatusPaid    = "paid"
	AgentTradeStatusSettled = "settled"
	AgentTradeStatusExpired = "expired"
	AgentTradeStatusFailed  = "failed"
)

type AgentTradeIntent struct {
	ID                   string     `json:"id"`
	AgentWallet          string     `json:"agentWallet"`
	PayAsset             string     `json:"payAsset"`
	ReceiveAsset         string     `json:"receiveAsset"`
	PayAmount            float64    `json:"payAmount"`
	ReceiveAmount        float64    `json:"receiveAmount"`
	ChainFXFeeAmount     float64    `json:"chainfxFeeAmount"`
	FeeBps               int        `json:"feeBps"`
	Network              string     `json:"network"`
	PaymentAddress       string     `json:"paymentAddress"`
	DestinationWallet    string     `json:"destinationWallet"`
	PayTokenContract     string     `json:"payTokenContract"`
	ReceiveTokenContract string     `json:"receiveTokenContract"`
	Nonce                string     `json:"nonce"`
	RequestHash          string     `json:"requestHash"`
	Status               string     `json:"status"`
	TxHash               *string    `json:"txHash,omitempty"`
	ChainID              *int64     `json:"chainId,omitempty"`
	LogIndex             *int       `json:"logIndex,omitempty"`
	BlockNumber          *uint64    `json:"blockNumber,omitempty"`
	BlockHash            *string    `json:"blockHash,omitempty"`
	TransferFrom         *string    `json:"transferFrom,omitempty"`
	TransferTo           *string    `json:"transferTo,omitempty"`
	TransferAmountRaw    *string    `json:"transferAmountRaw,omitempty"`
	OverpaymentAmount    float64    `json:"overpaymentAmount"`
	SettlementTxHash     *string    `json:"settlementTxHash,omitempty"`
	IdempotencyKey       *string    `json:"idempotencyKey,omitempty"`
	ExpiresAt            time.Time  `json:"expiresAt"`
	PaidAt               *time.Time `json:"paidAt,omitempty"`
	SettledAt            *time.Time `json:"settledAt,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
}

type AgentTradeIntentInput struct {
	AgentWallet          string
	PayAsset             string
	ReceiveAsset         string
	PayAmount            float64
	ReceiveAmount        float64
	ChainFXFeeAmount     float64
	FeeBps               int
	Network              string
	PaymentAddress       string
	DestinationWallet    string
	PayTokenContract     string
	ReceiveTokenContract string
	Nonce                string
	RequestHash          string
	TTL                  time.Duration
	IdempotencyKey       string
}

type AgentSupportedAsset struct {
	Symbol          string    `json:"symbol"`
	Network         string    `json:"network"`
	ContractAddress string    `json:"contractAddress"`
	Decimals        int       `json:"decimals"`
	FeeBps          int       `json:"feeBps"`
	MinAmount       float64   `json:"minAmount"`
	Status          string    `json:"status"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"createdAt"`
}

type AgentTradeReceipt struct {
	ChainID           int64
	TxHash            string
	LogIndex          int
	BlockNumber       uint64
	BlockHash         string
	TokenContract     string
	TransferFrom      string
	TransferTo        string
	TransferAmountRaw string
	OverpaymentAmount float64
}

func (db *DB) ListAgentSupportedAssets(ctx context.Context) ([]*AgentSupportedAsset, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT symbol, network, contract_address, decimals, fee_bps, min_amount::float8, status, enabled, created_at
		FROM agent_supported_assets
		ORDER BY enabled DESC, network ASC, symbol ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AgentSupportedAsset
	for rows.Next() {
		asset, err := scanAgentSupportedAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, asset)
	}
	return out, rows.Err()
}

func (db *DB) GetAgentSupportedAsset(ctx context.Context, symbol, network string) (*AgentSupportedAsset, error) {
	row := db.SQL.QueryRowContext(ctx, `
		SELECT symbol, network, contract_address, decimals, fee_bps, min_amount::float8, status, enabled, created_at
		FROM agent_supported_assets
		WHERE symbol = $1 AND network = $2 AND enabled = true`,
		strings.ToUpper(strings.TrimSpace(symbol)), strings.ToUpper(strings.TrimSpace(network)))
	asset, err := scanAgentSupportedAsset(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return asset, err
}

func (db *DB) CreateAgentTradeIntent(ctx context.Context, in AgentTradeIntentInput) (*AgentTradeIntent, error) {
	intent := &AgentTradeIntent{
		ID:                   NewID(),
		AgentWallet:          strings.ToLower(strings.TrimSpace(in.AgentWallet)),
		PayAsset:             strings.ToUpper(strings.TrimSpace(in.PayAsset)),
		ReceiveAsset:         strings.ToUpper(strings.TrimSpace(in.ReceiveAsset)),
		PayAmount:            roundUSDT(in.PayAmount),
		ReceiveAmount:        roundUSDT(in.ReceiveAmount),
		ChainFXFeeAmount:     roundUSDT(in.ChainFXFeeAmount),
		FeeBps:               in.FeeBps,
		Network:              strings.ToUpper(strings.TrimSpace(in.Network)),
		PaymentAddress:       strings.ToLower(strings.TrimSpace(in.PaymentAddress)),
		DestinationWallet:    strings.ToLower(strings.TrimSpace(in.DestinationWallet)),
		PayTokenContract:     strings.ToLower(strings.TrimSpace(in.PayTokenContract)),
		ReceiveTokenContract: strings.ToLower(strings.TrimSpace(in.ReceiveTokenContract)),
		Nonce:                strings.TrimSpace(in.Nonce),
		RequestHash:          strings.TrimSpace(in.RequestHash),
		Status:               AgentTradeStatusPending,
		ExpiresAt:            time.Now().UTC().Add(in.TTL),
	}
	var idempotency any
	if strings.TrimSpace(in.IdempotencyKey) != "" {
		idempotency = strings.TrimSpace(in.IdempotencyKey)
	}
	_, _ = db.SQL.ExecContext(ctx, `
		INSERT INTO agent_wallets (address, first_seen_at, last_seen_at)
		VALUES ($1, now(), now())
		ON CONFLICT (address) DO UPDATE SET last_seen_at = now()`,
		intent.AgentWallet)
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO agent_trade_intents (
		  id, agent_wallet, pay_asset, receive_asset, pay_amount, receive_amount,
		  chainfx_fee_amount, fee_bps, network, payment_address, destination_wallet,
		  pay_token_contract, receive_token_contract, nonce, request_hash, status,
		  expires_at, idempotency_key
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		intent.ID, intent.AgentWallet, intent.PayAsset, intent.ReceiveAsset, intent.PayAmount, intent.ReceiveAmount,
		intent.ChainFXFeeAmount, intent.FeeBps, intent.Network, intent.PaymentAddress, intent.DestinationWallet,
		intent.PayTokenContract, intent.ReceiveTokenContract, intent.Nonce, intent.RequestHash, intent.Status,
		intent.ExpiresAt, idempotency)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" && idempotency != nil {
			return db.GetAgentTradeIntentByIdempotency(ctx, strings.TrimSpace(in.IdempotencyKey))
		}
		return nil, err
	}
	return intent, nil
}

func (db *DB) GetAgentTradeIntent(ctx context.Context, id string) (*AgentTradeIntent, error) {
	row := db.SQL.QueryRowContext(ctx, agentTradeIntentSelect()+` WHERE id = $1`, id)
	intent, err := scanAgentTradeIntent(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return intent, err
}

func (db *DB) GetAgentTradeIntentByIdempotency(ctx context.Context, key string) (*AgentTradeIntent, error) {
	row := db.SQL.QueryRowContext(ctx, agentTradeIntentSelect()+` WHERE idempotency_key = $1`, key)
	intent, err := scanAgentTradeIntent(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return intent, err
}

func (db *DB) ConfirmAgentTradePayment(ctx context.Context, id string, receipt AgentTradeReceipt, idempotencyKey string) (*AgentTradeIntent, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	intent, err := scanAgentTradeIntent(tx.QueryRowContext(ctx, agentTradeIntentSelect()+` WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	if intent.Status == AgentTradeStatusSettled {
		return intent, tx.Commit()
	}
	if intent.Status == AgentTradeStatusPaid {
		return nil, fmt.Errorf("trade intent ja esta em liquidacao")
	}
	if time.Now().UTC().After(intent.ExpiresAt) {
		_, _ = tx.ExecContext(ctx, `UPDATE agent_trade_intents SET status = $2, updated_at = now() WHERE id = $1`, intent.ID, AgentTradeStatusExpired)
		return nil, fmt.Errorf("trade intent expirado")
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_trade_intents
		SET status = $2, tx_hash = $3,
		    chain_id = $4,
		    log_index = $5,
		    block_number = $6,
		    block_hash = $7,
		    transfer_from = $8,
		    transfer_to = $9,
		    transfer_amount_raw = $10,
		    overpayment_amount = $11,
		    idempotency_key = COALESCE(idempotency_key, NULLIF($12,'')),
		    paid_at = COALESCE(paid_at, now()),
		    updated_at = now()
		WHERE id = $1`,
		intent.ID, AgentTradeStatusPaid, strings.ToLower(strings.TrimSpace(receipt.TxHash)),
		receipt.ChainID, receipt.LogIndex, receipt.BlockNumber, strings.ToLower(strings.TrimSpace(receipt.BlockHash)),
		strings.ToLower(strings.TrimSpace(receipt.TransferFrom)), strings.ToLower(strings.TrimSpace(receipt.TransferTo)),
		strings.TrimSpace(receipt.TransferAmountRaw), receipt.OverpaymentAmount, strings.TrimSpace(idempotencyKey))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.GetAgentTradeIntent(ctx, id)
}

func (db *DB) CompleteAgentTradeSettlement(ctx context.Context, id, settlementTxHash string) (*AgentTradeIntent, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	intent, err := scanAgentTradeIntent(tx.QueryRowContext(ctx, agentTradeIntentSelect()+` WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	if intent.Status == AgentTradeStatusSettled {
		return intent, tx.Commit()
	}
	if intent.Status != AgentTradeStatusPaid {
		return nil, fmt.Errorf("trade intent precisa estar pago antes do settlement")
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_trade_intents
		SET status = $2, settlement_tx_hash = $3,
		    settled_at = COALESCE(settled_at, now()),
		    updated_at = now()
		WHERE id = $1`,
		intent.ID, AgentTradeStatusSettled, strings.ToLower(strings.TrimSpace(settlementTxHash)))
	if err != nil {
		return nil, err
	}
	_, _ = tx.ExecContext(ctx, `
		INSERT INTO agent_wallets (address, first_seen_at, last_seen_at, total_spent_usdt)
		VALUES ($1, now(), now(), $2)
		ON CONFLICT (address) DO UPDATE
		SET last_seen_at = now(),
		    total_spent_usdt = agent_wallets.total_spent_usdt + EXCLUDED.total_spent_usdt`,
		intent.AgentWallet, intent.PayAmount)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.GetAgentTradeIntent(ctx, id)
}

func (db *DB) FailAgentTradeSettlement(ctx context.Context, id string) error {
	_, err := db.SQL.ExecContext(ctx, `
		UPDATE agent_trade_intents
		SET status = $2, updated_at = now()
		WHERE id = $1 AND status = $3`,
		id, AgentTradeStatusFailed, AgentTradeStatusPaid)
	return err
}

func agentTradeIntentSelect() string {
	return `SELECT id, agent_wallet, pay_asset, receive_asset, pay_amount::float8, receive_amount::float8,
	       chainfx_fee_amount::float8, fee_bps, network, payment_address, destination_wallet,
	       pay_token_contract, receive_token_contract, nonce, request_hash, status, tx_hash,
	       chain_id, log_index, block_number, block_hash, transfer_from, transfer_to, transfer_amount_raw,
	       overpayment_amount::float8, settlement_tx_hash, idempotency_key, expires_at, paid_at, settled_at, created_at
	FROM agent_trade_intents`
}

func scanAgentTradeIntent(row rowScanner) (*AgentTradeIntent, error) {
	var intent AgentTradeIntent
	var txHash, settlementTxHash, idempotency sql.NullString
	var blockHash, transferFrom, transferTo, transferAmountRaw sql.NullString
	var chainID sql.NullInt64
	var logIndex sql.NullInt64
	var blockNumber sql.NullInt64
	var paidAt, settledAt sql.NullTime
	if err := row.Scan(&intent.ID, &intent.AgentWallet, &intent.PayAsset, &intent.ReceiveAsset,
		&intent.PayAmount, &intent.ReceiveAmount, &intent.ChainFXFeeAmount, &intent.FeeBps,
		&intent.Network, &intent.PaymentAddress, &intent.DestinationWallet,
		&intent.PayTokenContract, &intent.ReceiveTokenContract, &intent.Nonce, &intent.RequestHash,
		&intent.Status, &txHash, &chainID, &logIndex, &blockNumber, &blockHash, &transferFrom,
		&transferTo, &transferAmountRaw, &intent.OverpaymentAmount, &settlementTxHash, &idempotency,
		&intent.ExpiresAt, &paidAt, &settledAt, &intent.CreatedAt); err != nil {
		return nil, err
	}
	if txHash.Valid {
		intent.TxHash = &txHash.String
	}
	if chainID.Valid {
		value := chainID.Int64
		intent.ChainID = &value
	}
	if logIndex.Valid {
		value := int(logIndex.Int64)
		intent.LogIndex = &value
	}
	if blockNumber.Valid {
		value := uint64(blockNumber.Int64)
		intent.BlockNumber = &value
	}
	if blockHash.Valid {
		intent.BlockHash = &blockHash.String
	}
	if transferFrom.Valid {
		intent.TransferFrom = &transferFrom.String
	}
	if transferTo.Valid {
		intent.TransferTo = &transferTo.String
	}
	if transferAmountRaw.Valid {
		intent.TransferAmountRaw = &transferAmountRaw.String
	}
	if settlementTxHash.Valid {
		intent.SettlementTxHash = &settlementTxHash.String
	}
	if idempotency.Valid {
		intent.IdempotencyKey = &idempotency.String
	}
	if paidAt.Valid {
		intent.PaidAt = &paidAt.Time
	}
	if settledAt.Valid {
		intent.SettledAt = &settledAt.Time
	}
	return &intent, nil
}

func scanAgentSupportedAsset(row rowScanner) (*AgentSupportedAsset, error) {
	var asset AgentSupportedAsset
	if err := row.Scan(&asset.Symbol, &asset.Network, &asset.ContractAddress, &asset.Decimals, &asset.FeeBps, &asset.MinAmount, &asset.Status, &asset.Enabled, &asset.CreatedAt); err != nil {
		return nil, err
	}
	return &asset, nil
}
