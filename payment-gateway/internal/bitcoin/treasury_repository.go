package bitcoin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// treasuryStore define as operações de banco para a Treasury BTC.
// Acessa as tabelas btc_treasury_utxos e btc_treasury_operations.
type treasuryStore interface {
	// UTXO management
	UpsertTreasuryUTXO(ctx context.Context, u TreasuryUTXO, minConfirmations int) error
	GetTreasuryConfirmedUTXOs(ctx context.Context, network, address string) ([]TreasuryUTXO, error)
	GetTreasuryActiveTreasuryUTXOs(ctx context.Context, network, address string) ([]TreasuryUTXO, error)
	ReserveTreasuryUTXOs(ctx context.Context, opID string, ids []string) error
	ReleaseTreasuryUTXOs(ctx context.Context, opID string) error
	MarkTreasuryUTXOsSpent(ctx context.Context, spentByTxid string, ids []string) error
	MarkTreasuryUTXOOrphaned(ctx context.Context, id string) error

	// Operation (idempotent claim + status updates)
	ClaimTreasuryOperation(ctx context.Context, op TreasuryOperation) (*TreasuryOperation, bool, error)
	GetTreasuryOperationByOrderID(ctx context.Context, orderID string) (*TreasuryOperation, error)
	UpdateTreasuryOperationProcessing(ctx context.Context, id string) error
	UpdateTreasuryOperationSigned(ctx context.Context, id, txid, rawHex string, feeSats int64) error
	UpdateTreasuryOperationBroadcast(ctx context.Context, id, status string) error
	UpdateTreasuryOperationFailed(ctx context.Context, id, code, message, status string) error
}

// treasuryRepository implementa treasuryStore via database/sql.
type treasuryRepository struct {
	sql *sql.DB
}

// ─── UTXOs ────────────────────────────────────────────────────────────────────

// UpsertTreasuryUTXO insere ou atualiza um UTXO da Treasury.
// Nunca regride status de confirmed → pending.
func (r *treasuryRepository) UpsertTreasuryUTXO(ctx context.Context, u TreasuryUTXO, minConfirmations int) error {
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO btc_treasury_utxos
		  (id, network, address, txid, vout, value_sats, script_pub_key,
		   block_height, confirmations, status, detected_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())
		ON CONFLICT (network, txid, vout) DO UPDATE SET
		  confirmations  = CASE
		                     WHEN btc_treasury_utxos.status IN ('reserved','spent') AND EXCLUDED.confirmations < $11
		                     THEN btc_treasury_utxos.confirmations
		                     ELSE EXCLUDED.confirmations
		                   END,
		  block_height   = COALESCE(NULLIF(EXCLUDED.block_height,0), btc_treasury_utxos.block_height),
		  status         = CASE
		                     WHEN btc_treasury_utxos.status IN ('reserved','spent') THEN btc_treasury_utxos.status
		                     WHEN EXCLUDED.confirmations >= $11 THEN 'confirmed'
		                     WHEN btc_treasury_utxos.status = 'confirmed' AND EXCLUDED.confirmations < $11 THEN 'pending'
		                     ELSE 'pending'
		                   END,
		  confirmed_at   = CASE
		                     WHEN btc_treasury_utxos.confirmed_at IS NULL AND EXCLUDED.confirmations >= $11
		                     THEN now()
		                     ELSE btc_treasury_utxos.confirmed_at
		                   END,
		  updated_at     = now()`,
		u.ID, u.Network, u.Address, u.Txid, u.Vout, u.ValueSats,
		u.ScriptPubKey, u.BlockHeight, u.Confirmations, u.Status,
		minConfirmations,
	)
	return err
}

// GetTreasuryConfirmedUTXOs retorna UTXOs confirmados e não-reservados da Treasury.
// Ordenados por valor decrescente (greedy selection).
func (r *treasuryRepository) GetTreasuryConfirmedUTXOs(ctx context.Context, network, address string) ([]TreasuryUTXO, error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id, network, address, txid, vout, value_sats, script_pub_key,
		       block_height, confirmations, status,
		       COALESCE(reserved_by_op,''), COALESCE(spent_by_txid,''),
		       detected_at, confirmed_at, spent_at, created_at, updated_at
		FROM btc_treasury_utxos
		WHERE network = $1 AND address = $2 AND status = 'confirmed'
		ORDER BY value_sats DESC`,
		network, address,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTreasuryUTXOs(rows)
}

