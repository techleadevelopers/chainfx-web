package workers

import (
	"testing"

	"payment-gateway/internal/config"
)

// ---------------------------------------------------------------------
// LAYER 4 — ON-CHAIN SETTLEMENT (reorg / confirmation-floor attack)
//
// Attack: an operator (or an attacker who gained control of environment
// configuration) sets BSC_MIN_CONFIRMATIONS / POLYGON_MIN_CONFIRMATIONS
// dangerously low, trying to make the worker treat a deposit as final
// before it actually is — the classic setup for a reorg-based
// double-spend ("race a favor do payout" from the attack map). The
// absolute safety floors in NewOnchainWorker must silently clamp any
// configured value below them, in every code path, with no bypass.
// ---------------------------------------------------------------------

func TestOnchainFloor_MisconfiguredConfirmationsAreClampedNotHonoured(t *testing.T) {
	cfg := &config.Config{
		BscRpcUrls:              "http://127.0.0.1:9999", // never dialed by NewPool — just needs to be non-empty
		BscUsdtContract:         "0x0000000000000000000000000000000000dEaD",
		PolygonRpcUrls:          "http://127.0.0.1:9999",
		PolygonUsdtContract:     "0x0000000000000000000000000000000000dEaD",
		BSCMinConfirmations:     1,  // attacker-controlled: below the floor of 3
		PolygonMinConfirmations: 10, // attacker-controlled: below the floor of 64
	}

	w := NewOnchainWorker(NewEventBus(), nil, cfg)

	var bscConf, polyConf uint64
	for _, n := range w.networks {
		switch n.Name {
		case "BSC":
			bscConf = n.RequiredConfirmations
		case "POLYGON":
			polyConf = n.RequiredConfirmations
		}
	}

	if bscConf < minSafeBSCConfirmations {
		t.Fatalf("🚨 BRECHA DE REORG: BSC_MIN_CONFIRMATIONS=1 (malicioso/errado) não foi clampado; efetivo=%d, piso exigido=%d — permite payout antes da finalidade segura", bscConf, minSafeBSCConfirmations)
	}
	if polyConf < minSafePolygonConfirmations {
		t.Fatalf("🚨 BRECHA DE REORG: POLYGON_MIN_CONFIRMATIONS=10 (malicioso/errado) não foi clampado; efetivo=%d, piso exigido=%d — permite payout antes da finalidade segura", polyConf, minSafePolygonConfirmations)
	}
	t.Logf("[On-Chain Attack] pisos de segurança mantidos: BSC=%d (piso %d) POLYGON=%d (piso %d)", bscConf, minSafeBSCConfirmations, polyConf, minSafePolygonConfirmations)
}

// TestOnchainFloor_ZeroConfigFallsBackToSafeDefaults attacks the other edge:
// an entirely absent config (BSCMinConfirmations == 0, the Go zero value —
// e.g. an env var that failed to parse) must fall back to the safe
// defaults, not to zero confirmations (which would accept an unconfirmed,
// trivially-reorgable transfer as final).
func TestOnchainFloor_ZeroConfigFallsBackToSafeDefaults(t *testing.T) {
	cfg := &config.Config{
		BscRpcUrls:          "http://127.0.0.1:9999",
		BscUsdtContract:     "0x0000000000000000000000000000000000dEaD",
		PolygonRpcUrls:      "http://127.0.0.1:9999",
		PolygonUsdtContract: "0x0000000000000000000000000000000000dEaD",
		// BSCMinConfirmations / PolygonMinConfirmations left at zero value.
	}

	w := NewOnchainWorker(NewEventBus(), nil, cfg)

	for _, n := range w.networks {
		switch n.Name {
		case "BSC":
			if n.RequiredConfirmations != defaultBSCConfirmations {
				t.Fatalf("🚨 config zerada não caiu no default seguro de BSC: got %d, want %d", n.RequiredConfirmations, defaultBSCConfirmations)
			}
		case "POLYGON":
			if n.RequiredConfirmations != defaultPolygonConfirmations {
				t.Fatalf("🚨 config zerada não caiu no default seguro de Polygon: got %d, want %d", n.RequiredConfirmations, defaultPolygonConfirmations)
			}
		}
	}
}
