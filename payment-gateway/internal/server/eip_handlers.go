package server

import (
	"crypto/subtle"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/eip712"
)

type eipIntentRequest struct {
	IntentType   string         `json:"intentType"`
	Domain       eip712.Domain  `json:"domain"`
	Message      map[string]any `json:"message"`
	Signature    string         `json:"signature"`
	ConsumeNonce bool           `json:"consumeNonce"`
}

func (s *Server) handleAgentEIPCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.eipCapabilities(r))
}

func (s *Server) handleAgentEIPPrepare(w http.ResponseWriter, r *http.Request) {
	var req eipIntentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "INVALID_JSON", "JSON invalido")
		return
	}
	intent := eip712.NormalizeIntent(req.Message, req.IntentType)
	assets := s.eipAssetCapabilities(r)
	intent = resolveEIPIntentAsset(intent, assets)
	prepared, err := eip712.Prepare(s.eipDomain(req.Domain), intent, assets)
	if err != nil {
		writeEIPError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"prepared": prepared,
		"calldata": s.eipCalldataPreview(intent, req.Signature, assets),
	})
}

func (s *Server) handleAgentEIPVerify(w http.ResponseWriter, r *http.Request) {
	var req eipIntentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "INVALID_JSON", "JSON invalido")
		return
	}
	if strings.TrimSpace(req.Signature) == "" {
		writeAPIError(w, r, http.StatusBadRequest, "SIGNATURE_REQUIRED", "signature EIP-712 e obrigatoria para verify")
		return
	}
	intent := eip712.NormalizeIntent(req.Message, req.IntentType)
	assets := s.eipAssetCapabilities(r)
	intent = resolveEIPIntentAsset(intent, assets)
	verification, err := eip712.Verify(s.eipDomain(req.Domain), intent, req.Signature, assets)
	if err != nil {
		writeEIPError(w, r, err)
		return
	}
	if !verification.Valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"ok":              false,
			"valid":           false,
			"recoveredSigner": verification.RecoveredSigner,
			"expectedSigner":  verification.ExpectedSigner,
			"digest":          verification.Digest,
			"error":           "SIGNER_MISMATCH",
		})
		return
	}
	if req.ConsumeNonce && s.db != nil {
		expiresAt := time.Now().UTC().Add(15 * time.Minute)
		if verification.Deadline > 0 {
			expiresAt = time.Unix(int64(verification.Deadline), 0).UTC()
		}
		err = s.db.RecordEIP712Nonce(r.Context(), database.EIP712NonceInput{
			Signer:     verification.RecoveredSigner,
			IntentType: verification.IntentType,
			Nonce:      verification.Nonce,
			Digest:     verification.Digest,
			ChainID:    verification.Domain.ChainID,
			ExpiresAt:  expiresAt,
		})
		if errors.Is(err, database.ErrEIP712NonceReplay) {
			writeAPIError(w, r, http.StatusConflict, "NONCE_REPLAY", "nonce EIP-712 ja foi usado para esse signer/tipo/chainId")
			return
		}
		if err != nil {
			writeAPIError(w, r, http.StatusServiceUnavailable, "NONCE_STORE_ERROR", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"verification": verification,
		"nonceStored":  req.ConsumeNonce && s.db != nil,
		"calldata":     s.eipCalldataPreview(intent, req.Signature, assets),
	})
}

func (s *Server) handleAgentEIPTestSuiteStatus(w http.ResponseWriter, r *http.Request) {
	if s.eipProbes == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "EIP_PROBES_UNAVAILABLE", "EIP probe runner indisponivel")
		return
	}
	writeJSON(w, http.StatusOK, s.eipProbes.status(r.Context()))
}

func (s *Server) handleAgentEIPTestSuiteRun(w http.ResponseWriter, r *http.Request) {
	var req eipProbeRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "INVALID_JSON", "JSON invalido")
		return
	}
	if req.RealRun && !s.eipProbeRunAuthorized(w, r) {
		return
	}
	if s.eipProbes == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "EIP_PROBES_UNAVAILABLE", "EIP probe runner indisponivel")
		return
	}
	resp, err := s.eipProbes.run(r.Context(), req, s.eipDomain(eip712.Domain{}), s.eipAssetCapabilities(r))
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "EIP_PROBE_RUN_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) eipProbeRunAuthorized(w http.ResponseWriter, r *http.Request) bool {
	expected := ""
	if s != nil && s.cfg != nil {
		expected = strings.TrimSpace(s.cfg.AdminConsoleKey)
	}
	got := strings.TrimSpace(r.Header.Get("X-Admin-Console-Key"))
	if expected == "" || (got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1) {
		return true
	}
	auth := s.chainFXAuthContext(r)
	if auth.Valid && !(auth.Sandbox && s.cfg != nil && s.cfg.IsProduction()) {
		return true
	}
	writeAPIError(w, r, http.StatusUnauthorized, "EIP_PROBE_AUTH_REQUIRED", "real_run exige X-Admin-Console-Key valido ou API key valida")
	return false
}

