package e2e_test

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestMCPAgentDiscoversAndCreatesCapabilityPurchase(t *testing.T) {
	c := newE2EClient(t)

	init := c.post("/mcp/initialize", map[string]any{}, "")
	requireStatus(t, init, 200)
	serverInfo, _ := init.Body["serverInfo"].(map[string]any)
	if name := getStringField(serverInfo, "name"); name != "chainfx-mcp" {
		t.Fatalf("unexpected MCP server name %q, body=%s", name, init.Raw)
	}

	tools := c.post("/mcp/tools/list", map[string]any{}, "")
	requireStatus(t, tools, 200)
	if _, ok := tools.Body["tools"]; !ok {
		t.Fatalf("MCP tools response missing tools: %s", tools.Raw)
	}

	capabilities := c.get("/marketplace/capabilities")
	requireStatus(t, capabilities, 200)
	capabilityID := firstCapabilityID(t, capabilities)

	agent := c.post("/agent/connect", map[string]any{
		"name":        "e2e-agent",
		"agentWallet": envWallet("E2E_AGENT_WALLET", testAgentWallet),
	}, idempotencyKey("agent-connect"))
	requireStatus(t, agent, 201, 409)

	key := idempotencyKey("capability-purchase")
	request := map[string]any{
		"agentWallet":    envWallet("E2E_AGENT_WALLET", testAgentWallet),
		"payerWallet":    envWallet("E2E_PAYER_WALLET", testPayerWallet),
		"paymentAsset":   getenv("E2E_PAYMENT_ASSET", "USDT"),
		"idempotencyKey": key,
		"nonce":          key,
	}
	purchase := c.post("/marketplace/capabilities/"+capabilityID+"/purchase", request, key)
	requireStatus(t, purchase, 201)
	purchaseID := requireStringField(t, purchase.Body, "purchaseId", "id")

	replay := c.post("/marketplace/capabilities/"+capabilityID+"/purchase", request, key)
	requireStatus(t, replay, 200, 201)
	if replayID := getStringField(replay.Body, "purchaseId", "id"); replayID != "" && replayID != purchaseID {
		t.Fatalf("idempotent replay returned different purchase id: got %s want %s", replayID, purchaseID)
	}
}

func TestMCPAgentTestnetPaymentExecuteCanary(t *testing.T) {
	if os.Getenv("RUN_TESTNET_PAYMENT_TESTS") != "true" {
		t.Skip("set RUN_TESTNET_PAYMENT_TESTS=true to verify an on-chain testnet transaction")
	}
	c := newE2EClient(t)
	txHash := strings.TrimSpace(os.Getenv("E2E_TEST_TX_HASH"))
	if txHash == "" {
		t.Skip("set E2E_TEST_TX_HASH after sending the testnet ERC20 transfer")
	}
	logIndex, err := strconv.Atoi(getenv("E2E_TEST_LOG_INDEX", "0"))
	if err != nil || logIndex < 0 {
		t.Fatalf("E2E_TEST_LOG_INDEX must be a non-negative integer")
	}

	capabilities := c.get("/marketplace/capabilities")
	requireStatus(t, capabilities, 200)
	capabilityID := firstCapabilityID(t, capabilities)

	key := idempotencyKey("testnet-capability")
	purchase := c.post("/marketplace/capabilities/"+capabilityID+"/purchase", map[string]any{
		"agentWallet":    envWallet("E2E_AGENT_WALLET", testAgentWallet),
		"payerWallet":    envWallet("E2E_PAYER_WALLET", testPayerWallet),
		"paymentAsset":   getenv("E2E_PAYMENT_ASSET", "USDT"),
		"idempotencyKey": key,
		"nonce":          key,
	}, key)
	requireStatus(t, purchase, 201)
	purchaseID := requireStringField(t, purchase.Body, "purchaseId", "id")

	executed := c.post("/marketplace/purchase/"+purchaseID+"/execute", map[string]any{
		"txHash":   txHash,
		"logIndex": logIndex,
	}, idempotencyKey("testnet-execute"))
	requireStatus(t, executed, 200)

	accessToken := getStringField(executed.Body, "accessToken")
	if accessToken == "" {
		t.Fatalf("execute response missing access token: %s", executed.Raw)
	}
}

func TestLivePaymentCanaryRequiresExplicitConfirmation(t *testing.T) {
	if os.Getenv("RUN_LIVE_PAYMENT_TESTS") != "true" {
		t.Skip("set RUN_LIVE_PAYMENT_TESTS=true for live payment canary")
	}
	if os.Getenv("LIVE_PAYMENT_CONFIRMATION_REQUIRED") != "true" {
		t.Fatal("LIVE_PAYMENT_CONFIRMATION_REQUIRED must stay true for live canary tests")
	}
	if os.Getenv("LIVE_PAYMENT_MAX_USD") == "" {
		t.Fatal("set LIVE_PAYMENT_MAX_USD to cap the live canary amount")
	}
	if os.Getenv("RUN_TESTNET_PAYMENT_TESTS") != "true" {
		t.Fatal("run and pass testnet payment tests before live canary")
	}
	t.Skip("live payment canary is intentionally manual: send the controlled payment, then run the testnet execute flow with live-only credentials")
}
