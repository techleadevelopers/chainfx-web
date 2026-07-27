package bitcoin

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// ─── Entrada de transação para assinatura ─────────────────────────────────────

// TxInput representa um input P2WPKH já selecionado para gastar.
type TxInput struct {
	Txid         string // txid hex (não-invertido)
	Vout         uint32
	ValueSats    int64  // valor do UTXO sendo gasto (necessário para BIP143)
	ScriptPubKey string // hex do scriptPubKey (OP_0 PUSH20 <hash160>)
	PrivKeyBytes []byte // 32 bytes da chave privada para assinar este input
	PubKeyBytes  []byte // 33 bytes da pubkey comprimida
}

// TxOutput representa um output da transação.
type TxOutput struct {
	ValueSats    int64
	ScriptPubKey []byte // já serializado (ex: 0x0014<hash160>)
}

// BuildAndSignTx constrói e assina uma transação P2WPKH SegWit.
// Retorna o hex da transação assinada e o txid computado.
func BuildAndSignTx(inputs []TxInput, outputs []TxOutput) (rawHex string, txid string, err error) {
	if len(inputs) == 0 {
		return "", "", errors.New("tx: nenhum input")
	}
	if len(outputs) == 0 {
		return "", "", errors.New("tx: nenhum output")
	}

	curve := ethcrypto.S256()
	const nVersion = uint32(2)
	const nSequence = uint32(0xffffffff)
	const nLockTime = uint32(0)
	const sigHashAll = uint32(1)

	// ── Pré-computar hashes BIP143 ───────────────────────────────────────────

	// hashPrevouts = dSHA256(todos os outpoints)
	var prevoutsBuf bytes.Buffer
	for _, in := range inputs {
		txidBytes, e := reverseTxid(in.Txid)
		if e != nil {
			return "", "", fmt.Errorf("tx: txid inválido %q: %w", in.Txid, e)
		}
		prevoutsBuf.Write(txidBytes)
		prevoutsBuf.Write(uint32LE(in.Vout))
	}
	hashPrevouts := dSHA256(prevoutsBuf.Bytes())

	// hashSequence = dSHA256(todos os nSequence)
	var seqBuf bytes.Buffer
	for range inputs {
		seqBuf.Write(uint32LE(nSequence))
	}
	hashSequence := dSHA256(seqBuf.Bytes())

	// hashOutputs = dSHA256(todos os outputs)
	var outsBuf bytes.Buffer
	for _, out := range outputs {
		outsBuf.Write(uint64LE(uint64(out.ValueSats)))
		outsBuf.Write(varint(uint64(len(out.ScriptPubKey))))
		outsBuf.Write(out.ScriptPubKey)
	}
	hashOutputs := dSHA256(outsBuf.Bytes())

	// ── Assinar cada input ────────────────────────────────────────────────────

	witnesses := make([][][]byte, len(inputs))

	for i, in := range inputs {
		// scriptCode para P2WPKH: OP_DUP OP_HASH160 OP_PUSHBYTES_20 <hash160> OP_EQUALVERIFY OP_CHECKSIG
		// = 76 a9 14 <hash160 20 bytes> 88 ac  (sem o prefixo de tamanho varint)
		scriptPubKeyBytes, e := hex.DecodeString(in.ScriptPubKey)
		if e != nil {
			return "", "", fmt.Errorf("tx: scriptPubKey hex inválido: %w", e)
		}
		if len(scriptPubKeyBytes) != 22 || scriptPubKeyBytes[0] != 0x00 || scriptPubKeyBytes[1] != 0x14 {
			return "", "", fmt.Errorf("tx: scriptPubKey não é P2WPKH")
		}
		hash160 := scriptPubKeyBytes[2:] // 20 bytes

		// scriptCode = 0x1976a914<hash160>88ac
		// O prefixo 0x19 é o varint do comprimento (25 bytes)
		scriptCode := make([]byte, 26)
		scriptCode[0] = 0x19 // varint: 25 bytes abaixo
		scriptCode[1] = 0x76 // OP_DUP
		scriptCode[2] = 0xa9 // OP_HASH160
		scriptCode[3] = 0x14 // OP_PUSHBYTES_20
		copy(scriptCode[4:24], hash160)
		scriptCode[24] = 0x88 // OP_EQUALVERIFY
		scriptCode[25] = 0xac // OP_CHECKSIG

		txidBytes, _ := reverseTxid(in.Txid)

		// Serialização BIP143 do sighash
		var sighashBuf bytes.Buffer
		sighashBuf.Write(uint32LE(nVersion))
		sighashBuf.Write(hashPrevouts)
		sighashBuf.Write(hashSequence)
		sighashBuf.Write(txidBytes)
		sighashBuf.Write(uint32LE(in.Vout))
		sighashBuf.Write(scriptCode)
		sighashBuf.Write(uint64LE(uint64(in.ValueSats)))
		sighashBuf.Write(uint32LE(nSequence))
		sighashBuf.Write(hashOutputs)
		sighashBuf.Write(uint32LE(nLockTime))
		sighashBuf.Write(uint32LE(sigHashAll))

		sigHash := dSHA256(sighashBuf.Bytes())

		// Criar chave privada ECDSA
		d := new(big.Int).SetBytes(in.PrivKeyBytes)
		pubX, pubY, e := decompressPublicKey(in.PubKeyBytes)
		if e != nil {
			return "", "", fmt.Errorf("tx: pubkey inválida: %w", e)
		}
		privKey := &ecdsa.PrivateKey{
			PublicKey: ecdsa.PublicKey{
				Curve: curve,
				X:     pubX,
				Y:     pubY,
			},
			D: d,
		}

		// Assinar
		r, s, e := ecdsa.Sign(rand.Reader, privKey, sigHash)
		if e != nil {
			return "", "", fmt.Errorf("tx: erro ao assinar input %d: %w", i, e)
		}

		// Low-S normalization (regra de consenso Bitcoin)
		halfN := new(big.Int).Rsh(curve.Params().N, 1)
		if s.Cmp(halfN) > 0 {
			s.Sub(curve.Params().N, s)
		}

		// DER encode + sighash type byte
		derSig := derEncodeSignature(r, s)
		derSig = append(derSig, byte(sigHashAll)) // SIGHASH_ALL = 0x01

		witnesses[i] = [][]byte{derSig, in.PubKeyBytes}
	}

	// ── Serializar transação SegWit ───────────────────────────────────────────

	var txBuf bytes.Buffer
	txBuf.Write(uint32LE(nVersion))
	txBuf.WriteByte(0x00) // marker
	txBuf.WriteByte(0x01) // flag

	// Inputs
	txBuf.Write(varint(uint64(len(inputs))))
	for _, in := range inputs {
		txidBytes, _ := reverseTxid(in.Txid)
		txBuf.Write(txidBytes)
		txBuf.Write(uint32LE(in.Vout))
		txBuf.WriteByte(0x00) // empty scriptSig (segwit)
		txBuf.Write(uint32LE(nSequence))
	}

	// Outputs
	txBuf.Write(varint(uint64(len(outputs))))
	for _, out := range outputs {
		txBuf.Write(uint64LE(uint64(out.ValueSats)))
		txBuf.Write(varint(uint64(len(out.ScriptPubKey))))
		txBuf.Write(out.ScriptPubKey)
	}

	// Witness data (uma por input)
	for _, wit := range witnesses {
		txBuf.Write(varint(uint64(len(wit))))
		for _, item := range wit {
			txBuf.Write(varint(uint64(len(item))))
			txBuf.Write(item)
		}
	}

	txBuf.Write(uint32LE(nLockTime))

	rawBytes := txBuf.Bytes()
	rawHex = hex.EncodeToString(rawBytes)

	// txid = dSHA256 da serialização NÃO-witness (BIP141)
	txid = computeTxid(inputs, outputs, nVersion, nSequence, nLockTime)

	return rawHex, txid, nil
}

