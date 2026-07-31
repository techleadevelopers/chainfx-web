package treasury

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"payment-gateway/internal/config"
)

const (
	ReadinessReady         = "ready"
	ReadinessDisabled      = "disabled"
	ReadinessNotApplicable = "not_applicable"
	ReadinessFailClosed    = "fail_closed"
)

type DelegateCodeReader interface {
	ChainID(ctx context.Context) (*big.Int, error)
	CodeAt(ctx context.Context, account common.Address, blockNumber *big.Int) ([]byte, error)
}

type DelegateReadiness struct {
	Network          string
	Status           string
	DelegateAddress  string
	ExpectedChainID  int64
	ObservedChainID  int64
	ExpectedCodeHash string
	ObservedCodeHash string
	Reason           string
}

func ValidateDelegateReadiness(ctx context.Context, cfg *config.Config, network string, client DelegateCodeReader) DelegateReadiness {
	n := normalizeNetwork(network)
	out := DelegateReadiness{Network: n, Status: ReadinessFailClosed}
	if !networkPermits7702(n) {
		out.Status = ReadinessNotApplicable
		out.Reason = "network_forced_legacy"
		return out
	}
	if cfg == nil || !cfg.Treasury7702Enabled {
		out.Status = ReadinessDisabled
		out.Reason = "feature_disabled"
		return out
	}
	if !csvContainsNetwork(cfg.Treasury7702Networks, n) {
		out.Status = ReadinessDisabled
		out.Reason = "network_not_enabled"
		return out
	}
	delegate, expectedHash := delegateConfig(cfg, n)
	expectedChainID := expectedDelegateChainID(cfg, n)
	out.ExpectedChainID = expectedChainID
	out.ExpectedCodeHash = strings.ToLower(strings.TrimSpace(expectedHash))
	if !common.IsHexAddress(delegate) || common.HexToAddress(delegate) == (common.Address{}) {
		out.Reason = "delegate_address_missing"
		return out
	}
	if !isBytes32Hex(expectedHash) {
		out.Reason = "delegate_code_hash_missing"
		return out
	}
	out.DelegateAddress = common.HexToAddress(delegate).Hex()
	if client == nil {
		out.Reason = "rpc_client_missing"
		return out
	}
	chainID, err := client.ChainID(ctx)
	if err != nil {
		out.Reason = "chain_id_read_failed: " + err.Error()
		return out
	}
	if chainID == nil || chainID.Int64() != expectedChainID {
		if chainID != nil {
			out.ObservedChainID = chainID.Int64()
		}
		out.Reason = fmt.Sprintf("chain_id_mismatch_expected_%d", expectedChainID)
		return out
	}
	out.ObservedChainID = chainID.Int64()
	code, err := client.CodeAt(ctx, common.HexToAddress(delegate), nil)
	if err != nil {
		out.Reason = "delegate_code_read_failed: " + err.Error()
		return out
	}
	if len(code) == 0 {
		out.Reason = "delegate_code_empty"
		return out
	}
	observed := crypto.Keccak256Hash(code).Hex()
	out.ObservedCodeHash = strings.ToLower(observed)
	if !strings.EqualFold(observed, expectedHash) {
		out.Reason = "delegate_code_hash_mismatch"
		return out
	}
	out.Status = ReadinessReady
	out.Reason = "delegate_bytecode_ready"
	return out
}

func expectedDelegateChainID(cfg *config.Config, network string) int64 {
	switch normalizeNetwork(network) {
	case "BSC":
		if cfg != nil && cfg.BscChainID > 0 {
			return cfg.BscChainID
		}
		return 56
	case "POLYGON":
		if cfg != nil && cfg.PolygonChainID > 0 {
			return cfg.PolygonChainID
		}
		return 137
	default:
		return 0
	}
}
