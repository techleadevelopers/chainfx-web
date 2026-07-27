package nfc

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	ChainFXAIDHex = "F222222222"
	TokenTag      = 0xDF01
	VersionTag    = 0xDF02
)

var (
	StatusOK          = []byte{0x90, 0x00}
	StatusFailed      = []byte{0x6F, 0x00}
	StatusWrongLength = []byte{0x67, 0x00}
)

func BuildTokenResponse(token string) ([]byte, error) {
	if token == "" {
		return nil, fmt.Errorf("nfc: token is required")
	}
	body := appendTLV(nil, VersionTag, []byte{0x01})
	body = appendTLV(body, TokenTag, []byte(token))
	out := append([]byte{0x70}, encodeLength(len(body))...)
	out = append(out, body...)
	out = append(out, StatusOK...)
	return out, nil
}

func ParseTokenResponse(apdu []byte) (string, error) {
	if len(apdu) < 4 {
		return "", fmt.Errorf("nfc: response too short")
	}
	if !bytes.Equal(apdu[len(apdu)-2:], StatusOK) {
		return "", fmt.Errorf("nfc: response status is not 9000")
	}
	payload := apdu[:len(apdu)-2]
	if len(payload) < 2 || payload[0] != 0x70 {
		return "", fmt.Errorf("nfc: missing response template")
	}
	templateLen, used, err := decodeLength(payload[1:])
	if err != nil {
		return "", err
	}
	start := 1 + used
	if len(payload[start:]) != templateLen {
		return "", fmt.Errorf("nfc: invalid template length")
	}
	fields, err := parseTLV(payload[start:])
	if err != nil {
		return "", err
	}
	token := string(fields[TokenTag])
	if token == "" {
		return "", fmt.Errorf("nfc: token tag missing")
	}
	return token, nil
}

func appendTLV(dst []byte, tag uint16, value []byte) []byte {
	var tagBytes [2]byte
	binary.BigEndian.PutUint16(tagBytes[:], tag)
	dst = append(dst, tagBytes[:]...)
	dst = append(dst, encodeLength(len(value))...)
	dst = append(dst, value...)
	return dst
}

func encodeLength(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	if n <= 0xff {
		return []byte{0x81, byte(n)}
	}
	return []byte{0x82, byte(n >> 8), byte(n)}
}

func decodeLength(raw []byte) (int, int, error) {
	if len(raw) == 0 {
		return 0, 0, fmt.Errorf("nfc: missing length")
	}
	if raw[0] < 0x80 {
		return int(raw[0]), 1, nil
	}
	switch raw[0] {
	case 0x81:
		if len(raw) < 2 {
			return 0, 0, fmt.Errorf("nfc: short extended length")
		}
		return int(raw[1]), 2, nil
	case 0x82:
		if len(raw) < 3 {
			return 0, 0, fmt.Errorf("nfc: short extended length")
		}
		return int(binary.BigEndian.Uint16(raw[1:3])), 3, nil
	default:
		return 0, 0, fmt.Errorf("nfc: unsupported length encoding")
	}
}

func parseTLV(raw []byte) (map[uint16][]byte, error) {
	fields := make(map[uint16][]byte)
	for len(raw) > 0 {
		if len(raw) < 3 {
			return nil, fmt.Errorf("nfc: short tlv")
		}
		tag := binary.BigEndian.Uint16(raw[:2])
		length, used, err := decodeLength(raw[2:])
		if err != nil {
			return nil, err
		}
		start := 2 + used
		end := start + length
		if end > len(raw) {
			return nil, fmt.Errorf("nfc: tlv length overflow")
		}
		fields[tag] = raw[start:end]
		raw = raw[end:]
	}
	return fields, nil
}
