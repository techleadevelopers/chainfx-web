package bitcoin

import (
	"math/big"
	"testing"
)

// ─── Testes de bech32 ─────────────────────────────────────────────────────────

func TestBech32Encode(t *testing.T) {
	// Vetor de teste: endereço P2WPKH testnet conhecido
	prog := mustHexBytes(t, "751e76e8199196f38d3dba9f7b0bcee04b6e7de4")
	addr, err := segwitAddrEncode("tb", 0, prog)
	if err != nil {
		t.Fatalf("segwitAddrEncode: %v", err)
	}
	want := "tb1qw508d6qejxt08rfah20hkz7wup9kul0yuthvdd"
	if addr != want {
		t.Errorf("got %q want %q", addr, want)
	}
}

func TestBech32Decode(t *testing.T) {
	addr := "tb1qw508d6qejxt08rfah20hkz7wup9kul0yuthvdd"
	ver, prog, err := segwitAddrDecode("tb", addr)
	if err != nil {
		t.Fatalf("segwitAddrDecode: %v", err)
	}
	if ver != 0 {
		t.Errorf("witness version: got %d want 0", ver)
	}
	if len(prog) != 20 {
		t.Errorf("program length: got %d want 20", len(prog))
	}
	if string(prog) != string(mustHexBytes(t, "751e76e8199196f38d3dba9f7b0bcee04b6e7de4")) {
		t.Errorf("program bytes mismatch")
	}
}

func TestValidateAddress_Valid(t *testing.T) {
	tests := []struct{ addr, hrp string }{
		{"tb1qw508d6qejxt08rfah20hkz7wup9kul0yuthvdd", "tb"},
	}
	for _, tt := range tests {
		if err := ValidateAddress(tt.addr, tt.hrp); err != nil {
			t.Errorf("ValidateAddress(%q,%q) = %v, want nil", tt.addr, tt.hrp, err)
		}
	}
}

func TestValidateAddress_Invalid(t *testing.T) {
	invalid := []struct{ addr, hrp string }{
		{"not-an-address", "tb"},
		{"", "tb"},
		{"bc1qfake", "tb"}, // hrp errado
	}
	for _, tt := range invalid {
		if err := ValidateAddress(tt.addr, tt.hrp); err == nil {
			t.Errorf("ValidateAddress(%q,%q) = nil, want error", tt.addr, tt.hrp)
		}
	}
}

// ─── Testes de base58 ─────────────────────────────────────────────────────────

func TestBase58CheckRoundtrip(t *testing.T) {
	payload := []byte("hello bitcoin world test payload")
	encoded := base58CheckEncode(payload)
	decoded, err := base58CheckDecode(encoded)
	if err != nil {
		t.Fatalf("base58CheckDecode: %v", err)
	}
	if string(decoded) != string(payload) {
		t.Errorf("roundtrip: got %q want %q", decoded, payload)
	}
}

func TestBase58CheckInvalidChecksum(t *testing.T) {
	encoded := "1AGNa15ZQXAZUgFiqJ2i7Z2DPU2J6hW62i" // válido
	// Corromper o último caractere
	corrupted := encoded[:len(encoded)-1] + "x"
	_, err := base58CheckDecode(corrupted)
	if err == nil {
		t.Error("esperava erro de checksum inválido")
	}
}

// ─── Testes de BIP32 ─────────────────────────────────────────────────────────

func TestParseXPubInvalid(t *testing.T) {
	_, err := ParseXPub("not_an_xpub")
	if err != ErrInvalidXPub {
		t.Errorf("want ErrInvalidXPub, got %v", err)
	}
}

func TestParseXPubEmpty(t *testing.T) {
	_, err := ParseXPub("")
	if err != ErrInvalidXPub {
		t.Errorf("want ErrInvalidXPub for empty string, got %v", err)
	}
}

