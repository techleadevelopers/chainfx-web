package e2e_test

import "testing"

func TestMarketplacePurchasePayloadDivergenceReturnsConflict(t *testing.T) {
	c := newE2EClient(t)

	capabilities := c.get("/marketplace/capabilities")
	requireStatus(t, capabilities, 200)
	capabilityID := firstCapabilityID(t, capabilities)

	key := idempotencyKey("marketplace-divergent")
	body := map[string]any{
		"agentWallet":    envWallet("E2E_AGENT_WALLET", testAgentWallet),
		"payerWallet":    envWallet("E2E_PAYER_WALLET", testPayerWallet),
		"paymentAsset":   "USDT",
		"idempotencyKey": key,
		"nonce":          key,
	}
	requireStatus(t, c.post("/marketplace/capabilities/"+capabilityID+"/purchase", body, key), 201)

	divergent := map[string]any{
		"agentWallet":    envWallet("E2E_AGENT_WALLET", testAgentWallet),
		"payerWallet":    envWallet("E2E_PAYER_WALLET", testPayerWallet),
		"paymentAsset":   "USDT",
		"idempotencyKey": key,
		"nonce":          key + "-changed",
	}
	requireStatus(t, c.post("/marketplace/capabilities/"+capabilityID+"/purchase", divergent, key), 409)
}

func TestCapabilityExecuteWithoutAccessTokenFails(t *testing.T) {
	c := newE2EClient(t)
	resp := c.post("/agent/v1/capabilities/llm_chat/execute", map[string]any{
		"operation":      "chat",
		"requestId":      idempotencyKey("execute-no-auth"),
		"idempotencyKey": idempotencyKey("execute-no-auth"),
		"units":          1,
		"input":          map[string]any{"prompt": "ping"},
	}, "")
	requireStatus(t, resp, 401)
}
