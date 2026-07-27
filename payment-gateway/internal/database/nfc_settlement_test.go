package database

import "testing"

func TestClassifyMerchantProviderStatus(t *testing.T) {
	if got := classifyMerchantProviderStatus("REALIZADO"); got != MerchantSettlementStatusConfirmed {
		t.Fatalf("expected confirmed, got %s", got)
	}
	if got := classifyMerchantProviderStatus("REJEITADO"); got != MerchantSettlementStatusRejected {
		t.Fatalf("expected rejected, got %s", got)
	}
	if got := classifyMerchantProviderStatus("EM_PROCESSAMENTO"); got != MerchantSettlementStatusSubmitted {
		t.Fatalf("expected submitted, got %s", got)
	}
}
