package mobile

import (
	"encoding/json"
	"testing"
)

func TestParseDCABRLJSONCanonicalizesNumbersAndStrings(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{`0.01`, "0.01"},
		{`0.10`, "0.10"},
		{`19.99`, "19.99"},
		{`20.00`, "20.00"},
		{`99.99`, "99.99"},
		{`100.00`, "100.00"},
		{`999.99`, "999.99"},
		{`1000.00`, "1000.00"},
		{`"19.99"`, "19.99"},
	} {
		got, _, err := parseDCABRLJSON(json.RawMessage(tc.raw))
		if err != nil {
			t.Fatalf("parse %s: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("parse %s = %s, want %s", tc.raw, got, tc.want)
		}
	}
}

func TestParseDCABRLJSONRejectsImplicitRounding(t *testing.T) {
	if _, _, err := parseDCABRLJSON(json.RawMessage(`19.999`)); err == nil {
		t.Fatal("expected amount with more than 2 decimal places to be rejected")
	}
}
