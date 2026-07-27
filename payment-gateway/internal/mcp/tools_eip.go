package mcp

import (
	"context"
	"strings"

	"payment-gateway/internal/eip712"
)

func (s *Server) toolGetEIPCapabilities() any {
	assets := s.eipAssetCapabilities()
	return map[string]any{
		"domain": s.eipDomain(eip712.Domain{}),
		"typedIntents": []map[string]any{
			{"type": eip712.TypeM2MIntent, "signer": "payer", "status": "enabled"},
			{"type": eip712.TypeMobileTransfer, "signer": "from", "status": "enabled"},
			{"type": eip712.TypeCapabilityPurchase, "signer": "payer", "status": "enabled"},
			{"type": eip712.TypePayIntent, "signer": "payer", "status": "enabled"},
		},
		"assets": assets,
		"rails": map[string]any{
			"eip712":  "enabled",
			"eip2612": "optional_by_token",
			"eip3009": "enabled_for_usdc_capable_assets",
			"eip4337": "planned_phase_2",
			"eip7702": "planned_phase_3_guarded",
		},
	}
}

func (s *Server) toolPrepareEIPTypedIntent(ctx context.Context, args map[string]any) (any, error) {
	message := map[string]any{}
	if raw, ok := args["message"].(map[string]any); ok {
		message = raw
	} else {
		for key, value := range args {
			message[key] = value
		}
	}
	intentType := stringArg(args, "intentType")
	if intentType == "" {
		intentType = stringArg(args, "type")
	}
	intent := eip712.NormalizeIntent(message, intentType)
	assets := s.eipAssetCapabilities()
	intent = resolveEIPIntentAsset(intent, assets)
	prepared, err := eip712.Prepare(s.eipDomain(eip712.Domain{}), intent, assets)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":       true,
		"prepared": prepared,
		"nextStep": "Assine prepared.typedData via eth_signTypedData_v4 e envie a signature para /agent/v1/eips/verify ou para o rail financeiro escolhido.",
	}, nil
}

func (s *Server) eipDomain(override eip712.Domain) eip712.Domain {
	if strings.TrimSpace(override.Name) != "" || strings.TrimSpace(override.Version) != "" || override.ChainID != 0 || strings.TrimSpace(override.VerifyingContract) != "" {
		return eip712.NormalizeDomain(override)
	}
	domain := eip712.Domain{Name: "ChainFX", Version: "1", ChainID: 56}
	if s.cfg != nil {
		domain.Name = s.cfg.EIP712DomainName
		domain.Version = s.cfg.EIP712DomainVersion
		domain.ChainID = s.cfg.EIP712ChainID
		domain.VerifyingContract = firstNonEmptyMCP(s.cfg.EIP712VerifyingContract, s.cfg.TreasuryHot, s.cfg.SellWalletAddress)
	}
	return eip712.NormalizeDomain(domain)
}

func (s *Server) eipAssetCapabilities() []eip712.AssetCapability {
	assets := []eip712.AssetCapability{
		{Symbol: "USDC", Network: "BSC", TokenContract: bscUSDCContractMCP, Decimals: 18, SupportsEIP3009: true, SupportsPermit2: true, CustodialRelay: true, PreferredRail: "eip3009_transfer_with_authorization"},
		{Symbol: "USDT", Network: "BSC", TokenContract: s.bscUSDTContract(), Decimals: 18, SupportsPermit2: true, CustodialRelay: true, PreferredRail: "custodial_relay"},
	}
	return assets
}

func (s *Server) bscUSDTContract() string {
	if s.cfg != nil && strings.TrimSpace(s.cfg.BscUsdtContract) != "" {
		return strings.ToLower(strings.TrimSpace(s.cfg.BscUsdtContract))
	}
	return bscUSDTContractMCP
}

func resolveEIPIntentAsset(intent eip712.Intent, assets []eip712.AssetCapability) eip712.Intent {
	if strings.HasPrefix(strings.TrimSpace(intent.Asset), "0x") {
		return intent
	}
	for _, asset := range assets {
		if strings.EqualFold(asset.Symbol, intent.Asset) && strings.TrimSpace(asset.TokenContract) != "" {
			intent.Asset = asset.TokenContract
			return intent
		}
	}
	return intent
}

const (
	bscUSDCContractMCP = "0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d"
	bscUSDTContractMCP = "0x55d398326f99059ff775485246999027b3197955"
)