// GetTreasuryActiveTreasuryUTXOs retorna todos os UTXOs ativos (pending+confirmed+reserved) para sync.
func (r *treasuryRepository) GetTreasuryActiveTreasuryUTXOs(ctx context.Context, network, address string) ([]TreasuryUTXO, error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id, network, address, txid, vout, value_sats, script_pub_key,
		       block_height, confirmations, status,
		       COALESCE(reserved_by_op,''), COALESCE(spent_by_txid,''),
		       detected_at, confirmed_at, spent_at, created_at, updated_at
		FROM btc_treasury_utxos
		WHERE network = $1 AND address = $2 AND status IN ('pending','confirmed','reserved')`,
		network, address,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTreasuryUTXOs(rows)
}

// ReserveTreasuryUTXOs marca UTXOs como 'reserved' atomicamente.
// UPDATE ... WHERE status = 'confirmed' garante que dois processos concorrentes
// não reservem o mesmo UTXO (ErrDoubleSpend se algum já foi reservado).
func (r *treasuryRepository) ReserveTreasuryUTXOs(ctx context.Context, opID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	ph := buildPlaceholders(ids)
	args := []interface{}{opID}
	for _, id := range ids {
		args = append(args, id)
	}
	result, err := r.sql.ExecContext(ctx,
		`UPDATE btc_treasury_utxos
		 SET status='reserved', reserved_by_op=$1, updated_at=now()
		 WHERE id IN (`+ph(2)+`) AND status='confirmed'`,
		args...,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if int(n) != len(ids) {
		return ErrDoubleSpend
	}
	return nil
}

// ReleaseTreasuryUTXOs devolve UTXOs reservados para 'confirmed' (em caso de falha pré-broadcast).
func (r *treasuryRepository) ReleaseTreasuryUTXOs(ctx context.Context, opID string) error {
	_, err := r.sql.ExecContext(ctx,
		`UPDATE btc_treasury_utxos
		 SET status='confirmed', reserved_by_op=NULL, updated_at=now()
		 WHERE reserved_by_op=$1 AND status='reserved'`,
		opID,
	)
	return err
}

// MarkTreasuryUTXOsSpent marca UTXOs como gastos após broadcast.
func (r *treasuryRepository) MarkTreasuryUTXOsSpent(ctx context.Context, spentByTxid string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	ph := buildPlaceholders(ids)
	args := []interface{}{spentByTxid}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := r.sql.ExecContext(ctx,
		`UPDATE btc_treasury_utxos
		 SET status='spent', spent_by_txid=$1, spent_at=now(), updated_at=now()
		 WHERE id IN (`+ph(2)+`)`,
		args...,
	)
	return err
}

// MarkTreasuryUTXOOrphaned marca UTXO como orphaned (desapareceu do provider).
func (r *treasuryRepository) MarkTreasuryUTXOOrphaned(ctx context.Context, id string) error {
	_, err := r.sql.ExecContext(ctx,
		`UPDATE btc_treasury_utxos SET status='orphaned', updated_at=now() WHERE id=$1`,
		id,
	)
	return err
}

// ─── Operations ───────────────────────────────────────────────────────────────

// ClaimTreasuryOperation cria a operação idempotente antes de qualquer efeito econômico.
// Se a chave já existir, retorna a operação existente e created=false.
// A constraint UNIQUE(order_id) e UNIQUE(idempotency_key) garantem uma operação por ordem.
func (r *treasuryRepository) ClaimTreasuryOperation(ctx context.Context, op TreasuryOperation) (*TreasuryOperation, bool, error) {
	result, err := r.sql.ExecContext(ctx, `
		INSERT INTO btc_treasury_operations
		  (id, order_id, asset, network, funding_source, treasury_address,
		   destination_address, amount_sats, fee_sats, txid, raw_tx_hash,
		   signer_operation_id, status, idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,0,'','', $9,'pending',$10)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		op.ID, op.OrderID, op.Asset, op.Network, op.FundingSource,
		op.TreasuryAddress, op.DestinationAddress, op.AmountSats,
		op.SignerOperationID, op.IdempotencyKey,
	)
	if err != nil {
		return nil, false, err
	}
	n, _ := result.RowsAffected()
	existing, err := r.GetTreasuryOperationByOrderID(ctx, op.OrderID)
	if err != nil {
		return nil, false, err
	}
	return existing, n == 1, nil
}

