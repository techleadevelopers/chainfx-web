package treasury

import (
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"payment-gateway/internal/config"
)

const (
	RailLegacy           = "legacy"
	RailDelegate7702     = "delegate_7702"
	FallbackLegacy       = "legacy"
	FallbackManualReview = "manual_review"
)

type OperationState string

const (
	StateCreated          OperationState = "created"
	StateDelegatePrepared OperationState = "delegate_prepared"
	StateSigned           OperationState = "signed"
	StateBroadcastUnknown OperationState = "broadcast_unknown"
	StateBroadcast        OperationState = "broadcast"
	StateConfirmed        OperationState = "confirmed"
)

type RailDecision struct {
	Network         string
	CurrentRail     string
	Enabled7702     bool
	DelegateAddress string
	Fallback        string
	Reason          string
}

func ResolveRail(cfg *config.Config, network string, state OperationState, emergencyLockdown bool) RailDecision {
	n := normalizeNetwork(network)
	decision := RailDecision{Network: n, CurrentRail: RailLegacy, Fallback: FallbackLegacy}
	if n == "" {
		decision.Network = "UNKNOWN"
		decision.Reason = "network_unknown"
		return decision
	}
	if !networkPermits7702(n) {
		decision.Reason = "network_forced_legacy"
		return decision
	}
	if cfg == nil || !cfg.Treasury7702Enabled {
		decision.Reason = "feature_disabled"
		return decision
	}
	if !csvContainsNetwork(cfg.Treasury7702Networks, n) {
		decision.Reason = "network_not_enabled"
		return decision
	}
	if emergencyLockdown {
		return manualReviewDecision(n, "emergency_lockdown")
	}
	delegate, codeHash := delegateConfig(cfg, n)
	if !common.IsHexAddress(delegate) || !isBytes32Hex(codeHash) {
		if stateStartedDelegate(state) {
			return manualReviewDecision(n, "delegate_config_missing_after_delegate_state")
		}
		decision.Reason = "delegate_config_missing"
		return decision
	}
	if state == StateBroadcastUnknown || state == StateBroadcast || state == StateConfirmed {
		return manualReviewDecision(n, "delegate_state_requires_reconciliation_first")
	}
	decision.CurrentRail = RailDelegate7702
	decision.Enabled7702 = true
	decision.DelegateAddress = common.HexToAddress(delegate).Hex()
	decision.Fallback = FallbackLegacy
	decision.Reason = "delegate_ready_for_bsc_polygon"
	return decision
}

func Matrix(cfg *config.Config) []RailDecision {
	networks := []string{"BSC", "POLYGON", "ETHEREUM", "ARBITRUM", "BASE"}
	out := make([]RailDecision, 0, len(networks))
	for _, network := range networks {
		out = append(out, ResolveRail(cfg, network, StateCreated, false))
	}
	return out
}

func manualReviewDecision(network, reason string) RailDecision {
	return RailDecision{Network: network, CurrentRail: RailLegacy, Fallback: FallbackManualReview, Reason: reason}
}

func stateStartedDelegate(state OperationState) bool {
	switch state {
	case StateDelegatePrepared, StateSigned, StateBroadcastUnknown, StateBroadcast, StateConfirmed:
		return true
	default:
		return false
	}
}

func delegateConfig(cfg *config.Config, network string) (string, string) {
	switch normalizeNetwork(network) {
	case "BSC":
		return cfg.BSC7702Delegate, cfg.BSC7702DelegateCodeHash
	case "POLYGON":
		return cfg.Polygon7702Delegate, cfg.Polygon7702DelegateCodeHash
	default:
		return "", ""
	}
}

func networkPermits7702(network string) bool {
	switch normalizeNetwork(network) {
	case "BSC", "POLYGON":
		return true
	default:
		return false
	}
}

func normalizeNetwork(network string) string {
	n := strings.ToUpper(strings.TrimSpace(network))
	switch n {
	case "BINANCE", "BEP20":
		return "BSC"
	case "MATIC":
		return "POLYGON"
	default:
		return n
	}
}

func csvContainsNetwork(raw, network string) bool {
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		if normalizeNetwork(item) == normalizeNetwork(network) {
			return true
		}
	}
	return false
}

func isBytes32Hex(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return false
	}
	for _, r := range value[2:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}
