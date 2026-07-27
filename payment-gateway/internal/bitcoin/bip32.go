package bitcoin

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"math/big"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/ripemd160"
)

// Versões de xpub/zpub conhecidas → rede
var xpubVersions = map[uint32]Network{
	0x0488B21E: Mainnet, // xpub
	0x04B24746: Mainnet, // zpub (BIP84)
	0x043587CF: Testnet, // tpub
	0x045F1CF6: Testnet, // vpub (BIP84)
}

// ExtendedKey representa uma chave BIP32 pública ou privada.
type ExtendedKey struct {
	version     [4]byte
	depth       byte
	fingerprint [4]byte
	childNum    uint32
	chainCode   [32]byte
	key         [33]byte // pubkey comprimida (33 bytes) ou 0x00+privkey (33 bytes)
	isPrivate   bool
	network     Network
}

// ParseXPub decodifica um xpub/zpub/tpub/vpub base58check e retorna um ExtendedKey público.
func ParseXPub(xpub string) (*ExtendedKey, error) {
	payload, err := base58CheckDecode(xpub)
	if err != nil {
		return nil, ErrInvalidXPub
	}
	if len(payload) != 78 {
		return nil, ErrInvalidXPub
	}

	ver := binary.BigEndian.Uint32(payload[:4])
	net, ok := xpubVersions[ver]
	if !ok {
		return nil, ErrInvalidXPub
	}

	ek := &ExtendedKey{network: net}
	copy(ek.version[:], payload[:4])
	ek.depth = payload[4]
	copy(ek.fingerprint[:], payload[5:9])
	ek.childNum = binary.BigEndian.Uint32(payload[9:13])
	copy(ek.chainCode[:], payload[13:45])
	copy(ek.key[:], payload[45:78])
	// xpub tem o primeiro byte da chave como 0x02 ou 0x03 (pubkey)
	// xpriv tem 0x00 seguido de 32 bytes da privkey
	ek.isPrivate = payload[45] == 0x00

	return ek, nil
}

// NewMasterKey deriva a chave mestra BIP32 a partir de um seed raw (bytes).
// Usa HMAC-SHA512 com chave literal "Bitcoin seed" conforme BIP32.
func NewMasterKey(seed []byte) (*ExtendedKey, error) {
	mac := hmac.New(sha512.New, []byte("Bitcoin seed"))
	mac.Write(seed)
	I := mac.Sum(nil)

	IL := I[:32]
	IR := I[32:]

	curve := ethcrypto.S256()
	n := curve.Params().N
	d := new(big.Int).SetBytes(IL)
	if d.Sign() == 0 || d.Cmp(n) >= 0 {
		return nil, errors.New("bip32: chave mestra inválida — seed inadequado")
	}

	ek := &ExtendedKey{
		depth:     0,
		childNum:  0,
		isPrivate: true,
		network:   Mainnet,
	}
	copy(ek.version[:], []byte{0x04, 0x88, 0xAD, 0xE4}) // xpriv mainnet
	copy(ek.chainCode[:], IR)
	ek.key[0] = 0x00
	copy(ek.key[1:], padTo32(IL))
	return ek, nil
}

// PublicChild deriva o filho público não-hardenado no índice i.
// BIP32: HMAC-SHA512(key=chainCode, data=pubKey||index_be4)
func (ek *ExtendedKey) PublicChild(i uint32) (*ExtendedKey, error) {
	if i >= 0x80000000 {
		return nil, ErrHardenedFromPub
	}

	curve := ethcrypto.S256()
	n := curve.Params().N

	// Obter pubkey comprimida do pai
	var parentPub [33]byte
	if ek.isPrivate {
		x, y := curve.ScalarBaseMult(ek.key[1:])
		p := compressedPubKey(x, y)
		copy(parentPub[:], p)
	} else {
		parentPub = ek.key
	}

	// HMAC-SHA512
	indexBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(indexBytes, i)

	mac := hmac.New(sha512.New, ek.chainCode[:])
	mac.Write(parentPub[:])
	mac.Write(indexBytes)
	I := mac.Sum(nil)
	IL := I[:32]
	IR := I[32:]

	// Verificar IL < n
	ilInt := new(big.Int).SetBytes(IL)
	if ilInt.Cmp(n) >= 0 {
		return nil, errors.New("bip32: IL >= n, índice inválido")
	}

	// Child pubkey = IL*G + parentPubKey (point addition)
	childX, childY := curve.ScalarBaseMult(IL)
	px, py, err := decompressPublicKey(parentPub[:])
	if err != nil {
		return nil, err
	}
	cx, cy := curve.Add(childX, childY, px, py)
	childPub := compressedPubKey(cx, cy)

	// Fingerprint do pai = HASH160(parentPub)[:4]
	fp := btcHash160(parentPub[:])[0:4]

	child := &ExtendedKey{
		depth:     ek.depth + 1,
		childNum:  i,
		isPrivate: false,
		network:   ek.network,
	}
	copy(child.version[:], ek.version[:])
	copy(child.chainCode[:], IR)
	copy(child.key[:], childPub)
	copy(child.fingerprint[:], fp)
	return child, nil
}

