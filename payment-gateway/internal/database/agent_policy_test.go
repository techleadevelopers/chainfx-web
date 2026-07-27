package database

import (
	"encoding/json"
	"testing"
)

func TestAgentPolicyJSONListHelpers(t *testing.T) {
	raw, _ := json.Marshal([]string{"document_ocr", "USDT"})
	if !jsonListContains(raw, "document_ocr") {
		t.Fatal("expected capability to be present")
	}
	if !jsonListContains(raw, "usdt") {
		t.Fatal("expected case-insensitive match")
	}
	if jsonListContains(raw, "aml_screening") {
		t.Fatal("did not expect missing capability to match")
	}
	if jsonListEmpty(raw) {
		t.Fatal("expected list not to be empty")
	}
	empty, _ := json.Marshal([]string{})
	if !jsonListEmpty(empty) {
		t.Fatal("expected empty list")
	}
}

func TestAgentPolicyLimitExceeded(t *testing.T) {
	if !limitExceeded("101.00", "100") {
		t.Fatal("expected amount above limit to be rejected")
	}
	if limitExceeded("100.00", "100") {
		t.Fatal("expected amount equal to limit to pass")
	}
	if limitExceeded("1000", "0") {
		t.Fatal("zero limit should mean unlimited")
	}
}

func TestNormalizeAgentPolicyInputDefaults(t *testing.T) {
	policy := normalizeAgentPolicyInput(AgentPolicyInput{})
	if policy.Environment != "sandbox" {
		t.Fatalf("unexpected environment %q", policy.Environment)
	}
	if policy.MockFallback == nil || !*policy.MockFallback {
		t.Fatal("expected mock fallback default enabled")
	}
	if len(policy.Permissions) == 0 {
		t.Fatal("expected default permissions")
	}
}
