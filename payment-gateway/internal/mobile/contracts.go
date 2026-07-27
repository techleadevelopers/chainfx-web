package mobile

import (
	"context"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func (s *Server) handleContractVault(w http.ResponseWriter, r *http.Request) {
	vaultAddr := strings.TrimSpace(s.cfg.TreasuryHot)
	if vaultAddr == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false,
			"hint":       "set TREASURY_HOT in .env",
		})
		return
	}

	// Cache vault BNB balance for 30 s — vault balance changes only on sweeps,
	// not on every user action, so a fresh RPC dial per request is wasteful
	// and adds 200-800 ms of unnecessary latency.
	type vaultCached struct {
		balance string
		errMsg  string
	}
	cacheKey := "vault_bnb:" + strings.ToLower(vaultAddr)
	if raw, ok := s.getMobileCache(cacheKey); ok {
		if v, ok2 := raw.(vaultCached); ok2 {
			writeJSON(w, http.StatusOK, map[string]any{
				"configured":    true,
				"vault_address": vaultAddr,
				"bnb_balance":   v.balance,
				"error":         v.errMsg,
				"network":       s.cfg.SignerNetwork,
				"cached":        true,
			})
			return
		}
	}

	bal, err := fetchBNBBalance(r.Context(), s.cfg.BscRpcUrls, vaultAddr)
	s.setMobileCache(cacheKey, vaultCached{balance: bal, errMsg: errStr(err)}, 30*time.Second)

	writeJSON(w, http.StatusOK, map[string]any{
		"configured":    true,
		"vault_address": vaultAddr,
		"bnb_balance":   bal,
		"error":         errStr(err),
		"network":       s.cfg.SignerNetwork,
		"cached":        false,
	})
}

func (s *Server) handleContractDelegate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"signer_url":     s.cfg.SignerUrl,
		"signer_network": s.cfg.SignerNetwork,
		"configured":     s.cfg.SignerUrl != "" && s.cfg.SignerHmacSecret != "",
	})
}

func (s *Server) handleContractPayout(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error": "payout de contrato exige rota admin/internal, nao JWT mobile",
	})
}

func (s *Server) handleContractPause(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error": "pause de contrato exige rota admin/internal, nao JWT mobile",
	})
}

func (s *Server) handleContractUnpause(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error": "unpause de contrato exige rota admin/internal, nao JWT mobile",
	})
}

func fetchBNBBalance(ctx context.Context, rpcURLs, address string) (string, error) {
	url := strings.Split(rpcURLs, ",")[0]
	if url == "" {
		url = "https://bsc-dataseed.binance.org/"
	}
	client, err := ethclient.DialContext(ctx, url)
	if err != nil {
		return "0", err
	}
	defer client.Close()
	bal, err := client.BalanceAt(ctx, common.HexToAddress(address), nil)
	if err != nil {
		return "0", err
	}
	f := new(big.Float).Quo(new(big.Float).SetInt(bal), big.NewFloat(1e18))
	return f.Text('f', 6), nil
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
