package bitcoin

import (
	"crypto/sha256"
	"errors"
	"strings"

	"golang.org/x/crypto/ripemd160"
)

// bech32Charset são os 32 caracteres válidos do bech32.
const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

var bech32Generator = [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}

// P2WPKHAddress deriva o endereço P2WPKH (native SegWit bech32) de uma pubkey comprimida.
// pubKey deve ter 33 bytes (formato comprimido).
func P2WPKHAddress(pubKey []byte, hrp string) (string, error) {
	if len(pubKey) != 33 {
		return "", errors.New("bech32: pubkey deve ter 33 bytes")
	}
	h160 := hash160PubKey(pubKey)
	return segwitAddrEncode(hrp, 0, h160)
}

// P2WPKHScript retorna o scriptPubKey para um endereço P2WPKH.
// Formato: OP_0 OP_PUSHBYTES_20 <hash160> = 0x0014<20bytes>
func P2WPKHScript(pubKey []byte) ([]byte, error) {
	if len(pubKey) != 33 {
		return nil, errors.New("bech32: pubkey deve ter 33 bytes")
	}
	h160 := hash160PubKey(pubKey)
	script := make([]byte, 22)
	script[0] = 0x00 // OP_0
	script[1] = 0x14 // PUSH 20 bytes
	copy(script[2:], h160)
	return script, nil
}

// Hash160FromScript extrai os 20 bytes de hash160 de um script P2WPKH.
func Hash160FromScript(script []byte) ([]byte, error) {
	if len(script) != 22 || script[0] != 0x00 || script[1] != 0x14 {
		return nil, errors.New("bech32: não é um script P2WPKH")
	}
	return script[2:], nil
}

// Hash160FromAddress decodifica um endereço bech32 P2WPKH e retorna os 20 bytes de hash160.
func Hash160FromAddress(addr, hrp string) ([]byte, error) {
	ver, prog, err := segwitAddrDecode(hrp, addr)
	if err != nil {
		return nil, err
	}
	if ver != 0 || len(prog) != 20 {
		return nil, errors.New("bech32: não é um endereço P2WPKH")
	}
	return prog, nil
}

// ValidateAddress verifica se addr é um endereço bech32 válido para o hrp informado.
func ValidateAddress(addr, hrp string) error {
	_, _, err := segwitAddrDecode(hrp, addr)
	return err
}

// ─── implementação bech32 ─────────────────────────────────────────────────────

func bech32Polymod(values []byte) uint32 {
	chk := uint32(1)
	for _, v := range values {
		b := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (b>>uint(i))&1 == 1 {
				chk ^= bech32Generator[i]
			}
		}
	}
	return chk
}

func bech32HRPExpand(hrp string) []byte {
	lower := strings.ToLower(hrp)
	ret := make([]byte, len(lower)*2+1)
	for i, c := range lower {
		ret[i] = byte(c >> 5)
		ret[i+len(lower)+1] = byte(c & 31)
	}
	return ret
}

func convertBits(data []byte, fromBits, toBits uint, pad bool) ([]byte, error) {
	acc := 0
	bits := uint(0)
	maxv := (1 << toBits) - 1
	maxAcc := (1 << (fromBits + toBits - 1)) - 1
	var ret []byte
	for _, value := range data {
		if int(value)>>fromBits != 0 {
			return nil, errors.New("bech32: valor fora do range")
		}
		acc = ((acc << fromBits) | int(value)) & maxAcc
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			ret = append(ret, byte((acc>>bits)&maxv))
		}
	}
	if pad {
		if bits > 0 {
			ret = append(ret, byte((acc<<(toBits-bits))&maxv))
		}
	} else if bits >= fromBits || ((acc<<(toBits-bits))&maxv) != 0 {
		return nil, errors.New("bech32: padding inválido")
	}
	return ret, nil
}

func bech32Encode(hrp string, data []byte) (string, error) {
	combined := append(bech32HRPExpand(hrp), data...)
	combined = append(combined, 0, 0, 0, 0, 0, 0)
	mod := bech32Polymod(combined) ^ 1
	checksum := make([]byte, 6)
	for i := range checksum {
		checksum[i] = byte((mod >> (5 * uint(5-i))) & 31)
	}
	payload := append(data, checksum...)
	var sb strings.Builder
	sb.WriteString(strings.ToLower(hrp))
	sb.WriteByte('1')
	for _, b := range payload {
		sb.WriteByte(bech32Charset[b])
	}
	return sb.String(), nil
}

func bech32Decode(bechString string) (string, []byte, error) {
	lower := strings.ToLower(bechString)
	if bechString != lower && bechString != strings.ToUpper(bechString) {
		return "", nil, errors.New("bech32: mistura de maiúsculas e minúsculas")
	}
	sep := strings.LastIndex(lower, "1")
	if sep < 1 || sep+7 > len(lower) {
		return "", nil, errors.New("bech32: separador '1' ausente ou posição inválida")
	}
	hrp := lower[:sep]
	var data []byte
	for _, c := range lower[sep+1:] {
		idx := strings.IndexRune(bech32Charset, c)
		if idx < 0 {
			return "", nil, errors.New("bech32: caractere inválido")
		}
		data = append(data, byte(idx))
	}
	combined := append(bech32HRPExpand(hrp), data...)
	if bech32Polymod(combined) != 1 {
		return "", nil, errors.New("bech32: checksum inválido")
	}
	return hrp, data[:len(data)-6], nil
}

func segwitAddrEncode(hrp string, version int, program []byte) (string, error) {
	if version < 0 || version > 16 {
		return "", errors.New("bech32: versão segwit inválida")
	}
	if len(program) < 2 || len(program) > 40 {
		return "", errors.New("bech32: comprimento do programa inválido")
	}
	fiveBit, err := convertBits(program, 8, 5, true)
	if err != nil {
		return "", err
	}
	payload := append([]byte{byte(version)}, fiveBit...)
	return bech32Encode(hrp, payload)
}

func segwitAddrDecode(hrp, addr string) (int, []byte, error) {
	decodedHRP, data, err := bech32Decode(addr)
	if err != nil {
		return 0, nil, err
	}
	if decodedHRP != strings.ToLower(hrp) {
		return 0, nil, errors.New("bech32: hrp não corresponde")
	}
	if len(data) < 1 {
		return 0, nil, errors.New("bech32: dados vazios")
	}
	version := int(data[0])
	if version > 16 {
		return 0, nil, errors.New("bech32: versão segwit inválida")
	}
	prog, err := convertBits(data[1:], 5, 8, false)
	if err != nil {
		return 0, nil, err
	}
	if len(prog) < 2 || len(prog) > 40 {
		return 0, nil, errors.New("bech32: comprimento do programa inválido")
	}
	if version == 0 && len(prog) != 20 && len(prog) != 32 {
		return 0, nil, errors.New("bech32: programa v0 deve ter 20 ou 32 bytes")
	}
	return version, prog, nil
}

// ─── hash helpers ─────────────────────────────────────────────────────────────

func hash160PubKey(pubKey []byte) []byte {
	h := sha256.Sum256(pubKey)
	r := ripemd160.New()
	r.Write(h[:])
	return r.Sum(nil)
}
