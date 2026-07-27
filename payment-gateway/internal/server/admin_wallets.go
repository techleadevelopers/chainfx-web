package server

import (
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/transactions"

	"github.com/ethereum/go-ethereum/common"
)

type adminWalletRow struct {
	ID              string `json:"id"`
	Role            string `json:"role"`
	Address         string `json:"address"`
	Network         string `json:"network"`
	ChainID         int64  `json:"chainId"`
	Asset           string `json:"asset"`
	TokenContract   string `json:"tokenContract,omitempty"`
	ConfigKey       string `json:"configKey"`
	Configured      bool   `json:"configured"`
	ValidAddress    bool   `json:"validAddress"`
	CanReceiveFunds bool   `json:"canReceiveFunds"`
	CanPayout       bool   `json:"canPayout"`
	Notes           string `json:"notes,omitempty"`
}

func (s *Server) handleAdminWallets(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.adminWalletsSnapshot())
}

func (s *Server) adminWalletsSnapshot() map[string]any {
	wallets := []adminWalletRow{}
	add := func(row adminWalletRow) {
		row.Address = strings.TrimSpace(row.Address)
		row.Network = strings.ToUpper(strings.TrimSpace(row.Network))
		if row.ChainID == 0 {
			row.ChainID = transactions.ChainID(row.Network)
		}
		row.Asset = strings.ToUpper(strings.TrimSpace(row.Asset))
		if row.Asset == "" {
			row.Asset = "USDT"
		}
		row.Configured = row.Address != ""
		row.ValidAddress = row.Configured && common.IsHexAddress(row.Address)
		row.CanReceiveFunds = row.ValidAddress && row.TokenContract != ""
		wallets = append(wallets, row)
	}
	add(adminWalletRow{
		ID:            "treasury_vault_bsc",
		Role:          "treasury_vault_optional",
		Address:       s.cfg.BscTreasuryContract,
		Network:       "BSC",
		Asset:         "USDT",
		TokenContract: s.cfg.BscUsdtContract,
		ConfigKey:     "BSC_TREASURY_CONTRACT",
		CanPayout:     false,
		Notes:         "Optional Solidity vault. Core buy/sell runs without this contract.",
	})
	add(adminWalletRow{
		ID:            "treasury_hot_bsc",
		Role:          "treasury_hot",
		Address:       s.cfg.TreasuryHot,
		Network:       "BSC",
		Asset:         "USDT",
		TokenContract: s.cfg.BscUsdtContract,
		ConfigKey:     "TREASURY_HOT",
		CanPayout:     s.cfg.SignerUrl != "" && s.cfg.SignerHmacSecret != "",
		Notes:         "Hot wallet used by signer, sweeper and settlement operations.",
	})
	add(adminWalletRow{
		ID:            "treasury_cold_bsc",
		Role:          "treasury_cold",
		Address:       s.cfg.TreasuryCold,
		Network:       "BSC",
		Asset:         "USDT",
		TokenContract: s.cfg.BscUsdtContract,
		ConfigKey:     "TREASURY_COLD",
		CanPayout:     false,
		Notes:         "Cold destination for sweeper and manual reserves.",
	})
	add(adminWalletRow{
		ID:            "sell_deposit_bsc",
		Role:          "sell_deposit",
		Address:       s.cfg.SellWalletAddress,
		Network:       "BSC",
		Asset:         "USDT",
		TokenContract: s.cfg.BscUsdtContract,
		ConfigKey:     "SELL_WALLET_ADDRESS",
		CanPayout:     false,
		Notes:         "Customer sell deposit wallet for BSC flow.",
	})
	add(adminWalletRow{
		ID:            "treasury_vault_polygon",
		Role:          "treasury_vault_optional",
		Address:       s.cfg.PolygonTreasuryContract,
		Network:       "POLYGON",
		Asset:         "USDT",
		TokenContract: s.cfg.PolygonUsdtContract,
		ConfigKey:     "POLYGON_TREASURY_CONTRACT",
		CanPayout:     false,
		Notes:         "Optional Polygon vault. Leave empty until a later on-chain policy phase.",
	})
	add(adminWalletRow{
		ID:            "polygon_usdt_contract",
		Role:          "token_contract",
		Address:       s.cfg.PolygonUsdtContract,
		Network:       "POLYGON",
		Asset:         "USDT",
		TokenContract: s.cfg.PolygonUsdtContract,
		ConfigKey:     "POLYGON_USDT_CONTRACT",
		CanPayout:     false,
		Notes:         "Official Polygon USDT contract for the next network rollout.",
	})
	return map[string]any{
		"generatedAt": time.Now().UTC().Format(time.RFC3339Nano),
		"networkPolicy": []map[string]any{
			{"asset": "USDT", "network": "BSC", "chainId": transactions.ChainID("BSC"), "priority": "active"},
			{"asset": "USDT", "network": "POLYGON", "chainId": transactions.ChainID("POLYGON"), "priority": "configured_next"},
			{"asset": "USDT", "network": "BASE", "chainId": transactions.ChainID("BASE"), "priority": "future"},
			{"asset": "USDT", "network": "ARBITRUM", "chainId": transactions.ChainID("ARBITRUM"), "priority": "future"},
		},
		"signer": map[string]any{
			"network":    strings.ToUpper(s.cfg.SignerNetwork),
			"configured": s.cfg.SignerUrl != "" && s.cfg.SignerHmacSecret != "",
		},
		"wallets": wallets,
	}
}