// computeTxid calcula o txid (hash da serialização sem witness).
func computeTxid(inputs []TxInput, outputs []TxOutput, nVersion, nSequence, nLockTime uint32) string {
	var buf bytes.Buffer
	buf.Write(uint32LE(nVersion))

	buf.Write(varint(uint64(len(inputs))))
	for _, in := range inputs {
		txidBytes, _ := reverseTxid(in.Txid)
		buf.Write(txidBytes)
		buf.Write(uint32LE(in.Vout))
		buf.WriteByte(0x00) // empty scriptSig
		buf.Write(uint32LE(nSequence))
	}

	buf.Write(varint(uint64(len(outputs))))
	for _, out := range outputs {
		buf.Write(uint64LE(uint64(out.ValueSats)))
		buf.Write(varint(uint64(len(out.ScriptPubKey))))
		buf.Write(out.ScriptPubKey)
	}

	buf.Write(uint32LE(nLockTime))

	hash := dSHA256(buf.Bytes())
	// txid é o hash invertido em hex
	reversed := make([]byte, 32)
	for i := 0; i < 32; i++ {
		reversed[i] = hash[31-i]
	}
	return hex.EncodeToString(reversed)
}

// derEncodeSignature codifica (r, s) no formato DER ASN.1.
func derEncodeSignature(r, s *big.Int) []byte {
	rb := r.Bytes()
	sb := s.Bytes()
	if rb[0]&0x80 != 0 {
		rb = append([]byte{0x00}, rb...)
	}
	if sb[0]&0x80 != 0 {
		sb = append([]byte{0x00}, sb...)
	}

	inner := make([]byte, 0, 4+len(rb)+len(sb))
	inner = append(inner, 0x02, byte(len(rb)))
	inner = append(inner, rb...)
	inner = append(inner, 0x02, byte(len(sb)))
	inner = append(inner, sb...)

	out := make([]byte, 0, 2+len(inner))
	out = append(out, 0x30, byte(len(inner)))
	out = append(out, inner...)
	return out
}

// dSHA256 computa SHA256(SHA256(data)).
func dSHA256(data []byte) []byte {
	h1 := sha256.Sum256(data)
	h2 := sha256.Sum256(h1[:])
	return h2[:]
}

// ─── Script helpers ───────────────────────────────────────────────────────────

// ScriptFromAddress converte um endereço bech32 P2WPKH para scriptPubKey bytes.
func ScriptFromAddress(addr, hrp string) ([]byte, error) {
	h160, err := Hash160FromAddress(addr, hrp)
	if err != nil {
		return nil, err
	}
	script := make([]byte, 22)
	script[0] = 0x00 // OP_0
	script[1] = 0x14 // PUSH 20 bytes
	copy(script[2:], h160)
	return script, nil
}

// ScriptHexFromPubKey retorna o scriptPubKey hex de uma pubkey comprimida.
func ScriptHexFromPubKey(pubKey []byte) (string, error) {
	script, err := P2WPKHScript(pubKey)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(script), nil
}

// le32 lê um uint32 little-endian (alias para evitar import circular).
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
