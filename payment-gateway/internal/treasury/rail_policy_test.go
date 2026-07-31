package treasury

import (
	"testing"

	"payment-gateway/internal/config"
)

const testCodeHash = "0x1111111111111111111111111111111111111111111111111111111111111111"

func TestResolveRailAllows7702OnlyForBSCAndPolygon(t *testing.T) {
	cfg := &config.Config{
		Treasury7702Enabled:         true,
		Treasury7702Networks:        "BSC,POLYGON,ETHEREUM,ARBITRUM,BASE",
		BSC7702Delegate:             "0x1111111111111111111111111111111111111111",
		BSC7702DelegateCodeHash:     testCodeHash,
		Polygon7702Delegate:         "0x2222222222222222222222222222222222222222",
		Polygon7702DelegateCodeHash: testCodeHash,
	}
	for _, network := range []string{"BSC", "POLYGON"} {
		got := ResolveRail(cfg, network, StateCreated, false)
		if got.CurrentRail != RailDelegate7702 || !got.Enabled7702 {
			t.Fatalf("%s should allow delegate rail, got %+v", network, got)
		}
	}
	for _, network := range []string{"ETHEREUM", "ARBITRUM", "BASE", "EVM"} {
		got := ResolveRail(cfg, network, StateCreated, false)
		if got.CurrentRail != RailLegacy || got.Enabled7702 {
			t.Fatalf("%s must stay legacy even when configured, got %+v", network, got)
		}
	}
}

func TestResolveRailFallsBackLegacyBeforeDelegateState(t *testing.T) {
	cfg := &config.Config{Treasury7702Enabled: true, Treasury7702Networks: "BSC"}
	got := ResolveRail(cfg, "BSC", StateCreated, false)
	if got.CurrentRail != RailLegacy || got.Fallback != FallbackLegacy {
		t.Fatalf("missing delegate config before delegate state should use legacy fallback, got %+v", got)
	}
}

func TestResolveRailManualReviewAfterDelegateStateOrAmbiguous(t *testing.T) {
	cfg := &config.Config{
		Treasury7702Enabled:     true,
		Treasury7702Networks:    "BSC",
		BSC7702Delegate:         "0x1111111111111111111111111111111111111111",
		BSC7702DelegateCodeHash: testCodeHash,
	}
	for _, state := range []OperationState{StateDelegatePrepared, StateBroadcastUnknown, StateBroadcast} {
		got := ResolveRail(&config.Config{Treasury7702Enabled: true, Treasury7702Networks: "BSC"}, "BSC", state, false)
		if got.Fallback != FallbackManualReview {
			t.Fatalf("delegate state %s with missing config must manual_review, got %+v", state, got)
		}
	}
	got := ResolveRail(cfg, "BSC", StateBroadcastUnknown, false)
	if got.Fallback != FallbackManualReview {
		t.Fatalf("broadcast_unknown must reconcile/manual_review before fallback, got %+v", got)
	}
}

func TestResolveRailEmergencyLockdownManualReview(t *testing.T) {
	cfg := &config.Config{
		Treasury7702Enabled:         true,
		Treasury7702Networks:        "POLYGON",
		Polygon7702Delegate:         "0x2222222222222222222222222222222222222222",
		Polygon7702DelegateCodeHash: testCodeHash,
	}
	got := ResolveRail(cfg, "POLYGON", StateCreated, true)
	if got.Fallback != FallbackManualReview || got.Enabled7702 {
		t.Fatalf("lockdown must fail closed, got %+v", got)
	}
}
