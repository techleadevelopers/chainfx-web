package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/joho/godotenv"
)

// Config centraliza todas as variáveis do seu .env de forma tipada e segura
type Config struct {
	Environment                   string
	AllowSimulations              bool
	DatabaseURL                   string
	AllowedOrigins                string
	WebhookSecret                 string
	Port                          string
	OrderMinBrl                   float64
	OrderMaxBrl                   float64
	RateLockSec                   int
	PixChargeExpirySec            int
	RateLimitWindowMs             int
	RateLimitMax                  int
	RateLimitBackend              string
	RedisURL                      string
	PenaltyBoxEnabled             bool
	PenaltyViolationLimit         int
	PenaltyViolationWindowSec     int
	PenaltyBaseBanSec             int
	PenaltyEscalatedBanSec        int
	PenaltyMaxBanSec              int
	OrderRateLimitWindowMs        int
	OrderRateLimitMax             int
	FeeBps                        int
	FeeFixedUsd                   float64
	FeePerUsdtUsd                 float64
	FeeMinBrl                     float64
	BuyTier1MinBrl                float64
	BuyTier1MaxBrl                float64
	BuyTier1Bps                   int
	BuyTier2MaxBrl                float64
	BuyTier2Bps                   int
	BuyTier3Bps                   int
	BuyNetworkFeeBrl              float64
	BuyMinFeeBrl                  float64
	BuyRateSpreadBps              int
	BuyBTCBitcoinMinBrl           float64
	BuyBTCBitcoinNetworkFeeBrl    float64
	LiquidityRouterEnabled        bool
	LiquidityQuoteTimeoutMs       int
	LiquidityAllowedPairs         string
	LiquidityAllowedAssets        string
	LiquidityAllowedNetworks      string
	SupportedNetworks             string
	LiquidityHotWalletFirstAssets string
	LiquidityProviderURLs         string
	LiquidityProviderAPIKey       string
	BingXEnabled                  bool
	BingXAPIBaseURL               string
	BingXAPIKey                   string
	BingXAPISecret                string
	BingXRecvWindowMs             int
	BingXAllowedAssets            string
	BingXAllowedNetworks          string
	BingXTakerFeeBps              int
	BingXWithdrawFeeUSDT          float64
	BingXBTCWithdrawFeeBTC        float64
	BingXBTCWithdrawMinBTC        float64
	BingXMarketBuyMode            string
	BingXTradeEnabled             bool
	BingXWithdrawEnabled          bool
	SellRateBps                   int
	SellSpreadMinBps              int
	SellSpreadMaxBps              int
	SellSpreadHighValueBrl        float64
	SellUsdtBrlRate               float64
	SellWalletAddress             string
	SellBTCWalletAddress          string
	BuyHotDerivationIndex         int
	ChainFXLiveSecretKeys         string
	ChainFXTestSecretKeys         string
	ChainFXLivePublicKeys         string
	ChainFXTestPublicKeys         string
	ChainFXRequireAPIKey          bool
	InternalAllowedCIDRs          string
	AdminBootstrapEmail           string
	AdminBootstrapPassword        string
	AdminConsoleKey               string

	// Regras de Limite e Fraude
	PixMaxOrdersPer24h     int
	PixMaxBrlPer24h        float64
	OrderHoldSecForNewDest int
	BscDepositTolerancePct float64
	SellPayoutMode         string

	// Efí Bank Pix
	EfiClientID        string
	EfiClientSecret    string
	EfiPixKey          string
	EfiApiBaseURL      string
	EfiChargesBaseURL  string
	EfiCertificatePath string
	EfiCertificateKey  string
	EfiCertificatePass string
	EfiCertificateP12  string
	EfiPixFeeBps       int
	PixWebhookSecret   string

	// Tesouraria / signer / sweep
	TreasuryHot              string
	TreasuryCold             string
	SignerUrl                string
	SignerNetwork            string
	SignerHmacSecret         string
	BscRpcUrls               string
	BscUsdtContract          string
	BscChainID               int64
	BscTreasuryContract      string
	PolygonRpcUrls           string
	PolygonUsdtContract      string
	PolygonChainID           int64
	PolygonTreasuryContract  string
	BaseRpcUrls              string
	BaseUsdcContract         string
	BaseChainID              int64
	ArbitrumRpcUrls          string
	ArbitrumUsdcContract     string
	ArbitrumChainID          int64
	EthereumRpcUrls          string
	EthereumUsdcContract     string
	EthereumChainID          int64
	SolanaEnabled            bool
	SolanaRpcUrls            string
	SolanaCluster            string
	SolanaWithdrawalsEnabled bool
	SolanaScanIntervalSec    int
	SolanaTxScanIntervalSec  int
	SolanaMinConfirmations   int
	SolanaMaxSendLamports    int64
	EnableSweepWorker        bool
	EnableSweepStub          bool
	SweepBatchUsdtMin        float64
	SweepBatchUsdtMax        float64
	SweepFrequencyMs         int
	BscGasReserveBNB         float64

	// SMTP / mensagens
	SMTPHost       string
	SMTPPort       int
	SMTPUser       string
	SMTPPass       string
	SMTPSecure     bool
	SMTPFromEmail  string
	SMTPFromName   string
	OpsEmail       string
	EmailBrandName string
	EmailLogoURL   string
	EmailSiteURL   string
	EmailAddress   string
	SupportEmail   string

	// LGPD / auditoria
	LGPDSecret string

	// Webhooks
	WebhooksEnabled    bool
	WebhooksMaxRetries int

	// OpenAI / AI Agents
	OpenAIAPIKey  string
	OpenAIModel   string
	OpenAIBaseURL string

	// Capability provider adapters
	CapabilityOCRURL    string
	CapabilityOCRAPIKey string

	// M2M Agent Payments (PIX + credit-card on behalf of AI agents)
	M2MPixFeeBps           int     // default 1000 = 10%
	M2MCreditFeeBps        int     // default 1900 = 19%
	M2MDepositTolerancePct float64 // fraction tolerance for on-chain amount match (default 0.005)
	M2MMaxDailyOutflowBRL  float64 // max BRL settled per 24 h (0 = unlimited)
	M2MDepositAddresses    string  // comma-separated unique deposit addresses for pending M2M intents

	// Closed-loop NFC wallet-to-terminal rail.
	NFCEnabled                   bool
	NFCTokenSecret               string
	NFCTokenTTLSeconds           int
	NFCHoldTTLSeconds            int
	NFCMaxAmountBRL              float64
	NFCTerminals                 string
	NFCPriceMaxAgeSec            int
	NFCFeeBps                    int
	NFCSettlementMode            string
	NFCEfiMinBufferBRL           float64
	NFCEfiBalanceBRL             float64
	NFCLiquidityPolicyEnabled    bool
	NFCTreasurySnapshotMaxAgeSec int

	// EIP-712 typed intents for MCP/mobile/stablecoin rails.
	EIP712DomainName        string
	EIP712DomainVersion     string
	EIP712ChainID           int64
	EIP712VerifyingContract string
	EIPProbeEnabled         bool
	EIPProbeRealRun         bool
	EIPProbeAllowMainnet    bool
	EIPProbeNetwork         string
	EIPProbeRPCUrls         string
	EIPProbeRelayerPrivKey  string
	EIPProbeWalletPrivKeys  string
	EIPProbeUSDTContract    string
	EIPProbeAsset           string
	EIPProbeRail            string
	EIPProbeTokenContract   string
	EIPProbeTokenName       string
	EIPProbeTokenSymbol     string
	EIPProbeTokenVersion    string
	EIPProbeAmountRaw       string
	EIPProbeConfirmTimeout  int
	EIPProbeExpectedGas3009 uint64

	// On-chain reorg protection — minimum block confirmations before accepting a deposit.
	// BSC is finalistic with low reorg depth; Polygon can have deep reorgs.
	BSCMinConfirmations     uint64 // default 6
	PolygonMinConfirmations uint64 // default 128

	// Gas Station (Paymaster) — subsidise gas on behalf of AI agents.
	// Agents submit EIP-712 permits; ChainFX pays BNB/POL gas and deducts
	// the equivalent USDT fee from the user's on-chain balance.
	GasStationEnabled          bool
	GasStationSurchargeBps     int     // extra fee margin on top of raw gas cost (default 1000 = 10%)
	GasStationMaxFeeUsdt       float64 // per-relay USDT fee ceiling (default 2.00)
	GasStationMinFeeUsdt       float64 // per-relay USDT fee floor   (default 0.05)
	PaymasterMulticallContract string  // Multicall3 address (empty → 0xcA11bde…CA11)
	// TokenRelayer spread — hidden fee retained from the token amount before delivery.
	// The user pays "Taxa Zero"; ChainFX earns via the embedded spread.
	// Default 100 bps = 1 % (e.g. user moves 100 USDT, 1 USDT stays in TreasuryHot).
	PaymasterPrivKey        string // hex ECDSA key for the paymaster hot wallet (optional)
	PaymasterRelaySpreadBps int    // spread in BPS captured per relay (default 100 = 1 %)

	// Auto-Sweeper — moves hot wallet excess USDT to cold wallet / Gnosis Safe.
	AutoSweeperEnabled     bool
	AutoSweeperIntervalSec int     // polling interval in seconds (default 60)
	AutoSweeperHotMaxUsdt  float64 // sweep threshold — trigger when balance exceeds this (default 5000)
	AutoSweeperHotMinUsdt  float64 // minimum reserve to keep in hot wallet after sweep (default 500)
}