// PrivateChild deriva o filho privado no índice i (hardened ou normal).
// Requer chave privada.
func (ek *ExtendedKey) PrivateChild(i uint32) (*ExtendedKey, error) {
	if !ek.isPrivate {
		return nil, errors.New("bip32: PrivateChild requer chave privada")
	}

	curve := ethcrypto.S256()
	n := curve.Params().N
	parentPriv := ek.key[1:] // 32 bytes

	var data []byte
	if i >= 0x80000000 {
		// Hardenado: 0x00 || privkey || index
		data = append([]byte{0x00}, parentPriv...)
	} else {
		// Normal: pubkey comprimida || index
		x, y := curve.ScalarBaseMult(parentPriv)
		pub := compressedPubKey(x, y)
		data = pub
	}
	indexBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(indexBytes, i)
	data = append(data, indexBytes...)

	mac := hmac.New(sha512.New, ek.chainCode[:])
	mac.Write(data)
	I := mac.Sum(nil)
	IL := I[:32]
	IR := I[32:]

	ilInt := new(big.Int).SetBytes(IL)
	if ilInt.Cmp(n) >= 0 {
		return nil, errors.New("bip32: IL >= n, índice inválido")
	}

	parentInt := new(big.Int).SetBytes(parentPriv)
	childInt := new(big.Int).Add(ilInt, parentInt)
	childInt.Mod(childInt, n)
	if childInt.Sign() == 0 {
		return nil, errors.New("bip32: chave filho inválida")
	}

	// Fingerprint
	x, y := curve.ScalarBaseMult(parentPriv)
	fp := btcHash160(compressedPubKey(x, y))[0:4]

	child := &ExtendedKey{
		depth:     ek.depth + 1,
		childNum:  i,
		isPrivate: true,
		network:   ek.network,
	}
	copy(child.version[:], ek.version[:])
	copy(child.chainCode[:], IR)
	child.key[0] = 0x00
	copy(child.key[1:], padTo32(childInt.Bytes()))
	copy(child.fingerprint[:], fp)
	return child, nil
}

// RawPrivKey retorna os 32 bytes da chave privada.
func (ek *ExtendedKey) RawPrivKey() ([]byte, error) {
	if !ek.isPrivate {
		return nil, errors.New("bip32: não é uma chave privada")
	}
	out := make([]byte, 32)
	copy(out, ek.key[1:])
	return out, nil
}

// CompressedPubKey retorna a pubkey comprimida (33 bytes).
func (ek *ExtendedKey) CompressedPubKey() []byte {
	if !ek.isPrivate {
		out := make([]byte, 33)
		copy(out, ek.key[:])
		return out
	}
	curve := ethcrypto.S256()
	x, y := curve.ScalarBaseMult(ek.key[1:])
	return compressedPubKey(x, y)
}

// Network retorna a rede do key.
func (ek *ExtendedKey) Network() Network { return ek.network }

// ─── helpers ─────────────────────────────────────────────────────────────────

func compressedPubKey(x, y *big.Int) []byte {
	prefix := byte(0x02)
	if y.Bit(0) != 0 {
		prefix = 0x03
	}
	out := make([]byte, 33)
	out[0] = prefix
	xb := x.Bytes()
	copy(out[1+32-len(xb):], xb)
	return out
}

// decompressPublicKey retorna as coordenadas (x, y) de uma pubkey comprimida de 33 bytes.
func decompressPublicKey(pub []byte) (x, y *big.Int, err error) {
	if len(pub) != 33 {
		return nil, nil, errors.New("bip32: pubkey deve ter 33 bytes")
	}
	if pub[0] != 0x02 && pub[0] != 0x03 {
		return nil, nil, errors.New("bip32: prefixo de pubkey inválido")
	}
	curve := ethcrypto.S256()
	p := curve.Params().P

	x = new(big.Int).SetBytes(pub[1:])
	// y^2 = x^3 + 7 (mod p)
	x3 := new(big.Int).Mul(x, x)
	x3.Mul(x3, x)
	x3.Add(x3, big.NewInt(7))
	x3.Mod(x3, p)

	// y = sqrt(x^3+7) mod p; para p ≡ 3 (mod 4): exp = (p+1)/4
	exp := new(big.Int).Add(p, big.NewInt(1))
	exp.Rsh(exp, 2)
	y = new(big.Int).Exp(x3, exp, p)

	// Ajustar paridade
	if (y.Bit(0) == 1) != (pub[0] == 0x03) {
		y.Sub(p, y)
	}
	return x, y, nil
}

// btcHash160 computa RIPEMD160(SHA256(data)).
func btcHash160(data []byte) []byte {
	h := sha256.Sum256(data)
	r := ripemd160.New()
	r.Write(h[:])
	return r.Sum(nil)
}

func padTo32(b []byte) []byte {
	if len(b) >= 32 {
		return b[len(b)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}