// GetTreasuryOperationByOrderID busca a operação pelo order_id.
func (r *treasuryRepository) GetTreasuryOperationByOrderID(ctx context.Context, orderID string) (*TreasuryOperation, error) {
	row := r.sql.QueryRowContext(ctx, `
		SELECT id, order_id, asset, network, funding_source,
		       COALESCE(treasury_address,''), destination_address,
		       amount_sats, fee_sats, COALESCE(txid,''), COALESCE(raw_tx_hash,''),
		       COALESCE(signer_operation_id,''), status,
		       COALESCE(error_code,''), COALESCE(error_message,''),
		       idempotency_key,
		       signed_at, broadcast_at, confirmed_at, created_at, updated_at
		FROM btc_treasury_operations WHERE order_id=$1`, orderID)
	return scanTreasuryOp(row)
}

// UpdateTreasuryOperationProcessing marca a operação como 'processing'.
func (r *treasuryRepository) UpdateTreasuryOperationProcessing(ctx context.Context, id string) error {
	_, err := r.sql.ExecContext(ctx,
		`UPDATE btc_treasury_operations SET status='processing', updated_at=now() WHERE id=$1`, id)
	return err
}

// UpdateTreasuryOperationSigned persiste txid, raw_tx e fee; muda status para 'signed'.
func (r *treasuryRepository) UpdateTreasuryOperationSigned(ctx context.Context, id, txid, rawHex string, feeSats int64) error {
	now := time.Now()
	_, err := r.sql.ExecContext(ctx, `
		UPDATE btc_treasury_operations
		SET status='signed', txid=$2, raw_tx_hash=$3, fee_sats=$4, signed_at=$5, updated_at=$5
		WHERE id=$1`, id, txid, rawHex, feeSats, now)
	return err
}

// UpdateTreasuryOperationBroadcast atualiza o status após broadcast (broadcast | broadcast_unknown).
func (r *treasuryRepository) UpdateTreasuryOperationBroadcast(ctx context.Context, id, status string) error {
	now := time.Now()
	_, err := r.sql.ExecContext(ctx, `
		UPDATE btc_treasury_operations
		SET status=$2, broadcast_at=$3, raw_tx_hash=NULL, updated_at=$3
		WHERE id=$1`, id, status, now)
	return err
}

