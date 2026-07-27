// Package bitcoin implementa a rail Bitcoin isolada do backend ChainFX.
// Não toca em nenhum fluxo EVM/BSC/Polygon/NFC/signer existente.
package bitcoin

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Network identifica a rede Bitcoin em uso.
type Network string

const (
	Mainnet Network = "mainnet"
	Testnet Network = "testnet"
	Signet  Network = "signet"
	Regtest Network = "regtest"
)

// Config centraliza toda configuração da rail BTC.
type Config struct {
	Enabled              bool
	Network              Network
	ProviderType         string
	APIURL               string
	APIToken             string
	XPub                 string        // apenas para derivação de endereços (sem chave privada)
	EncryptedSeed        string        // seed/xpriv cifrado com AES-GCM para assinatura
	EncryptionKey        string        // chave hex para AES-GCM
	MinConfirmations     int
	DepositScanInterval  time.Duration
	TxScanInterval       time.Duration
	FeePolicy            string // normal | economy | fast
	FeeTargetBlocks      int
	MinFeeRateSatVB      int64
	MaxFeeRateSatVB      int64
	DustLimitSats        int64
	MaxSendSats          int64 // 0 = sem limite
	DailySendLimitSats   int64 // 0 = sem limite
	HotWalletReserveSats int64
	// Feature flags de segurança operacional
	WithdrawalsEnabled bool // BTC_WITHDRAWALS_ENABLED — false por padrão (opt-in explícito)
	EmergencyLockdown  bool // BTC_EMERGENCY_LOCKDOWN  — bloqueia novos envios sem parar workers de confirmação
}

// LoadBTCConfig lê variáveis de ambiente e retorna a configuração BTC.
// Retorna (nil, nil) se BTC_ENABLED=false — a rail simplesmente não inicia.
func LoadBTCConfig() (*Config, error) {
	enabled := btcEnvBool("BTC_ENABLED", false)
	if !enabled {
		return nil, nil
	}

	net := Network(strings.ToLower(btcEnvStr("BTC_NETWORK", "testnet")))
	switch net {
	case Mainnet, Testnet, Signet, Regtest:
	default:
		return nil, fmt.Errorf("bitcoin: BTC_NETWORK inválido %q — use mainnet, testnet, signet ou regtest", net)
	}

	xpub := btcEnvStr("BTC_XPUB", "")
	if xpub == "" {
		return nil, fmt.Errorf("bitcoin: BTC_XPUB é obrigatório quando BTC_ENABLED=true")
	}

	apiURL := btcEnvStr("BTC_API_URL", "")
	if apiURL == "" {
		apiURL = defaultAPIURL(net)
	}

	cfg := &Config{
		Enabled:              true,
		Network:              net,
		ProviderType:         btcEnvStr("BTC_PROVIDER_TYPE", "mempool"),
		APIURL:               apiURL,
		APIToken:             btcEnvStr("BTC_API_TOKEN", ""),
		XPub:                 xpub,
		EncryptedSeed:        btcEnvStr("BTC_ENCRYPTED_SEED", ""),
		EncryptionKey:        btcEnvStr("BTC_ENCRYPTION_KEY", ""),
		MinConfirmations:     btcEnvInt("BTC_MIN_CONFIRMATIONS", 3),
		DepositScanInterval:  btcEnvDuration("BTC_DEPOSIT_SCAN_INTERVAL", 30*time.Second),
		TxScanInterval:       btcEnvDuration("BTC_TRANSACTION_SCAN_INTERVAL", 30*time.Second),
		FeePolicy:            btcEnvStr("BTC_FEE_POLICY", "normal"),
		FeeTargetBlocks:      btcEnvInt("BTC_FEE_TARGET_BLOCKS", 3),
		MinFeeRateSatVB:      int64(btcEnvInt("BTC_MIN_FEE_RATE_SAT_VB", 1)),
		MaxFeeRateSatVB:      int64(btcEnvInt("BTC_MAX_FEE_RATE_SAT_VB", 200)),
		DustLimitSats:        int64(btcEnvInt("BTC_DUST_LIMIT_SATS", 546)),
		MaxSendSats:          int64(btcEnvInt("BTC_MAX_SEND_SATS", 0)),
		DailySendLimitSats:   int64(btcEnvInt("BTC_DAILY_SEND_LIMIT_SATS", 0)),
		HotWalletReserveSats: int64(btcEnvInt("BTC_HOT_WALLET_RESERVE_SATS", 0)),
		// Segurança operacional — ambos false por padrão (opt-in explícito)
		WithdrawalsEnabled: btcEnvBool("BTC_WITHDRAWALS_ENABLED", false),
		EmergencyLockdown:  btcEnvBool("BTC_EMERGENCY_LOCKDOWN", false),
	}

	// Segurança: mainnet exige configuração explícita
	if cfg.Network == Mainnet {
		if cfg.EncryptedSeed == "" || cfg.EncryptionKey == "" {
			return nil, fmt.Errorf("bitcoin: BTC_ENCRYPTED_SEED e BTC_ENCRYPTION_KEY são obrigatórios em mainnet")
		}
	}

	return cfg, nil
}

// IsMainnet retorna true apenas para mainnet.
func (c *Config) IsMainnet() bool { return c.Network == Mainnet }

// HRP retorna o prefixo bech32 da rede configurada.
func (c *Config) HRP() string {
	switch c.Network {
	case Mainnet:
		return "bc"
	case Testnet:
		return "tb"
	case Signet:
		return "tb"
	case Regtest:
		return "bcrt"
	}
	return "tb"
}

func defaultAPIURL(net Network) string {
	switch net {
	case Mainnet:
		return "https://mempool.space/api"
	case Testnet:
		return "https://mempool.space/testnet/api"
	case Signet:
		return "https://mempool.space/signet/api"
	case Regtest:
		return "http://localhost:3000/api"
	}
	return "https://mempool.space/testnet/api"
}

// ─── env helpers internos ─────────────────────────────────────────────────────

func btcEnvStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func btcEnvBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	}
	return def
}

func btcEnvInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func btcEnvDuration(key string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