func TestPublicChild_HardenedFails(t *testing.T) {
	// Criar uma chave falsa mas com formato válido para testar a validação
	// O índice hardenado 0x80000000 não pode ser derivado de xpub
	ek := &ExtendedKey{isPrivate: false}
	_, err := ek.PublicChild(0x80000000)
	if err != ErrHardenedFromPub {
		t.Errorf("want ErrHardenedFromPub, got %v", err)
	}
}

// ─── Testes de coin selection ─────────────────────────────────────────────────

func TestSelectUTXOs_ExactFit(t *testing.T) {
	utxos := []UTXO{
		{ID: "u1", ValueSats: 100_000, Status: UTXOStatusConfirmed},
		{ID: "u2", ValueSats: 50_000, Status: UTXOStatusConfirmed},
	}
	selected, change, fee, err := SelectUTXOs(utxos, 80_000, 5, 546)
	if err != nil {
		t.Fatalf("SelectUTXOs: %v", err)
	}
	if len(selected) == 0 {
		t.Fatal("nenhum UTXO selecionado")
	}
	total := int64(0)
	for _, u := range selected {
		total += u.ValueSats
	}
	if total < 80_000+fee {
		t.Errorf("total insuficiente: %d < %d + %d", total, 80_000, fee)
	}
	if change < 0 {
		t.Errorf("troco negativo: %d", change)
	}
}

func TestSelectUTXOs_Insufficient(t *testing.T) {
	utxos := []UTXO{
		{ID: "u1", ValueSats: 1_000, Status: UTXOStatusConfirmed},
	}
	_, _, _, err := SelectUTXOs(utxos, 100_000, 5, 546)
	if err == nil {
		t.Fatal("esperava ErrInsufficientFunds")
	}
}

func TestSelectUTXOs_Empty(t *testing.T) {
	_, _, _, err := SelectUTXOs(nil, 1_000, 5, 546)
	if err != ErrNoUTXOs {
		t.Errorf("want ErrNoUTXOs, got %v", err)
	}
}

func TestSelectUTXOs_DustChange(t *testing.T) {
	// Caso onde o troco seria menor que dust — deve ser absorvido na fee
	utxos := []UTXO{
		{ID: "u1", ValueSats: 10_000, Status: UTXOStatusConfirmed},
	}
	// Pedir quase todo o saldo para forçar troco < dust
	selected, change, _, err := SelectUTXOs(utxos, 9_400, 5, 546)
	if err != nil {
		t.Fatalf("SelectUTXOs: %v", err)
	}
	if len(selected) == 0 {
		t.Fatal("nenhum UTXO selecionado")
	}
	// Troco deve ser 0 ou >= dust (546)
	if change != 0 && change < 546 {
		t.Errorf("troco %d abaixo de dust sem ser 0", change)
	}
}

// ─── Testes de vsize ─────────────────────────────────────────────────────────

func TestEstimateVSize(t *testing.T) {
	// 1 input, 2 outputs P2WPKH — esperado ~141 vbytes
	v := EstimateVSize(1, 2)
	if v < 100 || v > 200 {
		t.Errorf("EstimateVSize(1,2) = %d, fora do range esperado [100,200]", v)
	}
}

func TestEstimateVSize_Monotone(t *testing.T) {
	v1 := EstimateVSize(1, 2)
	v2 := EstimateVSize(2, 2)
	if v2 <= v1 {
		t.Errorf("vsize com mais inputs deve ser maior: %d <= %d", v2, v1)
	}
}

// ─── Testes de utilitários ────────────────────────────────────────────────────

func TestSatsToBTCString(t *testing.T) {
	tests := []struct {
		sats int64
		want string
	}{
		{0, "0.00000000"},
		{1, "0.00000001"},
		{100_000_000, "1.00000000"},
		{150_000_000, "1.50000000"},
		{21_000_000_00_000_000, "21000000.00000000"},
	}
	for _, tt := range tests {
		got := satsToBTCString(tt.sats)
		if got != tt.want {
			t.Errorf("satsToBTCString(%d) = %q, want %q", tt.sats, got, tt.want)
		}
	}
}

