package nfc

import (
	"bytes"
	"testing"
)

func TestCardAppletSelectAndToken(t *testing.T) {
	card := NewCardApplet("nfc1.payload.signature")
	selectCmd, err := card.SelectCommand()
	if err != nil {
		t.Fatalf("SelectCommand() error = %v", err)
	}
	selectResp, err := card.ProcessCommandAPDU(selectCmd)
	if err != nil {
		t.Fatalf("ProcessCommandAPDU(select) error = %v", err)
	}
	if !bytes.Equal(selectResp[len(selectResp)-2:], StatusOK) {
		t.Fatalf("select status mismatch: %X", selectResp)
	}
	tokenResp, err := card.ProcessCommandAPDU([]byte{0x80, 0xCA, 0xDF, 0x01, 0x00})
	if err != nil {
		t.Fatalf("ProcessCommandAPDU(get data) error = %v", err)
	}
	token, err := ParseTokenResponse(tokenResp)
	if err != nil {
		t.Fatalf("ParseTokenResponse() error = %v", err)
	}
	if token != "nfc1.payload.signature" {
		t.Fatalf("token mismatch: %q", token)
	}
}

func TestCardAppletRequiresProvisionedToken(t *testing.T) {
	card := NewCardApplet("")
	resp, err := card.ProcessCommandAPDU([]byte{0x80, 0xCA, 0xDF, 0x01, 0x00})
	if err == nil {
		t.Fatal("expected missing token error")
	}
	if !bytes.Equal(resp, []byte{0x69, 0x85}) {
		t.Fatalf("status mismatch: %X", resp)
	}
}