// LoadConfig é o cara que lê o .env e joga para dentro da estrutura acima
func LoadConfig() *Config {
	if err := godotenv.Load(); err != nil {
		log.Println("Aviso: Arquivo .env não encontrado, usando variáveis de ambiente do sistema")
	}

	return &Config{
		Environment:                   getEnv("APP_ENV", getEnv("GO_ENV", "development")),
		AllowSimulations:              getEnvAsBool("ALLOW_SIMULATIONS", true),
		DatabaseURL:                   getEnv("DATABASE_URL", ""),
		AllowedOrigins:                getEnv("ALLOWED_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173,https://swapped-cryptocurrensy.vercel.app,https://www.chainfx.store,https://chainfx.store,https://chatgpt.com,https://chat.openai.com,https://codex.openai.com"),
		WebhookSecret:                 getEnv("WEBHOOK_SECRET", ""),
		Port:                          getEnv("PORT", "8080"),
		OrderMinBrl:                   getEnvAsFloat("ORDER_MIN_BRL", 10.0),
		OrderMaxBrl:                   getEnvAsFloat("ORDER_MAX_BRL", 10000.0),
		RateLockSec:                   getEnvAsInt("RATE_LOCK_SEC", 300),
		PixChargeExpirySec:            getEnvAsInt("PIX_CHARGE_EXPIRY_SEC", 1800),
		RateLimitWindowMs:             getEnvAsInt("RATE_LIMIT_WINDOW_MS", 60000),
		RateLimitMax:                  getEnvAsInt("RATE_LIMIT_MAX", 100),
		RateLimitBackend:              strings.ToLower(getEnv("RATE_LIMIT_BACKEND", "memory")),
		RedisURL:                      getEnv("REDIS_URL", ""),
		PenaltyBoxEnabled:             getEnvAsBool("PENALTY_BOX_ENABLED", true),
		PenaltyViolationLimit:         getEnvAsInt("PENALTY_BOX_VIOLATION_LIMIT", 10),
		PenaltyViolationWindowSec:     getEnvAsInt("PENALTY_BOX_VIOLATION_WINDOW_SEC", 120),
		PenaltyBaseBanSec:             getEnvAsInt("PENALTY_BOX_BASE_BAN_SEC", 900),
		PenaltyEscalatedBanSec:        getEnvAsInt("PENALTY_BOX_ESCALATED_BAN_SEC", 3600),
		PenaltyMaxBanSec:              getEnvAsInt("PENALTY_BOX_MAX_BAN_SEC", 86400),
		OrderRateLimitWindowMs:        getEnvAsInt("ORDER_RATE_LIMIT_WINDOW_MS", 60000),
		OrderRateLimitMax:             getEnvAsInt("ORDER_RATE_LIMIT_MAX", 20),
		FeeBps:                        getEnvAsInt("FEE_BPS", getEnvAsInt("TRANSACTION_FEE_BPS", 200)),
		FeeFixedUsd:                   getEnvAsFloat("FEE_FIXED_USD", getEnvAsFloat("TRANSACTION_FEE_FIXED_USD", 0)),
		FeePerUsdtUsd:                 getEnvAsFloat("FEE_PER_USDT_USD", 0.03),
		FeeMinBrl:                     getEnvAsFloat("FEE_MIN_BRL", 0),
		BuyTier1MinBrl:                getEnvAsFloat("FEE_BUY_TIER_1_MIN", 20),
		BuyTier1MaxBrl:                getEnvAsFloat("FEE_BUY_TIER_1_MAX", 100),
		BuyTier1Bps:                   getEnvPercentAsBps("FEE_BUY_TIER_1_PERCENT", getEnvAsInt("FEE_BUY_TIER_1_BPS", 750)),
		BuyTier2MaxBrl:                getEnvAsFloat("FEE_BUY_TIER_2_MAX", 500),
		BuyTier2Bps:                   getEnvPercentAsBps("FEE_BUY_TIER_2_PERCENT", getEnvAsInt("FEE_BUY_TIER_2_BPS", 550)),
		BuyTier3Bps:                   getEnvPercentAsBps("FEE_BUY_TIER_3_PERCENT", getEnvAsInt("FEE_BUY_TIER_3_BPS", 450)),
		BuyNetworkFeeBrl:              getEnvAsFloat("FEE_BUY_NETWORK_BRL", getEnvAsFloat("FEE_BUY_TIER_1_FIXED", getEnvAsFloat("FEE_BUY_TIER_FIXED", 1.99))),
		BuyMinFeeBrl:                  getEnvAsFloat("FEE_BUY_MIN_TOTAL", getEnvAsFloat("FEE_MIN_BRL", 4.99)),
		BuyRateSpreadBps:              getEnvPercentAsBps("FEE_BUY_SPREAD_PERCENT", getEnvAsInt("FEE_BUY_SPREAD_BPS", 100)),
		BuyBTCBitcoinMinBrl:           getEnvAsFloat("BUY_BTC_BITCOIN_MIN_BRL", 20),
		BuyBTCBitcoinNetworkFeeBrl:    getEnvAsFloat("BUY_BTC_BITCOIN_NETWORK_FEE_BRL", 20),
		LiquidityRouterEnabled:        getEnvAsBool("LIQUIDITY_ROUTER_ENABLED", false),
		LiquidityQuoteTimeoutMs:       getEnvAsInt("LIQUIDITY_QUOTE_TIMEOUT_MS", 2500),
		LiquidityAllowedPairs:         strings.ToUpper(getEnv("LIQUIDITY_ALLOWED_PAIRS", "USDT:BSC,BTC:BITCOIN::8,BTC:BSC:0x7130d2A12B9BCbFAe4f2634d864A1Ee1Ce3Ead9c:18,BNB:BSC::18,SOL:SOLANA::9,USDC:BASE:0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913:6,ETH:BASE::18,USDC:ARBITRUM:0xaf88d065e77c8cC2239327C5EDb3A432268e5831:6,ETH:ARBITRUM::18,USDC:ETHEREUM:0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48:6,ETH:ETHEREUM::18,USDT:ETHEREUM:0xdAC17F958D2ee523a2206206994597C13D831ec7:6,USDT:ARBITRUM:0xfd086bc7CD5C481DCC9C85ebe478A1C0b69FCbb9:6,LINK:ETHEREUM:0x514910771AF9Ca656af840dff83E8264EcF986CA:18,LINK:ARBITRUM:0xf97f4df75117a78c1A5a0DBb814Af92458539FB4:18")),
		LiquidityAllowedAssets:        strings.ToUpper(getEnv("LIQUIDITY_ALLOWED_ASSETS", "USDT,BTC,BNB,SOL,USDC,ETH")),
		LiquidityAllowedNetworks:      strings.ToUpper(getEnv("LIQUIDITY_ALLOWED_NETWORKS", "BSC,POLYGON,BITCOIN,SOLANA,BASE,ARBITRUM,ETHEREUM")),
		SupportedNetworks:             strings.ToUpper(getEnv("SUPPORTED_NETWORKS", getEnv("LIQUIDITY_ALLOWED_NETWORKS", "BSC,POLYGON,BITCOIN,SOLANA,BASE,ARBITRUM,ETHEREUM"))),
		LiquidityHotWalletFirstAssets: strings.ToUpper(getEnv("LIQUIDITY_ROUTER_HOT_WALLET_FIRST_ASSETS", getEnv("LIQUIDITY_ROUTER_SKIP_ASSETS", "USDT"))),
		LiquidityProviderURLs:         getEnv("LIQUIDITY_PROVIDER_URLS", ""),
		LiquidityProviderAPIKey:       getEnv("LIQUIDITY_PROVIDER_API_KEY", ""),
		BingXEnabled:                  getEnvAsBool("BINGX_ENABLED", false),
		BingXAPIBaseURL:               getEnv("BINGX_API_BASE_URL", "https://open-api.bingx.com"),
		BingXAPIKey:                   getEnv("BINGX_API_KEY", ""),
		BingXAPISecret:                getEnv("BINGX_API_SECRET", ""),
		BingXRecvWindowMs:             getEnvAsInt("BINGX_RECV_WINDOW_MS", 5000),
		BingXAllowedAssets:            strings.ToUpper(getEnv("BINGX_ALLOWED_ASSETS", "BTC,ETH,BNB,SOL,LINK,AVAX")),
		BingXAllowedNetworks:          strings.ToUpper(getEnv("BINGX_ALLOWED_NETWORKS", "BITCOIN,BSC,POLYGON,SOLANA,BASE,ARBITRUM,ETHEREUM")),
		BingXTakerFeeBps:              getEnvAsInt("BINGX_TAKER_FEE_BPS", 10),
		BingXWithdrawFeeUSDT:          getEnvAsFloat("BINGX_WITHDRAW_FEE_USDT", 0),
		BingXBTCWithdrawFeeBTC:        getEnvAsFloat("BINGX_BTC_WITHDRAW_FEE_BTC", 0.00005),
		BingXBTCWithdrawMinBTC:        getEnvAsFloat("BINGX_BTC_WITHDRAW_MIN_BTC", 0.000056),
		BingXMarketBuyMode:            strings.ToLower(getEnv("BINGX_MARKET_BUY_MODE", "quantity")),
		BingXTradeEnabled:             getEnvAsBool("BINGX_TRADE_ENABLED", false),
		BingXWithdrawEnabled:          getEnvAsBool("BINGX_WITHDRAW_ENABLED", false),
		SellRateBps:                   getEnvAsInt("SELL_RATE_BPS", 0),
		SellSpreadMinBps:              getEnvPercentAsBps("FEE_SELL_SPREAD_MIN", getEnvAsInt("FEE_SELL_SPREAD_MIN_BPS", 800)),
		SellSpreadMaxBps:              getEnvPercentAsBps("FEE_SELL_SPREAD_MAX", getEnvAsInt("FEE_SELL_SPREAD_MAX_BPS", 1000)),
		SellSpreadHighValueBrl:        getEnvAsFloat("FEE_SELL_HIGH_VALUE_BRL", 1000),
		SellUsdtBrlRate:               getEnvAsFloat("SELL_USDT_BRL_RATE", 0),
		SellWalletAddress:             getEnv("SELL_WALLET_ADDRESS", "0x7e3BF3FDfeF16040CE3ec60A663381766d3dB375"),
		SellBTCWalletAddress:          getEnv("SELL_BTC_WALLET_ADDRESS", "bc1qpu935hr3mx7zttznlemxv4xmqflg96fex6n0rn"),
		BuyHotDerivationIndex:         getEnvAsInt("BUY_HOT_DERIVATION_INDEX", 0),
		ChainFXLiveSecretKeys:         getEnv("CHAINFX_LIVE_SECRET_KEYS", ""),
		ChainFXTestSecretKeys:         getEnv("CHAINFX_TEST_SECRET_KEYS", "sk_test_chainfx_local"),
		ChainFXLivePublicKeys:         getEnv("CHAINFX_LIVE_PUBLIC_KEYS", ""),
		ChainFXTestPublicKeys:         getEnv("CHAINFX_TEST_PUBLIC_KEYS", "pk_test_chainfx_local"),
		ChainFXRequireAPIKey:          getEnvAsBool("CHAINFX_REQUIRE_API_KEY", false),
		InternalAllowedCIDRs:          getEnv("INTERNAL_ALLOWED_CIDRS", "127.0.0.1/32,::1/128"),
		AdminBootstrapEmail:           getEnv("ADMIN_BOOTSTRAP_EMAIL", "paulo@chainfx.com"),
		AdminBootstrapPassword:        getEnv("ADMIN_BOOTSTRAP_PASSWORD", ""),
		AdminConsoleKey:               getEnv("ADMIN_CONSOLE_KEY", ""),

		PixMaxOrdersPer24h:     getEnvAsInt("PIX_MAX_ORDERS_PER_24H", 5),
		PixMaxBrlPer24h:        getEnvAsFloat("PIX_MAX_BRL_PER_24H", 20000.0),
		OrderHoldSecForNewDest: getEnvAsInt("ORDER_HOLD_SEC_FOR_NEW_DEST", 180),
		BscDepositTolerancePct: getEnvAsFloat("BSC_DEPOSIT_TOLERANCE_PCT", 0.02),
		SellPayoutMode:         strings.ToLower(getEnv("SELL_PAYOUT_MODE", "manual")),

		EfiClientID:        getEnv("EFI_CLIENT_ID", ""),
		EfiClientSecret:    getEnv("EFI_CLIENT_SECRET", ""),
		EfiPixKey:          getEnv("EFI_PIX_KEY", ""),
		EfiApiBaseURL:      getEnv("EFI_API_BASE_URL", "https://pix.api.efipay.com.br"),
		EfiChargesBaseURL:  getEnv("EFI_CHARGES_API_BASE_URL", efiChargesDefaultBaseURL(getEnv("APP_ENV", getEnv("GO_ENV", "development")))),
		EfiCertificatePath: getEnv("EFI_CERTIFICATE_PATH", ""),
		EfiCertificateKey:  getEnv("EFI_CERTIFICATE_KEY_PATH", ""),
		EfiCertificatePass: getEnv("EFI_CERTIFICATE_PASSWORD", ""),
		EfiCertificateP12:  getEnv("EFI_CERTIFICATE_P12_BASE64", ""),
		EfiPixFeeBps:       getEnvAsInt("EFI_PIX_FEE_BPS", 119),
		PixWebhookSecret:   getEnv("PIX_WEBHOOK_SECRET", ""),

		TreasuryHot:              getEnv("TREASURY_HOT", ""),
		TreasuryCold:             getEnv("TREASURY_COLD", ""),
		SignerUrl:                getEnv("SIGNER_URL", ""),
		SignerNetwork:            strings.ToLower(getEnv("SIGNER_NETWORK", "bsc")),
		SignerHmacSecret:         getEnv("SIGNER_HMAC_SECRET", ""),
		BscRpcUrls:               getBscRpcUrls(),
		BscUsdtContract:          getEnv("BSC_USDT_CONTRACT", getEnv("BSC_TOKEN_CONTRACT", "")),
		BscChainID:               int64(getEnvAsInt("BSC_CHAIN_ID", 56)),
		BscTreasuryContract:      getEnv("BSC_TREASURY_CONTRACT", ""),
		PolygonRpcUrls:           getPolygonRpcUrls(),
		PolygonUsdtContract:      getEnv("POLYGON_USDT_CONTRACT", getEnv("POLYGON_TOKEN_CONTRACT", "")),
		PolygonChainID:           int64(getEnvAsInt("POLYGON_CHAIN_ID", 137)),
		PolygonTreasuryContract:  getEnv("POLYGON_TREASURY_CONTRACT", ""),
		BaseRpcUrls:              getChainRpcUrls("BASE", []string{"BASE_RPC_URLS", "BASE_RPC_URL", "ALCHEMY_BASE_RPC_URL_1", "ALCHEMY_BASE_RPC_URL_2", "ALCHEMY_BASE_RPC_URL", "ALCHEMY_BASE_FALLBACK_RPC_URL"}),
		BaseUsdcContract:         getEnv("BASE_USDC_CONTRACT", "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"),
		BaseChainID:              int64(getEnvAsInt("BASE_CHAIN_ID", 8453)),
		ArbitrumRpcUrls:          getChainRpcUrls("ARBITRUM", []string{"ARBITRUM_RPC_URLS", "ARBITRUM_RPC_URL", "ARB_RPC_URLS", "ARB_RPC_URL", "ALCHEMY_ARBITRUM_RPC_URL_1", "ALCHEMY_ARBITRUM_RPC_URL_2", "ALCHEMY_ARBITRUM_RPC_URL", "ALCHEMY_ARBITRUM_FALLBACK_RPC_URL"}),
		ArbitrumUsdcContract:     getEnv("ARBITRUM_USDC_CONTRACT", "0xaf88d065e77c8cC2239327C5EDb3A432268e5831"),
		ArbitrumChainID:          int64(getEnvAsInt("ARBITRUM_CHAIN_ID", 42161)),
		EthereumRpcUrls:          getChainRpcUrls("ETHEREUM", []string{"ETHEREUM_RPC_URLS", "ETHEREUM_RPC_URL", "ETH_RPC_URLS", "ETH_RPC_URL", "ALCHEMY_ETHEREUM_RPC_URL_1", "ALCHEMY_ETHEREUM_RPC_URL_2", "ALCHEMY_ETHEREUM_RPC_URL", "ALCHEMY_ETHEREUM_FALLBACK_RPC_URL"}),
		EthereumUsdcContract:     getEnv("ETHEREUM_USDC_CONTRACT", "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
		EthereumChainID:          int64(getEnvAsInt("ETHEREUM_CHAIN_ID", 1)),
		SolanaEnabled:            getEnvAsBool("SOLANA_ENABLED", true),
		SolanaRpcUrls:            getChainRpcUrls("SOLANA", []string{"SOLANA_RPC_URLS", "SOLANA_RPC_URL", "HELIUS_SOLANA_RPC_URL", "QUICKNODE_SOLANA_RPC_URL", "CHAINSTACK_SOLANA_RPC_URL"}),
		SolanaCluster:            strings.ToLower(getEnv("SOLANA_CLUSTER", "mainnet")),
		SolanaWithdrawalsEnabled: getEnvAsBool("SOLANA_WITHDRAWALS_ENABLED", false),
		SolanaScanIntervalSec:    getEnvAsInt("SOLANA_SCAN_INTERVAL_SEC", 30),
		SolanaTxScanIntervalSec:  getEnvAsInt("SOLANA_TX_SCAN_INTERVAL_SEC", 20),
		SolanaMinConfirmations:   getEnvAsInt("SOLANA_MIN_CONFIRMATIONS", 1),
		SolanaMaxSendLamports:    int64(getEnvAsInt("SOLANA_MAX_SEND_LAMPORTS", 0)),
		EnableSweepWorker:        getEnvAsBool("ENABLE_SWEEP_WORKER", false),
		EnableSweepStub:          getEnvAsBool("ENABLE_SWEEP_STUB", false),
		SweepBatchUsdtMin:        getEnvAsFloat("SWEEP_BATCH_USDT_MIN", 0),
		SweepBatchUsdtMax:        getEnvAsFloat("SWEEP_BATCH_USDT_MAX", 1_000_000),
		SweepFrequencyMs:         getEnvAsInt("SWEEP_FREQUENCY_MS", 80800),
		BscGasReserveBNB:         getEnvAsFloat("BSC_GAS_RESERVE_BNB", 0.003),

		SMTPHost:            getEnv("SMTP_HOST", ""),
		SMTPPort:            getEnvAsInt("SMTP_PORT", 587),
		SMTPUser:            getEnv("SMTP_USER", ""),
		SMTPPass:            getEnv("SMTP_PASS", ""),
		SMTPSecure:          getEnvAsBool("SMTP_SECURE", false),
		SMTPFromEmail:       getEnv("SMTP_FROM_EMAIL", ""),
		SMTPFromName:        getEnv("SMTP_FROM_NAME", "ChainFX"),
		OpsEmail:            getEnv("OPS_EMAIL", getEnv("SMTP_FROM_EMAIL", "")),
		EmailBrandName:      getEnv("EMAIL_BRAND_NAME", "ChainFX"),
		EmailLogoURL:        getEnv("EMAIL_LOGO_URL", "https://res.cloudinary.com/limpeja/image/upload/v1783623705/Green_Modern_Marketing_Logo-removebg-preview_1_yivrrc.png"),
		EmailSiteURL:        strings.TrimRight(getEnv("EMAIL_SITE_URL", "https://www.chainfx.store"), "/"),
		EmailAddress:        getEnv("EMAIL_COMPANY_ADDRESS", "ChainFX Payments"),
		SupportEmail:        getEnv("SUPPORT_EMAIL", getEnv("SMTP_FROM_EMAIL", "")),
		LGPDSecret:          getEnv("LGPD_SECRET", ""),
		WebhooksEnabled:     getEnvAsBool("WEBHOOKS_ENABLED", true),
		WebhooksMaxRetries:  getEnvAsInt("WEBHOOKS_MAX_RETRIES", 5),
		OpenAIAPIKey:        getEnv("OPENAI_API_KEY", ""),
		OpenAIModel:         getEnv("OPENAI_MODEL", "gpt-5.5"),
		OpenAIBaseURL:       strings.TrimRight(getEnv("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/"),
		CapabilityOCRURL:    strings.TrimRight(getEnv("CAPABILITY_OCR_URL", ""), "/"),
		CapabilityOCRAPIKey: getEnv("CAPABILITY_OCR_API_KEY", ""),

		M2MPixFeeBps:                 getEnvAsInt("M2M_PIX_FEE_BPS", 1000),
		M2MCreditFeeBps:              getEnvAsInt("M2M_CREDIT_FEE_BPS", 1900),
		M2MDepositTolerancePct:       getEnvAsFloat("M2M_DEPOSIT_TOLERANCE_PCT", 0.005),
		M2MMaxDailyOutflowBRL:        getEnvAsFloat("M2M_MAX_DAILY_OUTFLOW_BRL", 50000),
		M2MDepositAddresses:          getEnv("M2M_DEPOSIT_ADDRESSES", ""),
		NFCEnabled:                   getEnvAsBool("NFC_ENABLED", true),
		NFCTokenSecret:               getEnv("NFC_TOKEN_SECRET", getEnv("LGPD_SECRET", getEnv("WEBHOOK_SECRET", ""))),
		NFCTokenTTLSeconds:           getEnvAsInt("NFC_TOKEN_TTL_SEC", 120),
		NFCHoldTTLSeconds:            getEnvAsInt("NFC_HOLD_TTL_SEC", 900),
		NFCMaxAmountBRL:              getEnvAsFloat("NFC_MAX_AMOUNT_BRL", 500),
		NFCTerminals:                 getEnv("NFC_TERMINALS", ""),
		NFCPriceMaxAgeSec:            getEnvAsInt("NFC_PRICE_MAX_AGE_SEC", 180),
		NFCFeeBps:                    getEnvAsInt("NFC_FEE_BPS", 400),
		NFCSettlementMode:            strings.ToLower(getEnv("NFC_SETTLEMENT_MODE", "manual")),
		NFCEfiMinBufferBRL:           getEnvAsFloat("NFC_EFI_MIN_BUFFER_BRL", 10000),
		NFCEfiBalanceBRL:             getEnvAsFloat("NFC_EFI_BALANCE_BRL", 0),
		NFCLiquidityPolicyEnabled:    getEnvAsBool("NFC_LIQUIDITY_POLICY_ENABLED", false),
		NFCTreasurySnapshotMaxAgeSec: getEnvAsInt("NFC_TREASURY_SNAPSHOT_MAX_AGE_SEC", 120),
		EIP712DomainName:             getEnv("EIP712_DOMAIN_NAME", "ChainFX"),
		EIP712DomainVersion:          getEnv("EIP712_DOMAIN_VERSION", "1"),
		EIP712ChainID:                int64(getEnvAsInt("EIP712_CHAIN_ID", 56)),
		EIP712VerifyingContract:      getEnv("EIP712_VERIFYING_CONTRACT", firstNonEmptyConfig(getEnv("TREASURY_HOT", ""), getEnv("SELL_WALLET_ADDRESS", "0x7e3BF3FDfeF16040CE3ec60A663381766d3dB375"))),
		EIPProbeEnabled:              getEnvAsBool("EIP_PROBE_ENABLED", true),
		EIPProbeRealRun:              getEnvAsBool("EIP_PROBE_REAL_RUN", false),
		EIPProbeAllowMainnet:         getEnvAsBool("EIP_PROBE_ALLOW_MAINNET", false),
		EIPProbeNetwork:              strings.ToLower(getEnv("EIP_PROBE_NETWORK", getEnv("SIGNER_NETWORK", "bsc"))),
		EIPProbeRPCUrls:              getEnv("EIP_PROBE_RPC_URLS", firstNonEmptyConfig(getEnv("POLYGON_AMOY_RPC_URLS", ""), getEnv("BNB_TESTNET_RPC_URLS", ""), getBscRpcUrls(), getPolygonRpcUrls())),
		EIPProbeRelayerPrivKey:       getEnv("EIP_PROBE_RELAYER_PRIVATE_KEY", getEnv("PAYMASTER_PRIV_KEY", "")),
		EIPProbeWalletPrivKeys:       getEnv("EIP_PROBE_WALLET_PRIVATE_KEYS", ""),
		EIPProbeUSDTContract:         getEnv("EIP_PROBE_USDT_CONTRACT", getEnv("USDT_TESTNET_CONTRACT", "")),
		EIPProbeAsset:                strings.ToUpper(getEnv("EIP_PROBE_ASSET", "USDT")),
		EIPProbeRail:                 strings.ToLower(getEnv("EIP_PROBE_RAIL", "")),
		EIPProbeTokenContract:        getEnv("EIP_PROBE_TOKEN_CONTRACT", firstNonEmptyConfig(getEnv("EIP_PROBE_USDT_CONTRACT", ""), getEnv("USDT_TESTNET_CONTRACT", ""))),
		EIPProbeTokenName:            getEnv("EIP_PROBE_TOKEN_NAME", "Tether USD"),
		EIPProbeTokenSymbol:          strings.ToUpper(getEnv("EIP_PROBE_TOKEN_SYMBOL", getEnv("EIP_PROBE_ASSET", "USDT"))),
		EIPProbeTokenVersion:         getEnv("EIP_PROBE_TOKEN_VERSION", "1"),
		EIPProbeAmountRaw:            getEnv("EIP_PROBE_AMOUNT_RAW", "10000"),
		EIPProbeConfirmTimeout:       getEnvAsInt("EIP_PROBE_CONFIRM_TIMEOUT_SEC", 45),
		EIPProbeExpectedGas3009:      uint64(getEnvAsInt("EIP_PROBE_EXPECTED_GAS_EIP3009", 90000)),
		BSCMinConfirmations:          getEnvAsUint64("BSC_MIN_CONFIRMATIONS", 6),
		PolygonMinConfirmations:      getEnvAsUint64("POLYGON_MIN_CONFIRMATIONS", 128),

		GasStationEnabled:          getEnvAsBool("GAS_STATION_ENABLED", false),
		GasStationSurchargeBps:     getEnvAsInt("GAS_STATION_SURCHARGE_BPS", 1000),
		GasStationMaxFeeUsdt:       getEnvAsFloat("GAS_STATION_MAX_FEE_USDT", 2.00),
		GasStationMinFeeUsdt:       getEnvAsFloat("GAS_STATION_MIN_FEE_USDT", 0.05),
		PaymasterMulticallContract: getEnv("PAYMASTER_MULTICALL_CONTRACT", ""),
		PaymasterPrivKey:           getEnv("PAYMASTER_PRIV_KEY", ""),
		PaymasterRelaySpreadBps:    getEnvAsInt("PAYMASTER_RELAY_SPREAD_BPS", 100),

		AutoSweeperEnabled:     getEnvAsBool("AUTO_SWEEPER_ENABLED", false),
		AutoSweeperIntervalSec: getEnvAsInt("AUTO_SWEEPER_INTERVAL_SEC", 60),
		AutoSweeperHotMaxUsdt:  getEnvAsFloat("AUTO_SWEEPER_HOT_MAX_USDT", 5000),
		AutoSweeperHotMinUsdt:  getEnvAsFloat("AUTO_SWEEPER_HOT_MIN_USDT", 500),
	}
}

func (c *Config) IsProduction() bool {
	env := strings.ToLower(strings.TrimSpace(c.Environment))
	return env == "production" || env == "prod"
}

func efiChargesDefaultBaseURL(env string) string {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "production", "prod":
		return "https://cobrancas.api.efipay.com.br"
	default:
		return "https://cobrancas-h.api.efipay.com.br"
	}
}

func (c *Config) ValidateProduction() error {
	if !c.IsProduction() {
		return nil
	}
	required := map[string]string{
		"DATABASE_URL":             c.DatabaseURL,
		"LGPD_SECRET":              c.LGPDSecret,
		"WEBHOOK_SECRET":           c.WebhookSecret,
		"PIX_WEBHOOK_SECRET":       c.PixWebhookSecret,
		"SIGNER_URL":               c.SignerUrl,
		"SIGNER_HMAC_SECRET":       c.SignerHmacSecret,
		"EFI_CLIENT_ID":            c.EfiClientID,
		"EFI_CLIENT_SECRET":        c.EfiClientSecret,
		"EFI_PIX_KEY":              c.EfiPixKey,
		"TREASURY_HOT":             c.TreasuryHot,
		"CHAINFX_LIVE_SECRET_KEYS": c.ChainFXLiveSecretKeys,
	}
	if c.NFCEnabled {
		required["NFC_TOKEN_SECRET"] = c.NFCTokenSecret
		required["NFC_TERMINALS"] = c.NFCTerminals
	}
	if strings.TrimSpace(c.EfiCertificatePath) == "" && strings.TrimSpace(c.EfiCertificateP12) == "" {
		required["EFI_CERTIFICATE_PATH or EFI_CERTIFICATE_P12_BASE64"] = ""
	}
	switch strings.ToLower(strings.TrimSpace(c.SignerNetwork)) {
	case "bsc", "evm":
		required["BSC_RPC_URLS"] = c.BscRpcUrls
		required["BSC_USDT_CONTRACT"] = c.BscUsdtContract
	default:
		return fmt.Errorf("SIGNER_NETWORK deve ser bsc em producao")
	}
	var missing []string
	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("configuracao de producao incompleta: %s", strings.Join(missing, ", "))
	}
	if !common.IsHexAddress(strings.TrimSpace(c.TreasuryHot)) {
		return fmt.Errorf("TREASURY_HOT deve ser um endereco EVM valido")
	}
	if sellWallet := strings.TrimSpace(c.SellWalletAddress); sellWallet != "" && !common.IsHexAddress(sellWallet) {
		return fmt.Errorf("SELL_WALLET_ADDRESS deve ser um endereco EVM valido")
	}
	if err := validateEVMAddressList("M2M_DEPOSIT_ADDRESSES", c.M2MDepositAddresses); err != nil {
		return err
	}
	if c.AllowSimulations {
		return fmt.Errorf("ALLOW_SIMULATIONS deve ser false em producao")
	}
	if c.EnableSweepStub {
		return fmt.Errorf("ENABLE_SWEEP_STUB deve ser false em producao")
	}
	signerURL := strings.ToLower(strings.TrimSpace(c.SignerUrl))
	if strings.Contains(signerURL, "up.railway.app") {
		return fmt.Errorf("SIGNER_URL deve usar rede privada em producao, nao dominio publico Railway; exemplo: http://signer.railway.internal:4010")
	}
	return nil
}

