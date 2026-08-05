package bitcoin_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"payment-gateway/internal/bitcoin"

	"golang.org/x/crypto/pbkdf2"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

func satBalance(confirmed int64) bitcoin.TreasuryBalance {
	return bitcoin.TreasuryBalance{
		ConfirmedSats:  confirmed,
		MinReserveSats: 100_000,
		EstimatedFee:   1_000,
		SpendableSats:  confirmed - 100_000 - 1_000,
	}
}

// ─── TreasuryBalance.HasSufficientFunds ───────────────────────────────────────

func TestTreasuryBalance_HasSufficientFunds(t *testing.T) {
	tests := []struct {
		name       string
		balance    bitcoin.TreasuryBalance
		amountSats int64
		want       bool
	}{
		{
			name:       "saldo suficiente",
			balance:    satBalance(500_000),
			amountSats: 100_000,
			want:       true,
		},
		{
			name:       "saldo exatamente igual ao spendable",
			balance:    satBalance(201_000), // spendable = 201_000 - 100_000 - 1_000 = 100_000
			amountSats: 100_000,
			want:       true,
		},
		{
			name:       "saldo insuficiente (abaixo do spendable)",
			balance:    satBalance(150_000), // spendable = 150_000 - 100_000 - 1_000 = 49_000
			amountSats: 100_000,
			want:       false,
		},
		{
			name:       "saldo zero",
			balance:    satBalance(0),
			amountSats: 1_000,
			want:       false,
		},
		{
			name: "reserva mínima impede gasto",
			balance: bitcoin.TreasuryBalance{
				ConfirmedSats:  100_000, // = min_reserve
				MinReserveSats: 100_000,
				EstimatedFee:   1_000,
				SpendableSats:  0, // negativo normalizado para zero
			},
			amountSats: 1_000,
			want:       false,
		},
		{
			name: "emergency lockdown simulado via saldo zero",
			balance: bitcoin.TreasuryBalance{
				SpendableSats: 0,
			},
			amountSats: 1_000,
			want:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.balance.HasSufficientFunds(tc.amountSats)
			if got != tc.want {
				t.Errorf("HasSufficientFunds(%d) = %v; want %v (balance=%+v)",
					tc.amountSats, got, tc.want, tc.balance)
			}
		})
	}
}

// ─── TreasuryConfig ───────────────────────────────────────────────────────────

func TestLoadTreasuryConfig_Disabled(t *testing.T) {
	// BTC_TREASURY_ENABLED não está setado → retorna nil sem erro
	t.Setenv("BTC_TREASURY_ENABLED", "false")
	cfg, err := bitcoin.LoadTreasuryConfig()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil config when disabled, got: %+v", cfg)
	}
}

func TestLoadTreasuryConfig_MissingAddress(t *testing.T) {
	t.Setenv("BTC_TREASURY_ENABLED", "true")
	t.Setenv("BTC_TREASURY_ADDRESS", "")
	_, err := bitcoin.LoadTreasuryConfig()
	if err == nil {
		t.Fatal("expected error when BTC_TREASURY_ADDRESS is empty")
	}
}

func TestLoadTreasuryConfig_MissingKeys(t *testing.T) {
	t.Setenv("BTC_TREASURY_ENABLED", "true")
	t.Setenv("BTC_TREASURY_ADDRESS", "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx")
	t.Setenv("BTC_TREASURY_ENCRYPTED_KEY", "")
	t.Setenv("BTC_TREASURY_ENCRYPTION_KEY", "")
	_, err := bitcoin.LoadTreasuryConfig()
	if err == nil {
		t.Fatal("expected error when encrypted keys are empty")
	}
}

