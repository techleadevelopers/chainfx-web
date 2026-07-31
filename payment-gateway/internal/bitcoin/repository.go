package bitcoin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"time"
)

// repository implementa todas as operacoes de banco de dados da rail BTC.
// Acessa db.SQL diretamente, mesmo padrao do mobile.mobileQueries.
type repository struct {
	sql *sql.DB
}

func (r *repository) GetOrCreateUserAddress(ctx context.Context, userID, network string, derive func(index int) (BTCAddress, error)) (*BTCAddress, error) {
	tx, err := r.sql.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, btcAddressProvisioningLockKey(userID, network)); err != nil {
		return nil, err
	}

	if addr, err := getUserAddressTx(ctx, tx, userID, network); err != nil || addr != nil {
		return addr, err
	}

	var idx int
	if err := tx.QueryRowContext(ctx, `
		UPDATE btc_wallet_state
		SET next_derivation_index = next_derivation_index + 1,
		    updated_at = now()
		WHERE network = $1
		RETURNING next_derivation_index - 1`,
		network,
	).Scan(&idx); err != nil {
		return nil, fmt.Errorf("btc: erro ao alocar indice de derivacao (rede %s): %w", network, err)
	}

	newAddr, err := derive(idx)
	if err != nil {
		return nil, err
	}
	addr := &BTCAddress{}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO btc_wallet_addresses
		  (id, user_id, network, address, derivation_path, derivation_index, address_type, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'active')
		ON CONFLICT DO NOTHING
		RETURNING id, user_id, network, address, derivation_path, derivation_index,
		          address_type, status, created_at, updated_at`,
		newAddr.ID, newAddr.UserID, newAddr.Network, newAddr.Address,
		newAddr.DerivationPath, newAddr.DerivationIndex, newAddr.AddressType,
	).Scan(&addr.ID, &addr.UserID, &addr.Network, &addr.Address, &addr.DerivationPath, &addr.DerivationIndex, &addr.AddressType, &addr.Status, &addr.CreatedAt, &addr.UpdatedAt)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return addr, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	existing, err := getUserAddressTx(ctx, tx, userID, network)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("btc: conflito ao salvar endereco sem endereco ativo persistido para user=%s network=%s", userID, network)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return existing, nil
}

func getUserAddressTx(ctx context.Context, tx *sql.Tx, userID, network string) (*BTCAddress, error) {
	row := tx.QueryRowContext(ctx, `
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

