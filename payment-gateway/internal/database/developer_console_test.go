package database

import (
	"strings"
	"testing"
)

func TestGenerateDeveloperAPIKeyPairUsesEnvironmentPrefixes(t *testing.T) {
	public, secret, err := generateDeveloperAPIKeyPair("production")
	if err != nil {
		t.Fatalf("generate production key: %v", err)
	}
	if !strings.HasPrefix(public, "pk_live_cfx_") {
		t.Fatalf("unexpected production public prefix: %s", public)
	}
	if !strings.HasPrefix(secret, "sk_live_cfx_") {
		t.Fatalf("unexpected production secret prefix: %s", secret)
	}

	public, secret, err = generateDeveloperAPIKeyPair("sandbox")
	if err != nil {
		t.Fatalf("generate sandbox key: %v", err)
	}
	if !strings.HasPrefix(public, "pk_test_cfx_") {
		t.Fatalf("unexpected sandbox public prefix: %s", public)
	}
	if !strings.HasPrefix(secret, "sk_test_cfx_") {
		t.Fatalf("unexpected sandbox secret prefix: %s", secret)
	}
}

func TestDeveloperKeyLogHashIsStableAndNonPlaintext(t *testing.T) {
	secret := "sk_test_cfx_example"
	first := DeveloperKeyLogHash(secret)
	second := DeveloperKeyLogHash(secret)
	if first != second {
		t.Fatalf("expected stable log hash, got %s and %s", first, second)
	}
	if first == secret || strings.Contains(first, "sk_test") {
		t.Fatalf("log hash leaked secret material: %s", first)
	}
	if len(first) != 16 {
		t.Fatalf("expected 16-char log hash, got %d", len(first))
	}
}

func TestCleanStringListSplitsDeduplicatesAndTrims(t *testing.T) {
	got := cleanStringList([]string{"rates:read, orders:create", "rates:read", " mcp:connect "})
	want := []string{"rates:read", "orders:create", "mcp:connect"}
	if len(got) != len(want) {
		t.Fatalf("expected %d items, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d: got %q want %q", i, got[i], want[i])
		}
	}
}