func validateEVMAddressList(name, raw string) error {
	for _, value := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !common.IsHexAddress(value) {
			return fmt.Errorf("%s contem endereco EVM invalido: %s", name, value)
		}
	}
	return nil
}

// Auxiliares para leitura e conversão de tipos
func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return cleanEnvValue(value)
	}
	return defaultValue
}

func cleanEnvValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		first := value[0]
		last := value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return strings.TrimSpace(value[1 : len(value)-1])
		}
	}
	return value
}

func firstNonEmptyConfig(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func getBscRpcUrls() string {
	var urls []string
	seen := map[string]bool{}
	for _, key := range []string{
		"BSC_RPC_URLS", "BSC_TESTNET_RPC_URLS", "BSC_TESTNET_RPC", "BNB_TESTNET_RPC_URLS", "BNB_TESTNET_RPC", "RPC_URLS", "RPC_URL",
		"RPC1", "RPC2", "RPC3", "RPC4", "RPCN",
		"ALCHEMY_BSC_RPC_URL_1", "ALCHEMY_BSC_RPC_URL_2", "ALCHEMY_BSC_RPC_URL", "ALCHEMY_BSC_FALLBACK_RPC_URL",
	} {
		for _, url := range splitConfigCSV(getEnv(key, "")) {
			if !seen[url] {
				urls = append(urls, url)
				seen[url] = true
			}
		}
	}
	return strings.Join(urls, ",")
}

func getPolygonRpcUrls() string {
	var urls []string
	seen := map[string]bool{}
	for _, key := range []string{"POLYGON_RPC_URLS", "POLYGON_RPC_URL", "POLYGON_AMOY_RPC_URLS", "POLYGON_AMOY_RPC", "ALCHEMY_POLYGON_RPC_URL_1", "ALCHEMY_POLYGON_RPC_URL_2", "ALCHEMY_POLYGON_RPC_URL", "ALCHEMY_POLYGON_FALLBACK_RPC_URL"} {
		for _, url := range splitConfigCSV(getEnv(key, "")) {
			if !seen[url] {
				urls = append(urls, url)
				seen[url] = true
			}
		}
	}
	return strings.Join(urls, ",")
}

func getChainRpcUrls(prefix string, keys []string) string {
	var urls []string
	seen := map[string]bool{}
	for _, key := range keys {
		for _, url := range splitConfigCSV(getEnv(key, "")) {
			if !seen[url] {
				urls = append(urls, url)
				seen[url] = true
			}
		}
	}
	if len(urls) == 0 {
		for _, key := range []string{prefix + "_RPC_URLS", prefix + "_RPC_URL"} {
			for _, url := range splitConfigCSV(getEnv(key, "")) {
				if !seen[url] {
					urls = append(urls, url)
					seen[url] = true
				}
			}
		}
	}
	return strings.Join(urls, ",")
}

func splitConfigCSV(raw string) []string {
	var out []string
	for _, value := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func getEnvAsInt(key string, defaultValue int) int {
	valueStr := getEnv(key, "")
	if value, err := strconv.Atoi(valueStr); err == nil {
		return value
	}
	return defaultValue
}

func getEnvAsFloat(key string, defaultValue float64) float64 {
	valueStr := getEnv(key, "")
	if value, err := strconv.ParseFloat(valueStr, 64); err == nil {
		return value
	}
	return defaultValue
}

func getEnvPercentAsBps(key string, defaultValue int) int {
	valueStr := getEnv(key, "")
	if value, err := strconv.ParseFloat(valueStr, 64); err == nil {
		return int(value*100 + 0.5)
	}
	return defaultValue
}

func getEnvAsBool(key string, defaultValue bool) bool {
	valueStr := getEnv(key, "")
	if valueStr == "" {
		return defaultValue
	}
	value, err := strconv.ParseBool(valueStr)
	if err != nil {
		return defaultValue
	}
	return value
}

func getEnvAsUint64(key string, defaultValue uint64) uint64 {
	valueStr := getEnv(key, "")
	if valueStr == "" {
		return defaultValue
	}
	value, err := strconv.ParseUint(valueStr, 10, 64)
	if err != nil || value == 0 {
		return defaultValue
	}
	return value
}
