package workers

import (
	"os"
	"strings"
	"testing"
)

func TestSwapWorkerDoesNotContainLegacyFalseSuccessSimulation(t *testing.T) {
	raw, err := os.ReadFile("swap.go")
	if err != nil {
		t.Fatalf("read swap.go: %v", err)
	}
	src := string(raw)
	for _, forbidden := range []string{
		"0xswap_",
		"Simulate on-chain execution",
		"legacy BRL-price simulation",
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("swap worker still contains false-success simulation marker %q", forbidden)
		}
	}
	for _, required := range []string{
		"!w.cfg.MobileSwapPancakeEnabled",
		"markSwapRouteUnavailable",
		"status='route_unavailable'",
		"executePancakeSwap",
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("swap worker real-execution invariant missing: %s", required)
		}
	}
}
