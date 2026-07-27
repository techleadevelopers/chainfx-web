package mobile

import (
	"math/big"
	"strings"
	"testing"

	"payment-gateway/internal/config"

	"github.com/ethereum/go-ethereum/common"
)

func TestParseTokenAmount(t *testing.T) {
	tests := []struct {
		name     string
		amount   string
		decimals int
		want     string
		wantErr  bool
	}{
		{name: "bsc decimals", amount: "1.25", decimals: 18, want: "1250000000000000000"},
		{name: "polygon decimals", amount: "1.25", decimals: 6, want: "1250000"},
		{name: "too many decimals", amount: "0.0000001", decimals: 6, wantErr: true},
		{name: "zero", amount: "0", decimals: 18, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTokenAmount(tt.amount, tt.decimals)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != tt.want {
				t.Fatalf("got %s, want %s", got.String(), tt.want)
			}
		})
	}
}

func TestERC20TransferCalldata(t *testing.T) {
	to := common.HexToAddress("0x742d35Cc6634C0532925a3b844Bc454e4438f44e")
	data := erc20TransferCalldata(to, big.NewInt(1000000))

	if !strings.HasPrefix(data, "0xa9059cbb") {
		t.Fatalf("missing transfer selector: %s", data)
	}
	if len(data) != 138 {
		t.Fatalf("unexpected calldata length %d", len(data))
	}
	if !strings.Contains(strings.ToLower(data), strings.TrimPrefix(strings.ToLower(to.Hex()), "0x")) {
		t.Fatalf("recipient missing from calldata: %s", data)
	}
}

func TestMobileTransferTokenUsesConfiguredChainID(t *testing.T) {
	s := &Server{cfg: &config.Config{
		BscUsdtContract:     "0x0000000000000000000000000000000000000001",
		PolygonUsdtContract: "0x0000000000000000000000000000000000000002",
		BscChainID:          97,
		PolygonChainID:      80002,
	}}

	_, _, bscChainID, err := s.mobileTransferToken("USDT", "BSC")
	if err != nil {
		t.Fatalf("unexpected BSC error: %v", err)
	}
	if bscChainID != 97 {
		t.Fatalf("BSC chainID = %d, want 97", bscChainID)
	}

	_, _, polygonChainID, err := s.mobileTransferToken("USDT", "POLYGON")
	if err != nil {
		t.Fatalf("unexpected Polygon error: %v", err)
	}
	if polygonChainID != 80002 {
		t.Fatalf("Polygon chainID = %d, want 80002", polygonChainID)
	}
}

func TestMobileTransferTokenDefaultsToMainnetChainID(t *testing.T) {
	s := &Server{cfg: &config.Config{
		BscUsdtContract:     "0x0000000000000000000000000000000000000001",
		PolygonUsdtContract: "0x0000000000000000000000000000000000000002",
	}}

	_, _, bscChainID, err := s.mobileTransferToken("USDT", "BSC")
	if err != nil {
		t.Fatalf("unexpected BSC error: %v", err)
	}
	if bscChainID != 56 {
		t.Fatalf("BSC chainID = %d, want 56", bscChainID)
	}

	_, _, polygonChainID, err := s.mobileTransferToken("USDT", "POLYGON")
	if err != nil {
		t.Fatalf("unexpected Polygon error: %v", err)
	}
	if polygonChainID != 137 {
		t.Fatalf("Polygon chainID = %d, want 137", polygonChainID)
	}
}

func TestMobileTransferTokenSupportsNewEVMUSDCNetworks(t *testing.T) {
	s := &Server{cfg: &config.Config{
		BaseUsdcContract:     "0x0000000000000000000000000000000000000003",
		BaseChainID:          84531,
		ArbitrumUsdcContract: "0x0000000000000000000000000000000000000004",
		ArbitrumChainID:      421614,
		EthereumUsdcContract: "0x0000000000000000000000000000000000000005",
		EthereumChainID:      11155111,
	}}

	tests := []struct {
		network string
		want    int
	}{
		{"BASE", 84531},
		{"ARBITRUM", 421614},
		{"ETHEREUM", 11155111},
	}
	for _, tt := range tests {
		t.Run(tt.network, func(t *testing.T) {
			token, decimals, chainID, err := s.mobileTransferToken("USDC", tt.network)
			if err != nil {
				t.Fatalf("mobileTransferToken: %v", err)
			}
			if token == "" || decimals != 6 || chainID != tt.want {
				t.Fatalf("token=%q decimals=%d chainID=%d, want configured USDC/6/%d", token, decimals, chainID, tt.want)
			}
		})
	}
}

