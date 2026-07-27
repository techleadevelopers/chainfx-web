package nfc

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
)

type CardApplet struct {
	AID   string
	Label string
	Token string
}

func NewCardApplet(token string) CardApplet {
	return CardApplet{
		AID:   ChainFXAIDHex,
		Label: "ChainFX NFC",
		Token: strings.TrimSpace(token),
	}
}

func (c CardApplet) ProcessCommandAPDU(apdu []byte) ([]byte, error) {
	if len(apdu) < 4 {
		return StatusWrongLength, fmt.Errorf("nfc hce: APDU too short")
	}
	switch {
	case c.isSelectAID(apdu):
		return c.selectResponse()
	case isGPO(apdu), isReadRecord(apdu), isGetData(apdu):
		if strings.TrimSpace(c.Token) == "" {
			return []byte{0x69, 0x85}, fmt.Errorf("nfc hce: token not provisioned")
		}
		return BuildTokenResponse(c.Token)
	default:
		return StatusFailed, fmt.Errorf("nfc hce: unsupported APDU instruction 0x%02X", apdu[1])
	}
}

func (c CardApplet) SelectCommand() ([]byte, error) {
	aid := strings.TrimSpace(c.AID)
	if aid == "" {
		aid = ChainFXAIDHex
	}
	raw, err := hex.DecodeString(aid)
	if err != nil {
		return nil, err
	}
	out := []byte{0x00, 0xA4, 0x04, 0x00, byte(len(raw))}
	out = append(out, raw...)
	return out, nil
}

func (c CardApplet) isSelectAID(apdu []byte) bool {
	selectCmd, err := c.SelectCommand()
	return err == nil && len(apdu) >= len(selectCmd) && bytes.Equal(apdu[:len(selectCmd)], selectCmd)
}

func (c CardApplet) selectResponse() ([]byte, error) {
	aid := strings.TrimSpace(c.AID)
	if aid == "" {
		aid = ChainFXAIDHex
	}
	aidBytes, err := hex.DecodeString(aid)
	if err != nil {
		return StatusFailed, err
	}
	label := strings.TrimSpace(c.Label)
	if label == "" {
		label = "ChainFX NFC"
	}
	a5 := []byte{0x50}
	a5 = append(a5, encodeLength(len([]byte(label)))...)
	a5 = append(a5, []byte(label)...)
	a5 = append(a5, 0x87, 0x01, 0x01)
	body := []byte{0x84}
	body = append(body, encodeLength(len(aidBytes))...)
	body = append(body, aidBytes...)
	body = append(body, 0xA5)
	body = append(body, encodeLength(len(a5))...)
	body = append(body, a5...)
	out := append([]byte{0x6F}, encodeLength(len(body))...)
	out = append(out, body...)
	out = append(out, StatusOK...)
	return out, nil
}

func isGPO(apdu []byte) bool {
	return len(apdu) >= 2 && apdu[1] == 0xA8
}

func isReadRecord(apdu []byte) bool {
	return len(apdu) >= 2 && apdu[1] == 0xB2
}

func isGetData(apdu []byte) bool {
	return len(apdu) >= 2 && apdu[1] == 0xCA
}