func TestReverseTxid(t *testing.T) {
	txid := "4a5e1e4baab89f3a32518a88c31bc87f618f76673e2cc77ab2127b7afdeda33b"
	rev, err := reverseTxid(txid)
	if err != nil {
		t.Fatalf("reverseTxid: %v", err)
	}
	if len(rev) != 32 {
		t.Errorf("want 32 bytes, got %d", len(rev))
	}
	// Primeiro byte do reversed deve ser o último byte do txid hex
	if rev[0] != 0x3b {
		t.Errorf("primeiro byte invertido: got 0x%02x, want 0x3b", rev[0])
	}
}

func TestVarint(t *testing.T) {
	tests := []struct {
		n    uint64
		want []byte
	}{
		{0, []byte{0x00}},
		{252, []byte{0xfc}},
		{253, []byte{0xfd, 0xfd, 0x00}},
		{65535, []byte{0xfd, 0xff, 0xff}},
		{65536, []byte{0xfe, 0x00, 0x00, 0x01, 0x00}},
	}
	for _, tt := range tests {
		got := varint(tt.n)
		if string(got) != string(tt.want) {
			t.Errorf("varint(%d) = %v, want %v", tt.n, got, tt.want)
		}
	}
}

func TestDerEncodeSignature(t *testing.T) {
	r := new(big.Int).SetBytes(mustHexBytes(t,
		"2222222222222222222222222222222222222222222222222222222222222222"))
	s := new(big.Int).SetBytes(mustHexBytes(t,
		"3333333333333333333333333333333333333333333333333333333333333333"))
	der := derEncodeSignature(r, s)

	if len(der) < 6 {
		t.Fatalf("DER muito curto: %d bytes", len(der))
	}
	if der[0] != 0x30 {
		t.Errorf("DER deve começar com 0x30, got 0x%02x", der[0])
	}
	totalLen := int(der[1])
	if totalLen != len(der)-2 {
		t.Errorf("DER length: header diz %d, len-2=%d", totalLen, len(der)-2)
	}
	// Verificar estrutura: 0x02 <rlen> <r> 0x02 <slen> <s>
	if der[2] != 0x02 {
		t.Errorf("DER: esperava 0x02 em der[2], got 0x%02x", der[2])
	}
}

// ─── Testes adversariais de configuração ──────────────────────────────────────

func TestLoadBTCConfig_Disabled(t *testing.T) {
	t.Setenv("BTC_ENABLED", "false")
	cfg, err := LoadBTCConfig()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cfg != nil {
		t.Error("config deve ser nil quando BTC_ENABLED=false")
	}
}

func TestLoadBTCConfig_InvalidNetwork(t *testing.T) {
	t.Setenv("BTC_ENABLED", "true")
	t.Setenv("BTC_NETWORK", "ethereum")
	t.Setenv("BTC_XPUB", "xpubFake")
	_, err := LoadBTCConfig()
	if err == nil {
		t.Error("esperava erro para rede inválida")
	}
}

func TestLoadBTCConfig_MissingXPub(t *testing.T) {
	t.Setenv("BTC_ENABLED", "true")
	t.Setenv("BTC_NETWORK", "testnet")
	t.Setenv("BTC_XPUB", "")
	_, err := LoadBTCConfig()
	if err == nil {
		t.Error("esperava erro quando BTC_XPUB vazio")
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func mustHexBytes(t *testing.T, h string) []byte {
	t.Helper()
	b := make([]byte, len(h)/2)
	for i := range b {
		hi := hexNibble(h[i*2])
		lo := hexNibble(h[i*2+1])
		if hi < 0 || lo < 0 {
			t.Fatalf("hex inválido: %q", h)
		}
		b[i] = byte(hi<<4) | byte(lo)
	}
	return b
}
