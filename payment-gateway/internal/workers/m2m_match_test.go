package workers

import (
	"testing"

	"payment-gateway/internal/database"
)

func TestChooseM2MDepositCandidateSelectsUniqueAmountMatch(t *testing.T) {
	candidates := []database.M2MDepositMatch{
		{IntentID: "pix", RequiredUSDT: 2.140078},
		{IntentID: "card", RequiredUSDT: 2.315175},
	}

	chosen, err := chooseM2MDepositCandidate(candidates, 2.315175, 0.005)
	if err != nil {
		t.Fatalf("expected unique match, got %v", err)
	}
	if chosen.IntentID != "card" {
		t.Fatalf("expected card intent, got %s", chosen.IntentID)
	}
}

func TestChooseM2MDepositCandidateRejectsAmbiguousSameAmount(t *testing.T) {
	candidates := []database.M2MDepositMatch{
		{IntentID: "a", RequiredUSDT: 2.140078},
		{IntentID: "b", RequiredUSDT: 2.140078},
	}

	if _, err := chooseM2MDepositCandidate(candidates, 2.140078, 0.005); err == nil {
		t.Fatal("expected ambiguous same-amount intents to be rejected")
	}
}

func TestChooseM2MDepositCandidateRejectsUnderpayment(t *testing.T) {
	candidates := []database.M2MDepositMatch{
		{IntentID: "pix", RequiredUSDT: 2.140078},
	}

	if _, err := chooseM2MDepositCandidate(candidates, 2.00, 0.005); err == nil {
		t.Fatal("expected underpayment to be rejected")
	}
}
