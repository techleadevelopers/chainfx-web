package bitcoin

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// TreasurySigner assina transações BTC para a Treasury operacional.
//
// Requisitos de segurança:
//   - Assinatura é exclusivamente server-side
//   - Segredo NUNCA retorna ao mobile, ao web ou a logs
//   - BTC_TREASURY_SIGNER_KEY_ID identifica a chave para auditoria (não é o segredo)
//   - A chave privada é SEPARADA de BTC_ENCRYPTED_SEED (wallets dos usuários)
type TreasurySigner interface {
	// Sign assina inputs com a chave da treasury e retorna rawHex + txid.
	// keyID é incluído apenas para consistência de logging — não expõe segredo.
	Sign(ctx context.Context, inputs []TxInput, outputs []TxOutput) (rawHex, txid string, err error)
	// KeyID retorna o identificador auditável da chave (não contém segredo).
	KeyID() string
}

// AESGCMTreasurySigner implementa TreasurySigner com chave AES-GCM armazenada localmente.
//
// Formato da chave: BTC_TREASURY_ENCRYPTED_KEY deve ser o hex AES-GCM de um
// raw private key de 32 bytes (secp256k1). Este formato é DIFERENTE de
// BTC_ENCRYPTED_SEED (que cifra um xpriv derivado do HD wallet).
//
// A separação lógica entre a chave da Treasury e a seed dos usuários é explícita
// e obrigatória — nunca reutilize BTC_ENCRYPTED_SEED para a Treasury.
type AESGCMTreasurySigner struct {
	keyID       string
	privKeyRaw  []byte // 32 bytes — nunca logado, nunca serializado para resposta
	pubKeyBytes []byte // 33 bytes compressed secp256k1
}

// NewAESGCMTreasurySigner cria o signer a partir da configuração da treasury.
// Retorna erro se a chave não puder ser decifrada ou validada.
func NewAESGCMTreasurySigner(cfg *TreasuryConfig) (*AESGCMTreasurySigner, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, fmt.Errorf("bitcoin/treasury: TreasuryConfig não habilitada")
	}

	// Decifrar usando a mesma lógica AES-GCM do bitcoin.Service (sem duplicar código)
	var keyBytes []byte
	if len(cfg.EncryptionKey) == 64 {
		b, err := hex.DecodeString(cfg.EncryptionKey)
		if err != nil {
			return nil, fmt.Errorf("bitcoin/treasury: BTC_TREASURY_ENCRYPTION_KEY hex inválido")
		}
		keyBytes = b
	} else {
		// Passphrase → SHA256 → chave AES-256
		keyBytes = sha256Sum([]byte(cfg.EncryptionKey))
	}

	cipherBytes, err := hex.DecodeString(cfg.EncryptedKey)
	if err != nil {
		return nil, fmt.Errorf("bitcoin/treasury: BTC_TREASURY_ENCRYPTED_KEY hex inválido")
	}

	plaintext, err := aesGCMDecrypt(keyBytes, cipherBytes)
	if err != nil {
		return nil, fmt.Errorf("bitcoin/treasury: falha ao decifrar chave da treasury: %w", err)
	}

	// Plaintext esperado: 32 bytes em hex (64 caracteres)
	privKeyRaw, err := parseTreasuryPrivKey(plaintext)
	if err != nil {
		return nil, fmt.Errorf("bitcoin/treasury: formato de chave privada inválido: %w", err)
	}

	pubKeyBytes, err := derivePubKeyFromPrivKey(privKeyRaw)
	if err != nil {
		return nil, fmt.Errorf("bitcoin/treasury: erro ao derivar pubkey da treasury: %w", err)
	}

	return &AESGCMTreasurySigner{
		keyID:       cfg.SignerKeyID,
		privKeyRaw:  privKeyRaw,
		pubKeyBytes: pubKeyBytes,
	}, nil
}

// KeyID retorna o identificador auditável da chave (não é o segredo).
func (s *AESGCMTreasurySigner) KeyID() string { return s.keyID }

// Sign constrói e assina uma transação P2WPKH usando a chave da Treasury.
// Todos os inputs recebem a mesma chave (carteira de endereço único).
func (s *AESGCMTreasurySigner) Sign(_ context.Context, inputs []TxInput, outputs []TxOutput) (string, string, error) {
	if len(inputs) == 0 {
		return "", "", fmt.Errorf("bitcoin/treasury: nenhum input para assinar")
	}
	// Injetar chave nos inputs — a chave nunca é logada
	enriched := make([]TxInput, len(inputs))
	copy(enriched, inputs)
	for i := range enriched {
		enriched[i].PrivKeyBytes = s.privKeyRaw
		enriched[i].PubKeyBytes = s.pubKeyBytes
	}
	return BuildAndSignTx(enriched, outputs)
}

// ─── helpers privados ─────────────────────────────────────────────────────────

// parseTreasuryPrivKey aceita:
//   - 32 bytes raw (formato binário)
//   - 64 chars hex (32 bytes)
//   - 66 chars hex com prefixo "01" de compressão WIF (ignora o byte extra)
func parseTreasuryPrivKey(plaintext []byte) ([]byte, error) {
	// Remover espaços/newlines
	clean := []byte{}
	for _, b := range plaintext {
		if b != ' ' && b != '\n' && b != '\r' && b != '\t' {
			clean = append(clean, b)
		}
	}

	switch len(clean) {
	case 32:
		// Raw bytes
		return clean, nil
	case 64:
		// Hex 32 bytes
		decoded, err := hex.DecodeString(string(clean))
		if err != nil {
			return nil, fmt.Errorf("hex de 64 chars inválido")
		}
		return decoded, nil
	case 66:
		// Hex 33 bytes (com compression flag 01 no final — ignorar)
		decoded, err := hex.DecodeString(string(clean))
		if err != nil {
			return nil, fmt.Errorf("hex de 66 chars inválido")
		}
		return decoded[:32], nil
	default:
		return nil, fmt.Errorf("formato inválido: esperado 32 bytes raw, 64 chars hex ou 66 chars hex com compression flag; got %d bytes", len(clean))
	}
}

// derivePubKeyFromPrivKey deriva a pubkey secp256k1 comprimida (33 bytes) de uma privkey raw.
func derivePubKeyFromPrivKey(privKeyRaw []byte) ([]byte, error) {
	if len(privKeyRaw) != 32 {
		return nil, fmt.Errorf("privkey deve ter 32 bytes")
	}
	curve := ethcrypto.S256()
	privInt := new(big.Int).SetBytes(privKeyRaw)
	if privInt.Sign() == 0 || privInt.Cmp(curve.Params().N) >= 0 {
		return nil, fmt.Errorf("privkey fora do intervalo válido secp256k1")
	}
	x, y := curve.ScalarBaseMult(privKeyRaw)
	// Pubkey comprimida: 0x02 ou 0x03 + X (33 bytes)
	pubKey := make([]byte, 33)
	pubKey[0] = byte(2 + y.Bit(0))
	xBytes := x.Bytes()
	copy(pubKey[1+32-len(xBytes):], xBytes) // zero-pad left
	return pubKey, nil
}