func TestMobileTransferTokenSupportsRegistryERC20AndNativeEVMPairs(t *testing.T) {
	s := &Server{cfg: &config.Config{
		LiquidityAllowedPairs: "USDT:ETHEREUM:0xdAC17F958D2ee523a2206206994597C13D831ec7:6,USDT:ARBITRUM:0xfd086bc7CD5C481DCC9C85ebe478A1C0b69FCbb9:6,LINK:ETHEREUM:0x514910771AF9Ca656af840dff83E8264EcF986CA:18,LINK:ARBITRUM:0xf97f4df75117a78c1A5a0DBb814Af92458539FB4:18,ETH:BASE::18,ETH:ARBITRUM::18,ETH:ETHEREUM::18",
		SupportedNetworks:     "BASE,ARBITRUM,ETHEREUM",
		BaseChainID:           8453,
		ArbitrumChainID:       42161,
		EthereumChainID:       1,
	}}

	for _, tt := range []struct {
		asset    string
		network  string
		decimals int
		chainID  int
		native   bool
	}{
		{"USDT", "ETHEREUM", 6, 1, false},
		{"USDT", "ARBITRUM", 6, 42161, false},
		{"LINK", "ETHEREUM", 18, 1, false},
		{"LINK", "ARBITRUM", 18, 42161, false},
		{"ETH", "BASE", 18, 8453, true},
		{"ETH", "ARBITRUM", 18, 42161, true},
		{"ETH", "ETHEREUM", 18, 1, true},
	} {
		t.Run(tt.asset+"_"+tt.network, func(t *testing.T) {
			token, decimals, chainID, err := s.mobileTransferToken(tt.asset, tt.network)
			if err != nil {
				t.Fatalf("mobileTransferToken: %v", err)
			}
			if decimals != tt.decimals || chainID != tt.chainID {
				t.Fatalf("decimals=%d chainID=%d, want %d/%d", decimals, chainID, tt.decimals, tt.chainID)
			}
			if tt.native && token != "" {
				t.Fatalf("native pair token=%q, want empty contract", token)
			}
			if !tt.native && !common.IsHexAddress(token) {
				t.Fatalf("ERC20 pair token=%q, want EVM contract", token)
			}
		})
	}
}

func TestMobileTransferTokenSupportsBingXERC20AssetsOnBSCAndPolygon(t *testing.T) {
	s := &Server{cfg: &config.Config{
		BscUsdtContract:     "0x0000000000000000000000000000000000000001",
		PolygonUsdtContract: "0x0000000000000000000000000000000000000002",
		BscChainID:          56,
		PolygonChainID:      137,
	}}

	tests := []struct {
		asset   string
		network string
		chainID int
	}{
		{"ETH", "BSC", 56},
		{"LINK", "BSC", 56},
		{"AVAX", "BSC", 56},
		{"ETH", "POLYGON", 137},
		{"LINK", "POLYGON", 137},
		{"AVAX", "POLYGON", 137},
	}

	for _, tt := range tests {
		t.Run(tt.asset+"_"+tt.network, func(t *testing.T) {
			token, decimals, chainID, err := s.mobileTransferToken(tt.asset, tt.network)
			if err != nil {
				t.Fatalf("mobileTransferToken: %v", err)
			}
			if !common.IsHexAddress(token) || decimals != 18 || chainID != tt.chainID {
				t.Fatalf("token=%q decimals=%d chainID=%d, want ERC20/18/%d", token, decimals, chainID, tt.chainID)
			}
		})
	}
}

func TestNormalizeMobileTransferNetworkRejectsNonEVMRails(t *testing.T) {
	for _, network := range []string{"BITCOIN", "SOLANA", "APTOS"} {
		if got := normalizeMobileTransferNetwork(network); got != "" {
			t.Fatalf("normalizeMobileTransferNetwork(%q)=%q, want empty", network, got)
		}
	}
}