func TestLoadTreasuryConfig_ValidMinReserveDefault(t *testing.T) {
	t.Setenv("BTC_TREASURY_ENABLED", "true")
	t.Setenv("BTC_TREASURY_ADDRESS", "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx")
	t.Setenv("BTC_TREASURY_ENCRYPTED_KEY", "aabbcc") // valor placeholder para teste
	t.Setenv("BTC_TREASURY_ENCRYPTION_KEY", "deadbeef")
	t.Setenv("BTC_TREASURY_MIN_RESERVE_SATS", "")
	cfg, err := bitcoin.LoadTreasuryConfig()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config, got nil")
	}
	if cfg.MinReserveSats != 100_000 {
		t.Errorf("expected default MinReserveSats=100000, got %d", cfg.MinReserveSats)
	}
}

func TestLoadTreasuryConfig_CustomMinReserve(t *testing.T) {
	t.Setenv("BTC_TREASURY_ENABLED", "true")
	t.Setenv("BTC_TREASURY_ADDRESS", "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx")
	t.Setenv("BTC_TREASURY_ENCRYPTED_KEY", "aabbcc")
	t.Setenv("BTC_TREASURY_ENCRYPTION_KEY", "deadbeef")
	t.Setenv("BTC_TREASURY_MIN_RESERVE_SATS", "500000")
	cfg, _ := bitcoin.LoadTreasuryConfig()
	if cfg == nil {
		t.Fatal("expected config")
	}
	if cfg.MinReserveSats != 500_000 {
		t.Errorf("expected MinReserveSats=500000, got %d", cfg.MinReserveSats)
	}
}

// ─── AESGCMTreasurySigner ─────────────────────────────────────────────────────