// UpdateTreasuryOperationFailed registra falha com código e mensagem.
func (r *treasuryRepository) UpdateTreasuryOperationFailed(ctx context.Context, id, code, message, status string) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE btc_treasury_operations
		SET status=$2, error_code=$3, error_message=$4, updated_at=now()
		WHERE id=$1`, id, status, code, message)
	return err
}

// ─── advisory lock ────────────────────────────────────────────────────────────

// TreasuryAdvisoryLockKey retorna um int64 estável para pg_advisory_lock baseado
// no endereço da treasury e na rede — garante lock único por (address, network).
func TreasuryAdvisoryLockKey(address, network string) int64 {
	sum := sha256.Sum256([]byte("btc_treasury|" + address + "|" + network))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

// AcquireTreasuryAdvisoryLock adquire um advisory lock de sessão para a Treasury.
// Usar com cuidado: a conexão deve ser mantida durante toda a operação de spend.
// pg_try_advisory_lock retorna false se já estiver bloqueado por outra sessão.
func AcquireTreasuryAdvisoryLock(ctx context.Context, db *sql.DB, address, network string) (bool, error) {
	var acquired bool
	err := db.QueryRowContext(ctx,
		`SELECT pg_try_advisory_lock($1)`,
		TreasuryAdvisoryLockKey(address, network),
	).Scan(&acquired)
	return acquired, err
}

// ReleaseTreasuryAdvisoryLock libera o advisory lock da Treasury.
func ReleaseTreasuryAdvisoryLock(ctx context.Context, db *sql.DB, address, network string) error {
	_, err := db.ExecContext(ctx,
		`SELECT pg_advisory_unlock($1)`,
		TreasuryAdvisoryLockKey(address, network),
	)
	return err
}

// ─── scan helpers ─────────────────────────────────────────────────────────────

func scanTreasuryUTXOs(rows *sql.Rows) ([]TreasuryUTXO, error) {
	var out []TreasuryUTXO
	for rows.Next() {
		var u TreasuryUTXO
		var confirmedAt, spentAt sql.NullTime
		if err := rows.Scan(
			&u.ID, &u.Network, &u.Address, &u.Txid, &u.Vout, &u.ValueSats,
			&u.ScriptPubKey, &u.BlockHeight, &u.Confirmations, &u.Status,
			&u.ReservedByOp, &u.SpentByTxid,
			&u.DetectedAt, &confirmedAt, &spentAt,
			&u.CreatedAt, &u.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if confirmedAt.Valid {
			u.ConfirmedAt = &confirmedAt.Time
		}
		if spentAt.Valid {
			u.SpentAt = &spentAt.Time
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func scanTreasuryOp(row *sql.Row) (*TreasuryOperation, error) {
	var op TreasuryOperation
	var signedAt, broadcastAt, confirmedAt sql.NullTime
	err := row.Scan(
		&op.ID, &op.OrderID, &op.Asset, &op.Network, &op.FundingSource,
		&op.TreasuryAddress, &op.DestinationAddress,
		&op.AmountSats, &op.FeeSats, &op.Txid, &op.RawTxHash,
		&op.SignerOperationID, &op.Status,
		&op.ErrorCode, &op.ErrorMessage,
		&op.IdempotencyKey,
		&signedAt, &broadcastAt, &confirmedAt,
		&op.CreatedAt, &op.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if signedAt.Valid {
		op.SignedAt = &signedAt.Time
	}
	if broadcastAt.Valid {
		op.BroadcastAt = &broadcastAt.Time
	}
	if confirmedAt.Valid {
		op.ConfirmedAt = &confirmedAt.Time
	}
	return &op, nil
}

// newTreasuryUTXOFromProvider constrói um TreasuryUTXO a partir de um ProviderUTXO.
func newTreasuryUTXOFromProvider(pu ProviderUTXO, address, network, scriptHex string, blockHeight int64, minConfirmations int) TreasuryUTXO {
	confs := 0
	status := UTXOStatusPending
	if pu.Status.Confirmed {
		if blockHeight > 0 && pu.Status.BlockHeight > 0 {
			confs = int(blockHeight - pu.Status.BlockHeight + 1)
		}
		if confs >= minConfirmations {
			status = UTXOStatusConfirmed
		}
	}
	return TreasuryUTXO{
		ID:            uuid.New().String(),
		Network:       network,
		Address:       address,
		Txid:          pu.Txid,
		Vout:          pu.Vout,
		ValueSats:     pu.Value,
		ScriptPubKey:  scriptHex,
		BlockHeight:   pu.Status.BlockHeight,
		Confirmations: confs,
		Status:        status,
		DetectedAt:    time.Now(),
	}
}

// utxoIDsFromTreasury extrai IDs de TreasuryUTXO.
func utxoIDsFromTreasury(utxos []TreasuryUTXO) []string {
	ids := make([]string, len(utxos))
	for i, u := range utxos {
		ids[i] = u.ID
	}
	return ids
}

// treasuryUTXOsToUTXOs converte TreasuryUTXO para o tipo UTXO usado por SelectUTXOs.
// UserID e WalletAddressID são preenchidos com a sentinela da treasury.
func treasuryUTXOsToUTXOs(us []TreasuryUTXO) []UTXO {
	out := make([]UTXO, len(us))
	for i, u := range us {
		out[i] = UTXO{
			ID:              u.ID,
			Network:         u.Network,
			UserID:          treasuryUserID,
			WalletAddressID: u.ID, // único por UTXO; não é endereço HD
			Address:         u.Address,
			Txid:            u.Txid,
			Vout:            u.Vout,
			ValueSats:       u.ValueSats,
			ScriptPubKey:    u.ScriptPubKey,
			BlockHeight:     u.BlockHeight,
			Confirmations:   u.Confirmations,
			Status:          u.Status,
		}
	}
	return out
}

// buildPlaceholders retorna uma função que gera placeholders $startIdx, $startIdx+1, ...
func buildPlaceholders(ids []string) func(startIdx int) string {
	return func(startIdx int) string {
		ph := ""
		for i := range ids {
			if i > 0 {
				ph += ","
			}
			ph += fmt.Sprintf("$%d", startIdx+i)
		}
		return ph
	}
}

// treasuryUserID é a sentinela usada como user_id para operações da Treasury.
// Nunca deve corresponder a um user_id real de usuário.
const treasuryUserID = "__btc_treasury__"

// scriptHexForAddress retorna o scriptPubKey hex de um endereço bech32.
func scriptHexForAddress(address, hrp string) string {
	script, err := ScriptFromAddress(address, hrp)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(script)
}
