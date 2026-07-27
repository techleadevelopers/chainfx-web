package bitcoin

import "fmt"

// satsToBTCString converte satoshis para representação BTC como string.
// NUNCA use float64 para aritmética monetária — esta função é somente para exibição.
func satsToBTCString(sats int64) string {
	if sats == 0 {
		return "0.00000000"
	}
	neg := ""
	if sats < 0 {
		neg = "-"
		sats = -sats
	}
	whole := sats / 1_00_000_000
	frac := sats % 1_00_000_000
	return fmt.Sprintf("%s%d.%08d", neg, whole, frac)
}

// varint serializa um inteiro sem sinal no formato varint Bitcoin.
func varint(n uint64) []byte {
	switch {
	case n < 0xfd:
		return []byte{byte(n)}
	case n <= 0xffff:
		return []byte{0xfd, byte(n), byte(n >> 8)}
	case n <= 0xffffffff:
		return []byte{0xfe, byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
	default:
		return []byte{
			0xff,
			byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24),
			byte(n >> 32), byte(n >> 40), byte(n >> 48), byte(n >> 56),
		}
	}
}

// uint32LE serializa um uint32 em little-endian.
func uint32LE(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

// uint64LE serializa um uint64 em little-endian.
func uint64LE(v uint64) []byte {
	return []byte{
		byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24),
		byte(v >> 32), byte(v >> 40), byte(v >> 48), byte(v >> 56),
	}
}

// reverseTxid converte um txid hex para bytes em ordem little-endian
// (Bitcoin usa byte-reversed txids no wire format).
func reverseTxid(txidHex string) ([]byte, error) {
	if len(txidHex) != 64 {
		return nil, fmt.Errorf("txid inválido: %q", txidHex)
	}
	b := make([]byte, 32)
	for i := 0; i < 32; i++ {
		hi := hexNibble(txidHex[i*2])
		lo := hexNibble(txidHex[i*2+1])
		if hi < 0 || lo < 0 {
			return nil, fmt.Errorf("txid contém caractere inválido")
		}
		b[31-i] = byte(hi<<4) | byte(lo)
	}
	return b, nil
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