func (s *Server) eipCapabilities(r *http.Request) map[string]any {
	domain := s.eipDomain(eip712.Domain{})
	assets := s.eipAssetCapabilities(r)
	return map[string]any{
		"domain": domain,
		"typedIntents": []map[string]any{
			{"type": eip712.TypeM2MIntent, "signer": "payer", "status": "enabled", "use": "MCP/M2M payment intents"},
			{"type": eip712.TypeMobileTransfer, "signer": "from", "status": "enabled", "use": "mobile transfer intents"},
			{"type": eip712.TypeCapabilityPurchase, "signer": "payer", "status": "enabled", "use": "capability purchases"},
			{"type": eip712.TypePayIntent, "signer": "payer", "status": "enabled", "use": "generic stablecoin pay intents"},
		},
		"rails": map[string]any{
			"eip712": "enabled",
			"eip2612": map[string]any{
				"status": "optional_by_token",
				"note":   "Use only when the token contract exposes permit/nonces/DOMAIN_SEPARATOR.",
			},
			"eip3009": map[string]any{
				"status": "enabled_for_usdc_capable_assets",
				"note":   "transferWithAuthorization is preferred for USDC-style direct signed transfers.",
			},
			"eip4337": map[string]any{
				"status": "planned_phase_2",
				"note":   "Requires smart accounts, bundler, EntryPoint and dedicated paymaster monitoring.",
			},
			"eip7702": map[string]any{
				"status": "planned_phase_3_guarded",
				"note":   "Delegation must wait for wallet UX, chain support and phishing-resistant guards.",
			},
		},
		"assets":         assets,
		"prepareRoute":   "/agent/v1/eips/prepare",
		"verifyRoute":    "/agent/v1/eips/verify",
		"testSuiteRoute": "/agent/v1/eips/test-suite",
	}
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
		domain.VerifyingContract = firstNonEmpty(s.cfg.EIP712VerifyingContract, s.cfg.TreasuryHot, s.cfg.SellWalletAddress)
	}
	return eip712.NormalizeDomain(domain)
}

func (s *Server) eipAssetCapabilities(r *http.Request) []eip712.AssetCapability {
	ctx := r.Context()
	assets, err := s.agentTradeAssets(ctx)
	if err != nil || len(assets) == 0 {
		assets = s.fallbackAgentTradeAssets()
	}
	out := make([]eip712.AssetCapability, 0, len(assets))
	for _, asset := range assets {
		if asset == nil {
			continue
		}
		symbol := strings.ToUpper(strings.TrimSpace(asset.Symbol))
		cap := eip712.AssetCapability{
			Symbol:          symbol,
			Network:         firstNonEmpty(asset.Network, "BSC"),
			TokenContract:   strings.ToLower(strings.TrimSpace(asset.ContractAddress)),
			Decimals:        asset.Decimals,
			CustodialRelay:  true,
			SupportsPermit2: symbol != "BUSD",
		}
		switch symbol {
		case "USDC":
			cap.SupportsEIP3009 = true
			cap.PreferredRail = "eip3009_transfer_with_authorization"
		case "USDT":
			cap.SupportsEIP2612 = false
			cap.SupportsEIP3009 = false
			cap.PreferredRail = "custodial_relay"
		default:
			cap.PreferredRail = "custodial_relay"
		}
		out = append(out, cap)
	}
	return out
}

func (s *Server) eipCalldataPreview(intent eip712.Intent, signature string, assets []eip712.AssetCapability) map[string]any {
	if strings.TrimSpace(signature) == "" {
		return map[string]any{"available": false, "reason": "signature required"}
	}
	amount, ok := new(big.Int).SetString(strings.TrimSpace(intent.Amount), 10)
	if !ok {
		return map[string]any{"available": false, "reason": "amount must be an integer token base-unit string"}
	}
	rail := eip712.DecideRail(intent.Asset, assets)
	switch rail.PreferredRail {
	case "eip3009_transfer_with_authorization":
		from := firstNonEmpty(intent.Payer, intent.From)
		to := firstNonEmpty(intent.Recipient, intent.To, intent.Payee)
		data, err := eip712.BuildEIP3009TransferWithAuthorizationCalldata(from, to, amount, 0, intent.Deadline, intent.Nonce, signature)
		if err != nil {
			return map[string]any{"available": false, "rail": rail.PreferredRail, "error": err.Error()}
		}
		return map[string]any{"available": true, "rail": rail.PreferredRail, "method": "transferWithAuthorization", "data": data}
	case "eip2612_permit":
		data, err := eip712.BuildEIP2612PermitCalldata(intent.Payer, intent.Payee, amount, intent.Deadline, signature)
		if err != nil {
			return map[string]any{"available": false, "rail": rail.PreferredRail, "error": err.Error()}
		}
		return map[string]any{"available": true, "rail": rail.PreferredRail, "method": "permit", "data": data}
	default:
		return map[string]any{"available": false, "rail": rail.PreferredRail, "reason": "custodial relay does not require token calldata"}
	}
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

func writeEIPError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, eip712.ErrUnknownIntentType):
		writeAPIError(w, r, http.StatusBadRequest, "UNKNOWN_INTENT_TYPE", err.Error())
	case errors.Is(err, eip712.ErrExpiredIntent):
		writeAPIError(w, r, http.StatusBadRequest, "EXPIRED_INTENT", err.Error())
	default:
		writeAPIError(w, r, http.StatusBadRequest, "EIP712_ERROR", err.Error())
	}
}
