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

func TestParseMobilePayCodeValidPixInAlternateMerchantAccountTag(t *testing.T) {
	raw := "000201" +
		"52040000" +
		"5303986" +
		"5406123.00" +
		"5802BR" +
		"5909Santander" +
		"27330014br.gov.bcb.pix0111pix@example" +
		"6304ABCD"
	parsed := parseMobilePayCode(raw)
	if !parsed.Valid {
		t.Fatalf("expected valid Pix BR Code in tag 27, got error=%q", parsed.Error)
	}
	if parsed.BeneficiaryName != "Santander" {
		t.Fatalf("expected merchant Santander, got %q", parsed.BeneficiaryName)
	}
	if key := mobilePixKeyFromBRCode(raw); key != "pix@example" {
		t.Fatalf("expected pix key from alternate merchant account tag, got %q", key)
	}
}

func TestParseMobilePayCodeNormalizesScannerLineBreaks(t *testing.T) {
	raw := "00020126330014BR.GOV.BCB.PIX0111pix@example520400005303986\n5406123.005802BR5904Loja6304ABCD"
	parsed := parseMobilePayCode(raw)
	if !parsed.Valid {
		t.Fatalf("expected valid Pix BR Code with scanner line break, got error=%q", parsed.Error)
	}
	if parsed.RawCode != testMobilePayPixBRCode123 {
		t.Fatalf("expected normalized raw code, got %q", parsed.RawCode)
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
