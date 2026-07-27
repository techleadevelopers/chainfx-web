package server

import (
	"errors"
	"testing"
)

func TestX402PolicyPaymentRequiredMapsPolicyFailuresToChallenge(t *testing.T) {
	payload, ok := x402PolicyPaymentRequired(errors.New("AGENT_POLICY_REQUIRED: Agent policy is required for this operation."))
	if !ok {
		t.Fatal("expected policy failure to be mapped to x402 payment-required payload")
	}
	if payload["code"] != "AGENT_POLICY_REQUIRED" {
		t.Fatalf("unexpected code: %v", payload["code"])
	}
	if payload["policy_discovery"] != "/.well-known/agent-policy.json" {
		t.Fatalf("unexpected policy discovery link: %v", payload["policy_discovery"])
	}
	if payload["capability_graph"] != "/.well-known/capability-graph.json" {
		t.Fatalf("unexpected capability graph link: %v", payload["capability_graph"])
	}
	if payload["next_action"] == "" {
		t.Fatal("expected next_action guidance for autonomous agents")
	}
}

func TestX402PolicyPaymentRequiredIgnoresGenericErrors(t *testing.T) {
	if payload, ok := x402PolicyPaymentRequired(errors.New("capability not found")); ok {
		t.Fatalf("expected generic error to remain non-x402 validation error, got %#v", payload)
	}
}