func btcAddressProvisioningLockKey(userID, network string) int64 {
	sum := sha256.Sum256([]byte(userID + "|" + network))
	return int64(binary.BigEndian.Uint64(sum[:8]))
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
func (r *repository) UpsertUTXO(ctx context.Context, u UTXO, minConfirmations int) error {
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO btc_utxos
		  (id, network, user_id, wallet_address_id, txid, vout, value_sats,
		   script_pub_key, block_height, confirmations, status, detected_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
		ON CONFLICT (network, txid, vout) DO UPDATE SET
		  confirmations   = CASE
		                      WHEN btc_utxos.status IN ('spent','reserved') AND EXCLUDED.confirmations < $13
		                      THEN btc_utxos.confirmations
		                      ELSE EXCLUDED.confirmations
		                    END,
		  block_height    = COALESCE(NULLIF(EXCLUDED.block_height,0), btc_utxos.block_height),
		  status          = CASE
		                      WHEN btc_utxos.status IN ('spent','reserved') AND EXCLUDED.confirmations < $13 THEN 'manual_review'
		                      WHEN btc_utxos.status IN ('spent','reserved') THEN btc_utxos.status
		                      WHEN EXCLUDED.confirmations >= $13 THEN 'confirmed'
		                      WHEN btc_utxos.status = 'confirmed' AND EXCLUDED.confirmations < $13 THEN 'reorg_pending'
		                      ELSE 'pending'
		                    END,
		  confirmed_at    = CASE
		                      WHEN btc_utxos.confirmed_at IS NULL AND EXCLUDED.confirmations >= $13
		                      THEN now()
		                      ELSE btc_utxos.confirmed_at
		                    END,
		  updated_at      = now()`,
		u.ID, u.Network, u.UserID, u.WalletAddressID,
		u.Txid, u.Vout, u.ValueSats,
		u.ScriptPubKey, u.BlockHeight, u.Confirmations, u.Status, minConfirmations,
	)
	return err
}

func (r *repository) GetAddressByID(ctx context.Context, id, network string) (*BTCAddress, error) {
	row := r.sql.QueryRowContext(ctx, `
		SELECT id, user_id, network, address, derivation_path, derivation_index,
		       address_type, status, created_at, updated_at
		FROM btc_wallet_addresses
		WHERE id = $1 AND network = $2
		LIMIT 1`,
		id, network,
	)
	return scanAddress(row)
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

func (r *repository) MarkTransactionUTXOsSpent(ctx context.Context, txID, spentByTxid string) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE btc_utxos u
		SET status='spent', spent_by_txid=$2, spent_at=COALESCE(spent_at, now()), updated_at=now()
		FROM btc_transaction_inputs i
		WHERE i.transaction_id=$1 AND i.utxo_id=u.id
		  AND i.active_spend=true AND u.status IN ('reserved','confirmed')`,
		txID, spentByTxid,
	)
	return err
}

// ─── Transações ───────────────────────────────────────────────────────────────

// ClaimTransaction cria a operação idempotente antes de qualquer efeito econômico.
// Se a chave já existir, retorna a operação existente e created=false.
func (r *repository) ClaimTransaction(ctx context.Context, t BTCTransaction) (*BTCTransaction, bool, error) {
	result, err := r.sql.ExecContext(ctx, `
		INSERT INTO btc_transactions
		  (id, user_id, network, direction, txid, raw_tx_hash, destination_address,
		   amount_sats, fee_sats, fee_rate_sat_vbyte, status, confirmations,
		   block_height, idempotency_key, request_hash)
		VALUES ($1,$2,$3,$4,'','',$5,$6,0,0,'created',0,0,$7,$8)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING`,
		t.ID, t.UserID, t.Network, t.Direction, t.DestinationAddr,
		t.AmountSats, t.IdempotencyKey, t.RequestHash,
	)
	if err != nil {
		return nil, false, err
	}
	n, _ := result.RowsAffected()
	if n == 1 {
		claimed, err := r.GetTransactionByIdempotencyKey(ctx, t.UserID, t.IdempotencyKey)
		return claimed, true, err
	}
	existing, err := r.GetTransactionByIdempotencyKey(ctx, t.UserID, t.IdempotencyKey)
	return existing, false, err
}

// PersistSpendPlan reserva UTXOs e grava inputs/outputs em uma transação DB única.
func (r *repository) PersistSpendPlan(ctx context.Context, txID string, inputs []BTCTransactionInput, outputs []BTCTransactionOutput, feeSats, feeRate int64) error {
	tx, err := r.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	ids := make([]string, len(inputs))
	for i, in := range inputs {
		ids[i] = in.UTXOID
	}
	if err := reserveUTXOsTx(ctx, tx, ids); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE btc_transactions
		SET status='building', fee_sats=$2, fee_rate_sat_vbyte=$3, updated_at=now()
		WHERE id=$1 AND status='created'`,
		txID, feeSats, feeRate,
	); err != nil {
		return err
	}

	for _, in := range inputs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO btc_transaction_inputs
			  (id, transaction_id, utxo_id, txid, vout, value_sats, user_id,
			   wallet_address_id, address, derivation_path, derivation_index,
			   script_pub_key, active_spend)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,true)`,
			in.ID, txID, in.UTXOID, in.Txid, in.Vout, in.ValueSats, in.UserID,
			in.WalletAddressID, in.Address, in.DerivationPath, in.DerivationIndex,
			in.ScriptPubKey,
		); err != nil {
			return err
		}
	}

	for _, out := range outputs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO btc_transaction_outputs
			  (id, transaction_id, vout, address, value_sats, output_type, script_pub_key)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			out.ID, txID, out.Vout, out.Address, out.ValueSats, out.OutputType, out.ScriptPubKey,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func reserveUTXOsTx(ctx context.Context, tx *sql.Tx, ids []string) error {
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
	result, err := tx.ExecContext(ctx,
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

func (r *repository) UpdateTransactionSigned(ctx context.Context, id, txid, rawHex string, feeSats, feeRate int64) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE btc_transactions
		SET status='signed', txid=$2, raw_tx_hash=$3, fee_sats=$4,
		    fee_rate_sat_vbyte=$5, signed_at=now(), updated_at=now()
		WHERE id=$1 AND status IN ('building','signed')`,
		id, txid, rawHex, feeSats, feeRate,
	)
	return err
}

func (r *repository) UpdateTransactionStatus(ctx context.Context, id, status, code, message string) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE btc_transactions
		SET status=$2, error_code=NULLIF($3,''), error_message=NULLIF($4,''), updated_at=now()
		WHERE id=$1 AND status <> 'confirmed'`,
		id, status, code, message,
	)
	return err
}

func (r *repository) ReleaseSpend(ctx context.Context, txID string) error {
	tx, err := r.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE btc_utxos u
		SET status='confirmed', updated_at=now()
		FROM btc_transaction_inputs i
		WHERE i.transaction_id=$1 AND i.utxo_id=u.id
		  AND i.active_spend=true AND u.status='reserved'`,
		txID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE btc_transaction_inputs
		SET active_spend=false
		WHERE transaction_id=$1 AND active_spend=true`,
		txID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

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
		       signed_at, broadcast_at, confirmed_at, created_at, updated_at
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
		       signed_at, broadcast_at, confirmed_at, created_at, updated_at
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
		       signed_at, broadcast_at, confirmed_at, created_at, updated_at
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
		       signed_at, broadcast_at, confirmed_at, created_at, updated_at
		FROM btc_transactions
		WHERE network=$1 AND status IN ('signed','broadcast_unknown','broadcast','pending')
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
		WHERE id=$1 AND status <> 'confirmed'`,
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
		  AND u.status IN ('pending','reorg_pending','confirmed')`,
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
		WHERE id = $1 AND status IN ('pending','reorg_pending','confirmed')`,
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
	var signedAt, broadcastAt, confirmedAt sql.NullTime
	err := row.Scan(
		&t.ID, &t.UserID, &t.Network, &t.Direction,
		&t.Txid, &t.RawTxHash, &t.DestinationAddr,
		&t.AmountSats, &t.FeeSats, &t.FeeRateSatVByte,
		&t.Status, &t.Confirmations, &t.BlockHeight,
		&t.IdempotencyKey, &t.RequestHash,
		&t.ErrorCode, &t.ErrorMessage,
		&signedAt, &broadcastAt, &confirmedAt,
		&t.CreatedAt, &t.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if signedAt.Valid {
		t.SignedAt = &signedAt.Time
	}
	if broadcastAt.Valid {
		t.BroadcastAt = &broadcastAt.Time
	}
	if confirmedAt.Valid {
		t.ConfirmedAt = &confirmedAt.Time
	}
	return &t, err
}