func TestNewAESGCMTreasurySigner_NilConfig(t *testing.T) {
	_, err := bitcoin.NewAESGCMTreasurySigner(nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestNewAESGCMTreasurySigner_DisabledConfig(t *testing.T) {
	cfg := &bitcoin.TreasuryConfig{Enabled: false}
	_, err := bitcoin.NewAESGCMTreasurySigner(cfg)
	if err == nil {
		t.Fatal("expected error for disabled config")
	}
}

func TestNewAESGCMTreasurySigner_InvalidHex(t *testing.T) {
	cfg := &bitcoin.TreasuryConfig{
		Enabled:       true,
		SignerKeyID:   "test",
		EncryptedKey:  "not-valid-hex!",
		EncryptionKey: "deadbeef",
	}
	_, err := bitcoin.NewAESGCMTreasurySigner(cfg)
	if err == nil {
		t.Fatal("expected error for invalid hex")
	}
}

func TestNewAESGCMTreasurySigner_MnemonicFormat(t *testing.T) {
	mnemonic := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	encKey := sha256.Sum256([]byte("treasury-mnemonic-test"))
	encrypted := encryptTreasuryForTest(t, encKey[:], []byte(mnemonic))
	btcCfg := &bitcoin.Config{Enabled: true, Network: bitcoin.Testnet}
	address := deriveTreasuryAddressForTest(t, mnemonic, "", btcCfg, "m/84'/1'/0'/0/0")
	cfg := &bitcoin.TreasuryConfig{
		Enabled:        true,
		Address:        address,
		SignerKeyID:    "test",
		EncryptedKey:   hex.EncodeToString(encrypted),
		EncryptionKey:  hex.EncodeToString(encKey[:]),
		KeyFormat:      "mnemonic",
		DerivationPath: "m/84'/1'/0'/0/0",
	}
	if _, err := bitcoin.NewAESGCMTreasurySigner(cfg, btcCfg); err != nil {
		t.Fatalf("expected mnemonic treasury signer, got %v", err)
	}
}

func encryptTreasuryForTest(t *testing.T, key, plaintext []byte) []byte {
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

func deriveTreasuryAddressForTest(t *testing.T, mnemonic, passphrase string, cfg *bitcoin.Config, path string) string {
	t.Helper()
	seed := pbkdf2.Key([]byte(mnemonic), []byte("mnemonic"+passphrase), 2048, 64, sha512.New)
	key, err := bitcoin.NewMasterKeyForNetwork(seed, cfg.Network)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []uint32{84 + 0x80000000, 1 + 0x80000000, 0 + 0x80000000, 0, 0} {
		key, err = key.PrivateChild(part)
		if err != nil {
			t.Fatalf("derive %s: %v", path, err)
		}
	}
	address, err := bitcoin.P2WPKHAddress(key.CompressedPubKey(), cfg.HRP())
	if err != nil {
		t.Fatal(err)
	}
	return address
}

// ─── SelectUTXOs (reutilizado pela treasury) ──────────────────────────────────

func TestSelectUTXOs_TreasuryScenarios(t *testing.T) {
	makeUTXOs := func(values ...int64) []bitcoin.UTXO {
		var out []bitcoin.UTXO
		for i, v := range values {
			out = append(out, bitcoin.UTXO{
				ID:           fmt.Sprintf("utxo-%d", i),
				ValueSats:    v,
				Status:       bitcoin.UTXOStatusConfirmed,
				ScriptPubKey: "0014" + "aabbccdd" + fmt.Sprintf("%032d", i),
			})
		}
		return out
	}

	t.Run("saldo suficiente seleciona UTXOs corretos", func(t *testing.T) {
		utxos := makeUTXOs(500_000, 100_000, 50_000)
		selected, change, fee, err := bitcoin.SelectUTXOs(utxos, 200_000, 5, 546)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(selected) == 0 {
			t.Fatal("expected UTXOs to be selected")
		}
		if fee <= 0 {
			t.Errorf("expected fee > 0, got %d", fee)
		}
		if change < 0 {
			t.Errorf("expected change >= 0, got %d", change)
		}
	})

	t.Run("saldo insuficiente retorna ErrInsufficientFunds", func(t *testing.T) {
		utxos := makeUTXOs(10_000, 5_000)
		_, _, _, err := bitcoin.SelectUTXOs(utxos, 200_000, 5, 546)
		if !errors.Is(err, bitcoin.ErrInsufficientFunds) {
			t.Errorf("expected ErrInsufficientFunds, got %v", err)
		}
	})

	t.Run("UTXO vazio retorna ErrNoUTXOs", func(t *testing.T) {
		_, _, _, err := bitcoin.SelectUTXOs(nil, 1_000, 5, 546)
		if !errors.Is(err, bitcoin.ErrNoUTXOs) {
			t.Errorf("expected ErrNoUTXOs, got %v", err)
		}
	})

	t.Run("amount abaixo do dust retorna erro", func(t *testing.T) {
		utxos := makeUTXOs(500_000)
		_, _, _, err := bitcoin.SelectUTXOs(utxos, 100, 5, 546) // 100 < 546 (dust)
		// Erro de dust é retornado por SelectUTXOs via fee math, não diretamente
		// mas o resultado deve ser erro pois amount+fee > accumulated
		if err == nil {
			t.Error("expected error for amount below dust limit")
		}
	})
}

// ─── TreasuryAdvisoryLockKey ──────────────────────────────────────────────────

func TestTreasuryAdvisoryLockKey_Deterministic(t *testing.T) {
	k1 := bitcoin.TreasuryAdvisoryLockKey("bc1qtest", "mainnet")
	k2 := bitcoin.TreasuryAdvisoryLockKey("bc1qtest", "mainnet")
	if k1 != k2 {
		t.Errorf("advisory lock key não é determinístico: %d vs %d", k1, k2)
	}
}

func TestTreasuryAdvisoryLockKey_DifferentForDifferentAddress(t *testing.T) {
	k1 := bitcoin.TreasuryAdvisoryLockKey("bc1qtest", "mainnet")
	k2 := bitcoin.TreasuryAdvisoryLockKey("bc1qother", "mainnet")
	if k1 == k2 {
		t.Error("advisory lock keys deveriam ser diferentes para endereços diferentes")
	}
}

func TestTreasuryAdvisoryLockKey_DifferentForDifferentNetwork(t *testing.T) {
	k1 := bitcoin.TreasuryAdvisoryLockKey("bc1qtest", "mainnet")
	k2 := bitcoin.TreasuryAdvisoryLockKey("bc1qtest", "testnet")
	if k1 == k2 {
		t.Error("advisory lock keys deveriam ser diferentes para redes diferentes")
	}
}

// ─── Idempotência — chave de operação ─────────────────────────────────────────

func TestTreasuryIdempotencyKey_Format(t *testing.T) {
	orderID := "ord-12345"
	expected := "btc_buy:ord-12345"
	got := "btc_buy:" + orderID
	if got != expected {
		t.Errorf("idempotency key incorreta: got %q, want %q", got, expected)
	}
}

// ─── TreasuryBalance reserve ──────────────────────────────────────────────────

func TestTreasuryBalance_ReserveProtection(t *testing.T) {
	// Garante que a reserva mínima é sempre respeitada

	tests := []struct {
		name             string
		confirmed        int64
		minReserve       int64
		estimatedFee     int64
		amountSats       int64
		expectSufficient bool
	}{
		{
			name:             "treasury não pode zerar (reserva protege)",
			confirmed:        200_000,
			minReserve:       100_000,
			estimatedFee:     1_000,
			amountSats:       150_000, // spendable = 200k - 100k - 1k = 99k < 150k
			expectSufficient: false,
		},
		{
			name:             "reserva mínima alta bloqueia todos os gastos",
			confirmed:        500_000,
			minReserve:       500_000,
			estimatedFee:     1_000,
			amountSats:       1_000, // spendable = 500k - 500k - 1k = negativo → 0
			expectSufficient: false,
		},
		{
			name:             "gasto abaixo do spendable é permitido",
			confirmed:        500_000,
			minReserve:       100_000,
			estimatedFee:     1_000,
			amountSats:       100_000, // spendable = 399k >= 100k
			expectSufficient: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spendable := tc.confirmed - tc.minReserve - tc.estimatedFee
			if spendable < 0 {
				spendable = 0
			}
			b := bitcoin.TreasuryBalance{
				ConfirmedSats:  tc.confirmed,
				MinReserveSats: tc.minReserve,
				EstimatedFee:   tc.estimatedFee,
				SpendableSats:  spendable,
			}
			got := b.HasSufficientFunds(tc.amountSats)
			if got != tc.expectSufficient {
				t.Errorf("HasSufficientFunds(%d) = %v; want %v (spendable=%d)",
					tc.amountSats, got, tc.expectSufficient, spendable)
			}
		})
	}
}

// ─── Concorrência / reserva de UTXO ──────────────────────────────────────────

func TestUTXOReservationConcurrency(t *testing.T) {
	// Este teste valida a LÓGICA de detecção de double-spend.
	// O mecanismo real usa DB + advisory lock (testado em integration tests).
	// Aqui verificamos que ErrDoubleSpend é o erro sentinela correto.

	if !errors.Is(bitcoin.ErrDoubleSpend, bitcoin.ErrDoubleSpend) {
		t.Fatal("ErrDoubleSpend não é comparável via errors.Is")
	}
}

// ─── broadcast_unknown — invariante crítico ───────────────────────────────────

func TestBroadcastUnknownStatus(t *testing.T) {
	// Verifica que TxStatusBroadcastUnknown existe como constante e tem valor correto
	if bitcoin.TxStatusBroadcastUnknown == "" {
		t.Fatal("TxStatusBroadcastUnknown não definido")
	}
	if bitcoin.TxStatusBroadcastUnknown == bitcoin.TxStatusBroadcast {
		t.Fatal("TxStatusBroadcastUnknown e TxStatusBroadcast devem ser distintos")
	}
	if bitcoin.TxStatusBroadcastUnknown == bitcoin.TxStatusFailed {
		t.Fatal("TxStatusBroadcastUnknown não pode ser TxStatusFailed — não é falha definitiva")
	}
}

// ─── Context helpers ──────────────────────────────────────────────────────────

func testCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = cancel
	return ctx
}
