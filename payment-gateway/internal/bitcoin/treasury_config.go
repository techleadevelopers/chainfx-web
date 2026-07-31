package bitcoin

import "fmt"

// TreasuryConfig centraliza a configuração da BTC Treasury operacional da plataforma.
//
// A Treasury é a carteira operacional usada como FONTE PRIORITÁRIA de liquidez
// nos fluxos BUY BTC (web + mobile). É completamente SEPARADA de:
//
//   - BTC_XPUB / BTC_ENCRYPTED_SEED  → wallets HD custodiais dos usuários
//   - SELL_BTC_WALLET_ADDRESS        → endereço de entrada do fluxo SELL web
//   - BTC_HOT_WALLET_RESERVE_SATS    → reserva do hot wallet custodial (semântica diferente:
//                                       protege o saldo dos usuários; a Treasury tem sua
//                                       própria reserva operacional via BTC_TREASURY_MIN_RESERVE_SATS)
//
// NÃO CONFUNDIR: BTC_HOT_WALLET_RESERVE_SATS != BTC_TREASURY_MIN_RESERVE_SATS
//   BTC_HOT_WALLET_RESERVE_SATS → mínimo para operações custodiais dos usuários
//   BTC_TREASURY_MIN_RESERVE_SATS → mínimo operacional da Treasury (independente)
//   Os dois conceitos coexistem e nunca devem ser mesclados silenciosamente.
type TreasuryConfig struct {
	Enabled        bool
	Address        string // bc1q... (mainnet) ou tb1q... (testnet)
	SignerKeyID    string // identificador auditável da chave (ex: "btc_treasury_main")
	EncryptedKey   string // hex AES-GCM: 32 bytes raw privkey cifrada
	EncryptionKey  string // hex 32-byte ou passphrase para AES-GCM (mesmo formato de BTC_ENCRYPTION_KEY)
	MinReserveSats int64  // sats mínimos mantidos na Treasury (nunca gastar abaixo disso)
}

// LoadTreasuryConfig lê as variáveis BTC_TREASURY_* do ambiente.
// Retorna (nil, nil) se BTC_TREASURY_ENABLED=false — a Treasury simplesmente não é ativada.
func LoadTreasuryConfig() (*TreasuryConfig, error) {
	if !btcEnvBool("BTC_TREASURY_ENABLED", false) {
		return nil, nil
	}

	addr := btcEnvStr("BTC_TREASURY_ADDRESS", "")
	if addr == "" {
		return nil, fmt.Errorf("bitcoin/treasury: BTC_TREASURY_ADDRESS é obrigatório quando BTC_TREASURY_ENABLED=true")
	}

	encKey := btcEnvStr("BTC_TREASURY_ENCRYPTED_KEY", "")
	encKeyPass := btcEnvStr("BTC_TREASURY_ENCRYPTION_KEY", "")
	if encKey == "" || encKeyPass == "" {
		return nil, fmt.Errorf("bitcoin/treasury: BTC_TREASURY_ENCRYPTED_KEY e BTC_TREASURY_ENCRYPTION_KEY são obrigatórios quando BTC_TREASURY_ENABLED=true")
	}

	return &TreasuryConfig{
		Enabled:        true,
		Address:        addr,
		SignerKeyID:    btcEnvStr("BTC_TREASURY_SIGNER_KEY_ID", "btc_treasury_main"),
		EncryptedKey:   encKey,
		EncryptionKey:  encKeyPass,
		MinReserveSats: int64(btcEnvInt("BTC_TREASURY_MIN_RESERVE_SATS", 100_000)), // default 0.001 BTC
	}, nil
}
