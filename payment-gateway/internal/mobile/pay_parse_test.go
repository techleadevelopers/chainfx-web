package mobile

import "testing"

const testMobilePayPixBRCode123 = "00020126330014BR.GOV.BCB.PIX0111pix@example5204000053039865406123.005802BR5904Loja6304ABCD"

func TestParseMobilePayCodeValidPix(t *testing.T) {
	parsed := parseMobilePayCode(testMobilePayPixBRCode123)
	if !parsed.Valid {
		t.Fatalf("expected valid Pix BR Code, got error=%q", parsed.Error)
	}
	if parsed.PaymentType != "pix" {
		t.Fatalf("expected pix, got %q", parsed.PaymentType)
	}
	if parsed.BeneficiaryName != "Loja" {
		t.Fatalf("expected merchant Loja, got %q", parsed.BeneficiaryName)
	}
	if parsed.AmountBRL != 123 {
		t.Fatalf("expected amount 123.00, got %.2f", parsed.AmountBRL)
	}
	if parsed.ID == "" || parsed.RawCode == "" {
		t.Fatalf("expected canonical parsed id and raw code, got %+v", parsed)
	}
}

func TestParseMobilePayCodeRejectsInvalidPayload(t *testing.T) {
	parsed := parseMobilePayCode("not-a-pix-qr")
	if parsed.Valid {
		t.Fatalf("random payload must not be valid: %+v", parsed)
	}
}

func TestParseMobilePayCodeRejectsPixWithoutAmount(t *testing.T) {
	raw := "00020126330014BR.GOV.BCB.PIX0111pix@example5204000053039865802BR5904Loja6304ABCD"
	parsed := parseMobilePayCode(raw)
	if parsed.Valid {
		t.Fatalf("QR Pix without amount must not enter automatic QR Pay: %+v", parsed)
	}
}

func TestParseMobilePayCodeRejectsPixWithoutKey(t *testing.T) {
	raw := "00020126180014BR.GOV.BCB.PIX5204000053039865406123.005802BR5904Loja6304ABCD"
	parsed := parseMobilePayCode(raw)
	if parsed.Valid {
		t.Fatalf("QR Pix without key must not enter automatic QR Pay: %+v", parsed)
	}
}
