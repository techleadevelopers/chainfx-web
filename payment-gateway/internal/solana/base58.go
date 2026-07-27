package solana

import (
	"errors"
	"math/big"
	"strings"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func base58Encode(data []byte) string {
	x := new(big.Int).SetBytes(data)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := new(big.Int)
	var out []byte
	for x.Cmp(zero) > 0 {
		x.DivMod(x, base, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for _, b := range data {
		if b != 0 {
			break
		}
		out = append(out, base58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func base58Decode(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("solana: endereco vazio")
	}
	x := big.NewInt(0)
	base := big.NewInt(58)
	for _, ch := range value {
		idx := strings.IndexRune(base58Alphabet, ch)
		if idx < 0 {
			return nil, errors.New("solana: base58 invalido")
		}
		x.Mul(x, base)
		x.Add(x, big.NewInt(int64(idx)))
	}
	out := x.Bytes()
	leadingZeros := 0
	for _, ch := range value {
		if ch != rune(base58Alphabet[0]) {
			break
		}
		leadingZeros++
	}
	if leadingZeros > 0 {
		out = append(make([]byte, leadingZeros), out...)
	}
	return out, nil
}

func ValidateAddress(address string) error {
	raw, err := base58Decode(address)
	if err != nil {
		return err
	}
	if len(raw) != 32 {
		return errors.New("solana: endereco deve decodificar para 32 bytes")
	}
	return nil
}
