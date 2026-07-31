package treasury

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"payment-gateway/internal/config"
)

type fakeDelegateCodeReader struct {
	chainID *big.Int
	code    []byte
	err     error
}

func (f fakeDelegateCodeReader) ChainID(context.Context) (*big.Int, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.chainID, nil
}

func (f fakeDelegateCodeReader) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.code, nil
}

func TestValidateDelegateReadinessBSCAndPolygon(t *testing.T) {
	code := []byte{0x60, 0x80, 0x60, 0x40}
	hash := crypto.Keccak256Hash(code).Hex()
	cfg := &config.Config{
		Treasury7702Enabled:         true,
		Treasury7702Networks:        "BSC,POLYGON,ETHEREUM",
		BscChainID:                  56,
		PolygonChainID:              137,
		BSC7702Delegate:             "0x1111111111111111111111111111111111111111",
		BSC7702DelegateCodeHash:     hash,
		Polygon7702Delegate:         "0x2222222222222222222222222222222222222222",
		Polygon7702DelegateCodeHash: hash,
	}
	for _, tc := range []struct {
		network string
		chainID int64
	}{
		{"BSC", 56},
		{"POLYGON", 137},
	} {
		got := ValidateDelegateReadiness(context.Background(), cfg, tc.network, fakeDelegateCodeReader{chainID: big.NewInt(tc.chainID), code: code})
		if got.Status != ReadinessReady {
			t.Fatalf("%s readiness should pass, got %+v", tc.network, got)
		}
	}
}

func TestValidateDelegateReadinessNeverAppliesToEthereumFamily(t *testing.T) {
	cfg := &config.Config{
		Treasury7702Enabled:  true,
		Treasury7702Networks: "BSC,POLYGON,ETHEREUM,ARBITRUM,BASE",
	}
	for _, network := range []string{"ETHEREUM", "ARBITRUM", "BASE", "EVM"} {
		got := ValidateDelegateReadiness(context.Background(), cfg, network, fakeDelegateCodeReader{})
		if got.Status != ReadinessNotApplicable || got.Reason != "network_forced_legacy" {
			t.Fatalf("%s must be not_applicable, got %+v", network, got)
		}
	}
}

func TestValidateDelegateReadinessFailsClosed(t *testing.T) {
	code := []byte{0x60, 0x80}
	hash := crypto.Keccak256Hash(code).Hex()
	cfg := &config.Config{
		Treasury7702Enabled:     true,
		Treasury7702Networks:    "BSC",
		BscChainID:              56,
		BSC7702Delegate:         "0x1111111111111111111111111111111111111111",
		BSC7702DelegateCodeHash: hash,
	}
	cases := []struct {
		name   string
		client fakeDelegateCodeReader
	}{
		{"wrong_chain", fakeDelegateCodeReader{chainID: big.NewInt(137), code: code}},
		{"empty_code", fakeDelegateCodeReader{chainID: big.NewInt(56), code: nil}},
		{"hash_mismatch", fakeDelegateCodeReader{chainID: big.NewInt(56), code: []byte{0x60, 0x81}}},
		{"rpc_error", fakeDelegateCodeReader{err: errors.New("rpc down")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateDelegateReadiness(context.Background(), cfg, "BSC", tc.client)
			if got.Status != ReadinessFailClosed {
				t.Fatalf("readiness must fail closed, got %+v", got)
			}
		})
	}
}
