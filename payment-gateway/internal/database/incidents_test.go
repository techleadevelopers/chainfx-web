package database

import (
	"strings"
	"testing"
)

func TestDocumentDigitMatching(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		payer    string
		match    bool
	}{
		{name: "full cpf", expected: "123.456.789-09", payer: "12345678909", match: true},
		{name: "efi masked middle digits", expected: "123.456.789-09", payer: ".123.456-", match: true},
		{name: "masked mismatch", expected: "123.456.789-09", payer: ".999.456-", match: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Contains(onlyDigits(tt.expected), onlyDigits(tt.payer))
			if got != tt.match {
				t.Fatalf("match=%v want %v", got, tt.match)
			}
		})
	}
}
