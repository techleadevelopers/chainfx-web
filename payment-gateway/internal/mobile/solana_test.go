package mobile

import (
	"encoding/json"
	"testing"
)

func TestParseSolLamportsJSONAcceptsExactInteger(t *testing.T) {
	got, err := parseSolLamportsJSON(json.RawMessage(`"123456789"`))
	if err != nil {
		t.Fatalf("parseSolLamportsJSON: %v", err)
	}
	if got != 123456789 {
		t.Fatalf("lamports=%d want 123456789", got)
	}
}

func TestParseSolLamportsJSONRejectsDecimalFloat(t *testing.T) {
	if _, err := parseSolLamportsJSON(json.RawMessage(`0.1`)); err == nil {
		t.Fatal("expected decimal amount_lamports to be rejected")
	}
}
