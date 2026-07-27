package bitcoin

import (
	"crypto/sha256"
	"errors"
	"math/big"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var bigZero = big.NewInt(0)
var bigBase = big.NewInt(58)

// base58Decode decodifica uma string base58 para bytes (sem checksum).
func base58Decode(s string) ([]byte, error) {
	result := big.NewInt(0)
	for _, c := range s {
		idx := -1
		for i, ac := range base58Alphabet {
			if ac == c {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, errors.New("base58: caractere inválido")
		}
		result.Mul(result, bigBase)
		result.Add(result, big.NewInt(int64(idx)))
	}

	decoded := result.Bytes()

	// Contar zeros à esquerda
	numLeadingZeros := 0
	for _, c := range s {
		if c == '1' {
			numLeadingZeros++
		} else {
			break
		}
	}

	out := make([]byte, numLeadingZeros+len(decoded))
	copy(out[numLeadingZeros:], decoded)
	return out, nil
}

// base58CheckDecode decodifica uma string base58check, verifica o checksum e
// retorna o payload (sem os 4 bytes de checksum).
func base58CheckDecode(s string) ([]byte, error) {
	decoded, err := base58Decode(s)
	if err != nil {
		return nil, err
	}
	if len(decoded) < 4 {
		return nil, errors.New("base58check: dados muito curtos")
	}
	payload := decoded[:len(decoded)-4]
	checksum := decoded[len(decoded)-4:]
	expected := dsha256(payload)
	if expected[0] != checksum[0] || expected[1] != checksum[1] ||
		expected[2] != checksum[2] || expected[3] != checksum[3] {
		return nil, errors.New("base58check: checksum inválido")
	}
	return payload, nil
}

// dsha256 computa SHA256(SHA256(data)).
func dsha256(data []byte) []byte {
	h1 := sha256.Sum256(data)
	h2 := sha256.Sum256(h1[:])
	return h2[:]
}

// base58CheckEncode encoda payload em base58check.
func base58CheckEncode(payload []byte) string {
	checksum := dsha256(payload)
	full := append(payload, checksum[:4]...)
	return base58Encode(full)
}

func base58Encode(data []byte) string {
	// Contar bytes zero à esquerda
	numLeadingZeros := 0
	for _, b := range data {
		if b == 0 {
			numLeadingZeros++
		} else {
			break
		}
	}

	n := new(big.Int).SetBytes(data)
	var result []byte
	mod := new(big.Int)
	for n.Cmp(bigZero) > 0 {
		n.DivMod(n, bigBase, mod)
		result = append(result, base58Alphabet[mod.Int64()])
	}
	for i := 0; i < numLeadingZeros; i++ {
		result = append(result, base58Alphabet[0])
	}
	// Reverter
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return string(result)
}
