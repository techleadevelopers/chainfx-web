package bitcoin

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// repository implementa todas as operações de banco de dados da rail BTC.
// Acessa db.SQL diretamente — mesmo padrão do mobile.mobileQueries.
type repository struct {
	sql *sql.DB
}

// ─── Endereços ────────────────────────────────────────────────────────────────

// GetNextDerivationIndex aloca atomicamente o próximo índice HD usando btc_wallet_state.
// UPDATE ... RETURNING garante que dois requests concorrentes nunca recebem o mesmo índice.
// A tabela deve ter sido pré-populada pela migration 028 com uma linha por rede.
func (r *repository) GetNextDerivationIndex(ctx context.Context, network string) (int, error) {
	var idx int
	err := r.sql.QueryRowContext(ctx, `
		UPDATE btc_wallet_state
		SET next_derivation_index = next_derivation_index + 1,
		    updated_at = now()
		WHERE network = $1
		RETURNING next_derivation_index - 1`,
		network,
	).Scan(&idx)
	if err != nil {
		return 0, fmt.Errorf("btc: erro ao alocar índice de derivação (rede %s): %w", network, err)
	}
	return idx, nil
}

// AllocateAddress persiste um novo endereço BTC para o usuário.
// A constraint UNIQUE(network, derivation_index) garante que dois usuários
// nunca recebam o mesmo índice mesmo em concorrência.
func (r *repository) AllocateAddress(ctx context.Context, a BTCAddress) error {
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO btc_wallet_addresses
		  (id, user_id, network, address, derivation_path, derivation_index, address_type, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'active')
		ON CONFLICT DO NOTHING`,
		a.ID, a.UserID, a.Network, a.Address,
		a.DerivationPath, a.DerivationIndex, a.AddressType,
	)
	return err
}

// GetUserAddress retorna o endereço BTC ativo do usuário para a rede informada.
func (r *repository) GetUserAddress(ctx context.Context, userID, network string) (*BTCAddress, error) {
	row := r.sql.QueryRowContext(ctx, `
		SELECT id, user_id, network, address, derivation_path, derivation_index,
		       address_type, status, created_at, updated_at
		FROM btc_wallet_addresses
		WHERE user_id = $1 AND network = $2 AND status = 'active'
		ORDER BY derivation_index ASC
		LIMIT 1`,
		userID, network,
	)
	return scanAddress(row)
}

// GetAllActiveAddresses retorna todos os endereços ativos da rede para o scanner de depósitos.
func (r *repository) GetAllActiveAddresses(ctx context.Context, network string) ([]BTCAddress, error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id, user_id, network, address, derivation_path, derivation_index,
		       address_type, status, created_at, updated_at
		FROM btc_wallet_addresses
		WHERE network = $1 AND status = 'active'`,
		network,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BTCAddress
	for rows.Next() {
		a, err := scanAddress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ─── UTXOs ────────────────────────────────────────────────────────────────────

// UpsertUTXO insere ou atualiza um UTXO. Nunca regride status para pending se já estiver confirmed.
func (r *repository) UpsertUTXO(ctx context.Context, u UTXO) error {
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO btc_utxos
		  (id, network, user_id, wallet_address_id, txid, vout, value_sats,
		   script_pub_key, block_height, confirmations, status, detected_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
		ON CONFLICT (network, txid, vout) DO UPDATE SET
		  confirmations   = GREATEST(btc_utxos.confirmations, EXCLUDED.confirmations),
		  block_height    = COALESCE(NULLIF(EXCLUDED.block_height,0), btc_utxos.block_height),
		  status          = CASE
		                      WHEN btc_utxos.status IN ('spent','reserved') THEN btc_utxos.status
		                      WHEN EXCLUDED.confirmations >= 1 THEN 'confirmed'
		                      ELSE btc_utxos.status
		                    END,
		  confirmed_at    = CASE
		                      WHEN btc_utxos.confirmed_at IS NULL AND EXCLUDED.confirmations >= 1
		                      THEN now()
		                      ELSE btc_utxos.confirmed_at
		                    END,
		  updated_at      = now()`,
		u.ID, u.Network, u.UserID, u.WalletAddressID,
		u.Txid, u.Vout, u.ValueSats,
		u.ScriptPubKey, u.BlockHeight, u.Confirmations, u.Status,
	)
	return err
}

// GetConfirmedUTXOs retorna UTXOs confirmados e não-reservados do usuário.
func (r *repository) GetConfirmedUTXOs(ctx context.Context, userID, network string) ([]UTXO, error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT u.id, u.network, u.user_id, u.wallet_address_id,
		       a.address,
		       u.txid, u.vout, u.value_sats, u.script_pub_key,
		       u.block_height, u.confirmations, u.status,
		       COALESCE(u.spent_by_txid,''),
		       u.detected_at,
		       u.confirmed_at, u.spent_at,
		       u.created_at, u.updated_at
		FROM btc_utxos u
		JOIN btc_wallet_addresses a ON a.id = u.wallet_address_id
		WHERE u.user_id = $1 AND u.network = $2 AND u.status = 'confirmed'
		ORDER BY u.value_sats DESC`,
		userID, network,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUTXOs(rows)
}

// GetBalance soma saldo confirmado, pendente e reservado do usuário.
// available_sats = confirmed - reserved (nunca negativo).
func (r *repository) GetBalance(ctx context.Context, userID, network string) (Balance, error) {
	row := r.sql.QueryRowContext(ctx, `
		SELECT
		  COALESCE(SUM(value_sats) FILTER (WHERE status = 'confirmed'), 0),
		  COALESCE(SUM(value_sats) FILTER (WHERE status = 'pending'),   0),
		  COALESCE(SUM(value_sats) FILTER (WHERE status = 'reserved'),  0)
		FROM btc_utxos
		WHERE user_id = $1 AND network = $2 AND status IN ('confirmed','pending','reserved')`,
		userID, network,
	)
	var confirmed, pending, reserved int64
	if err := row.Scan(&confirmed, &pending, &reserved); err != nil {
		return Balance{}, err
	}
	available := confirmed - reserved
	if available < 0 {
		available = 0
	}
	return Balance{
		ConfirmedSats: confirmed,
		PendingSats:   pending,
		ReservedSats:  reserved,
		AvailableSats: available,
		TotalSats:     confirmed + pending,
		ConfirmedBTC:  satsToBTCString(confirmed),
		PendingBTC:    satsToBTCString(pending),
		AvailableBTC:  satsToBTCString(available),
		UpdatedAt:     time.Now(),
	}, nil
}

// ReserveUTXOs marca UTXOs como 'reserved' para evitar double-spend interno.
// Usa UPDATE ... WHERE status = 'confirmed' para garantir atomicidade.
func (r *repository) ReserveUTXOs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	// Construir placeholders: $1, $2, ...
	ph := make([]interface{}, len(ids))
	placeholders := ""
	for i, id := range ids {
		ph[i] = id
		if i > 0 {
			placeholders += ","
		}
		placeholders += fmt.Sprintf("$%d", i+1)
	}

	result, err := r.sql.ExecContext(ctx,
		`UPDATE btc_utxos SET status='reserved', updated_at=now()
		 WHERE id IN (`+placeholders+`) AND status='confirmed'`,
		ph...,
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

// ReleaseUTXOs devolve UTXOs reservados para 'confirmed' (em caso de falha no broadcast).
func (r *repository) ReleaseUTXOs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	ph := make([]interface{}, len(ids))
	placeholders := ""
	for i, id := range ids {
		ph[i] = id
		if i > 0 {
			placeholders += ","
		}
		placeholders += fmt.Sprintf("$%d", i+1)
	}
	_, err := r.sql.ExecContext(ctx,
		`UPDATE btc_utxos SET status='confirmed', updated_at=now()
		 WHERE id IN (`+placeholders+`) AND status='reserved'`,
		ph...,
	)
	return err
}

// MarkUTXOsSpent marca UTXOs como gastos após broadcast confirmado.
func (r *repository) MarkUTXOsSpent(ctx context.Context, spentByTxid string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	ph := make([]interface{}, len(ids)+1)
	ph[0] = spentByTxid
	placeholders := ""
	for i, id := range ids {
		ph[i+1] = id
		if i > 0 {
			placeholders += ","
		}
		placeholders += fmt.Sprintf("$%d", i+2)
	}
	_, err := r.sql.ExecContext(ctx,
		`UPDATE btc_utxos SET status='spent', spent_by_txid=$1, spent_at=now(), updated_at=now()
		 WHERE id IN (`+placeholders+`)`,
		ph...,
	)
	return err
}

// ─── Transações ───────────────────────────────────────────────────────────────

// SaveTransaction persiste uma transação BTC nova.
func (r *repository) SaveTransaction(ctx context.Context, t BTCTransaction) error {
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO btc_transactions
		  (id, user_id, network, direction, txid, raw_tx_hash, destination_address,
		   amount_sats, fee_sats, fee_rate_sat_vbyte, status, confirmations,
		   block_height, idempotency_key, request_hash, broadcast_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING`,
		t.ID, t.UserID, t.Network, t.Direction, t.Txid, t.RawTxHash,
		t.DestinationAddr, t.AmountSats, t.FeeSats, t.FeeRateSatVByte,
		t.Status, t.Confirmations, t.BlockHeight,
		t.IdempotencyKey, t.RequestHash, t.BroadcastAt,
	)
	return err
}

// GetTransactionByIdempotencyKey busca uma transação pelo par (user_id, idempotency_key).
func (r *repository) GetTransactionByIdempotencyKey(ctx context.Context, userID, key string) (*BTCTransaction, error) {
	row := r.sql.QueryRowContext(ctx, `
		SELECT id, user_id, network, direction, txid, COALESCE(raw_tx_hash,''),
		       COALESCE(destination_address,''), amount_sats, fee_sats,
		       fee_rate_sat_vbyte, status, confirmations, block_height,
		       idempotency_key, COALESCE(request_hash,''),
		       COALESCE(error_code,''), COALESCE(error_message,''),
		       broadcast_at, confirmed_at, created_at, updated_at
		FROM btc_transactions
		WHERE user_id=$1 AND idempotency_key=$2`,
		userID, key,
	)
	return scanTransaction(row)
}

// GetTransactionByTxid busca por txid na rede.
func (r *repository) GetTransactionByTxid(ctx context.Context, txid, network string) (*BTCTransaction, error) {
	row := r.sql.QueryRowContext(ctx, `
		SELECT id, user_id, network, direction, txid, COALESCE(raw_tx_hash,''),
		       COALESCE(destination_address,''), amount_sats, fee_sats,
		       fee_rate_sat_vbyte, status, confirmations, block_height,
		       idempotency_key, COALESCE(request_hash,''),
		       COALESCE(error_code,''), COALESCE(error_message,''),
		       broadcast_at, confirmed_at, created_at, updated_at
		FROM btc_transactions
		WHERE txid=$1 AND network=$2
		LIMIT 1`,
		txid, network,
	)
	return scanTransaction(row)
}

// ListUserTransactions lista transações do usuário com paginação simples.
func (r *repository) ListUserTransactions(ctx context.Context, userID, network string, limit int) ([]BTCTransaction, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id, user_id, network, direction, txid, COALESCE(raw_tx_hash,''),
		       COALESCE(destination_address,''), amount_sats, fee_sats,
		       fee_rate_sat_vbyte, status, confirmations, block_height,
		       idempotency_key, COALESCE(request_hash,''),
		       COALESCE(error_code,''), COALESCE(error_message,''),
		       broadcast_at, confirmed_at, created_at, updated_at
		FROM btc_transactions
		WHERE user_id=$1 AND network=$2
		ORDER BY created_at DESC
		LIMIT $3`,
		userID, network, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BTCTransaction
	for rows.Next() {
		t, err := scanTransaction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// GetPendingTransactions retorna transações aguardando confirmação (para o worker).
func (r *repository) GetPendingTransactions(ctx context.Context, network string) ([]BTCTransaction, error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id, user_id, network, direction, txid, COALESCE(raw_tx_hash,''),
		       COALESCE(destination_address,''), amount_sats, fee_sats,
		       fee_rate_sat_vbyte, status, confirmations, block_height,
		       idempotency_key, COALESCE(request_hash,''),
		       COALESCE(error_code,''), COALESCE(error_message,''),
		       broadcast_at, confirmed_at, created_at, updated_at
		FROM btc_transactions
		WHERE network=$1 AND status IN ('broadcast','pending')
		ORDER BY created_at ASC`,
		network,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BTCTransaction
	for rows.Next() {
		t, err := scanTransaction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// UpdateTransactionConfirmations atualiza o status e confirmações de uma transação.
func (r *repository) UpdateTransactionConfirmations(ctx context.Context, id, status string, confs int, blockHeight int64) error {
	var confirmedAt *time.Time
	if status == TxStatusConfirmed {
		now := time.Now()
		confirmedAt = &now
	}
	_, err := r.sql.ExecContext(ctx, `
		UPDATE btc_transactions SET
		  status=$2, confirmations=$3, block_height=$4,
		  confirmed_at=COALESCE($5, confirmed_at),
		  updated_at=now()
		WHERE id=$1`,
		id, status, confs, blockHeight, confirmedAt,
	)
	return err
}

// UpdateTransactionError registra um erro em uma transação.
func (r *repository) UpdateTransactionError(ctx context.Context, id, code, message, status string) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE btc_transactions SET
		  status=$2, error_code=$3, error_message=$4, updated_at=now()
		WHERE id=$1`,
		id, status, code, message,
	)
	return err
}

// ─── Diário de saques ─────────────────────────────────────────────────────────

// GetTodayWithdrawalSats soma os saques do usuário confirmados hoje (UTC).
// Status 'failed', 'dropped' e 'replaced' são excluídos — não consumiram liquidez.
func (r *repository) GetTodayWithdrawalSats(ctx context.Context, userID, network string) (int64, error) {
	var total int64
	err := r.sql.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount_sats), 0)
		FROM btc_transactions
		WHERE user_id = $1
		  AND network = $2
		  AND direction = 'withdrawal'
		  AND status NOT IN ('failed', 'dropped', 'replaced')
		  AND created_at >= date_trunc('day', now() AT TIME ZONE 'UTC')`,
		userID, network,
	).Scan(&total)
	return total, err
}

// ─── UTXOs por endereço (reorg detection) ─────────────────────────────────────

// GetActiveUTXOsByAddress retorna UTXOs pending/confirmed de um wallet_address_id.
// Usado pelo scanner para detectar UTXOs que desapareceram (reorg/double-spend).
func (r *repository) GetActiveUTXOsByAddress(ctx context.Context, walletAddressID, network string) ([]UTXO, error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT u.id, u.network, u.user_id, u.wallet_address_id,
		       a.address,
		       u.txid, u.vout, u.value_sats, u.script_pub_key,
		       u.block_height, u.confirmations, u.status,
		       COALESCE(u.spent_by_txid,''),
		       u.detected_at,
		       u.confirmed_at, u.spent_at,
		       u.created_at, u.updated_at
		FROM btc_utxos u
		JOIN btc_wallet_addresses a ON a.id = u.wallet_address_id
		WHERE u.wallet_address_id = $1 AND u.network = $2
		  AND u.status IN ('pending','confirmed')`,
		walletAddressID, network,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUTXOs(rows)
}

// MarkUTXOOrphaned marca um UTXO como orphaned — aparecia no DB mas desapareceu
// do provider (reorg ou double-spend externo).
func (r *repository) MarkUTXOOrphaned(ctx context.Context, id string) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE btc_utxos
		SET status = 'orphaned', updated_at = now()
		WHERE id = $1 AND status IN ('pending','confirmed')`,
		id,
	)
	return err
}

// ─── Estado do scanner ────────────────────────────────────────────────────────

// UpdateWalletState atualiza last_scanned_block e last_scan_at após cada ciclo.
// Usa GREATEST para nunca regredir o bloco mesmo em re-execuções paralelas.
func (r *repository) UpdateWalletState(ctx context.Context, network string, lastScannedBlock int64) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE btc_wallet_state
		SET last_scanned_block = GREATEST(last_scanned_block, $2),
		    last_scan_at       = now(),
		    scanner_status     = 'idle',
		    updated_at         = now()
		WHERE network = $1`,
		network, lastScannedBlock,
	)
	return err
}

// ─── Scan helpers ─────────────────────────────────────────────────────────────

type scannable interface {
	Scan(dest ...any) error
}

func scanAddress(row scannable) (*BTCAddress, error) {
	var a BTCAddress
	err := row.Scan(
		&a.ID, &a.UserID, &a.Network, &a.Address,
		&a.DerivationPath, &a.DerivationIndex,
		&a.AddressType, &a.Status,
		&a.CreatedAt, &a.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &a, err
}

func scanUTXOs(rows *sql.Rows) ([]UTXO, error) {
	var out []UTXO
	for rows.Next() {
		var u UTXO
		err := rows.Scan(
			&u.ID, &u.Network, &u.UserID, &u.WalletAddressID,
			&u.Address,
			&u.Txid, &u.Vout, &u.ValueSats, &u.ScriptPubKey,
			&u.BlockHeight, &u.Confirmations, &u.Status,
			&u.SpentByTxid,
			&u.DetectedAt, &u.ConfirmedAt, &u.SpentAt,
			&u.CreatedAt, &u.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func scanTransaction(row scannable) (*BTCTransaction, error) {
	var t BTCTransaction
	err := row.Scan(
		&t.ID, &t.UserID, &t.Network, &t.Direction,
		&t.Txid, &t.RawTxHash, &t.DestinationAddr,
		&t.AmountSats, &t.FeeSats, &t.FeeRateSatVByte,
		&t.Status, &t.Confirmations, &t.BlockHeight,
		&t.IdempotencyKey, &t.RequestHash,
		&t.ErrorCode, &t.ErrorMessage,
		&t.BroadcastAt, &t.ConfirmedAt,
		&t.CreatedAt, &t.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &t, err
}
