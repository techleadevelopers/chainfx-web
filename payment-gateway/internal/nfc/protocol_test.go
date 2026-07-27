package nfc

import "testing"

func TestTokenResponseRoundTrip(t *testing.T) {
	token := "nfc1.payload.signature"
	response, err := BuildTokenResponse(token)
	if err != nil {
		t.Fatalf("BuildTokenResponse() error = %v", err)
	}
	got, err := ParseTokenResponse(response)
	if err != nil {
		t.Fatalf("ParseTokenResponse() error = %v", err)
	}
	if got != token {
		t.Fatalf("token mismatch: %q != %q", got, token)
	}
}

func TestParseTokenResponseRejectsNonSuccess(t *testing.T) {
	if _, err := ParseTokenResponse([]byte{0x70, 0x00, 0x6F, 0x00}); err == nil {
		t.Fatal("expected non-success status to fail")
	}
}

func TestParseTokenResponseRejectsMalformedTLV(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0x90, 0x00},
		{0x70, 0x03, 0xDF, 0x01, 0x10, 0x90, 0x00},
		{0x70, 0x02, 0xDF, 0x01, 0x90, 0x00},
		{0x70, 0x03, 0xDF, 0x03, 0x00, 0x90, 0x00},
	}
	for _, tc := range cases {
		if token, err := ParseTokenResponse(tc); err == nil {
			t.Fatalf("expected malformed APDU to fail, got token %q for %x", token, tc)
		}
	}
}

func TestBuildTokenResponseRejectsEmptyToken(t *testing.T) {
	if _, err := BuildTokenResponse(""); err == nil {
		t.Fatal("expected empty token to fail")
	}
}
