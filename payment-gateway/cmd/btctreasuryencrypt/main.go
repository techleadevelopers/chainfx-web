package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"payment-gateway/internal/bitcoin"
)

func main() {
	networkFlag := flag.String("network", "mainnet", "Bitcoin network: mainnet, testnet, signet, regtest")
	format := flag.String("format", "mnemonic", "treasury key format: mnemonic, xpriv, raw")
	address := flag.String("address", "", "optional BTC_TREASURY_ADDRESS validation")
	path := flag.String("path", "", "optional BTC_TREASURY_DERIVATION_PATH")
	passphrase := flag.String("passphrase", "", "optional BIP39 mnemonic passphrase")
	flag.Parse()

	plaintext := readSecretFromStdin()
	if plaintext == "" {
		log.Fatal("stdin vazio: informe a mnemonic/xpriv/raw privkey pelo stdin")
	}

	network := bitcoin.Network(strings.ToLower(strings.TrimSpace(*networkFlag)))
	switch network {
	case bitcoin.Mainnet, bitcoin.Testnet, bitcoin.Signet, bitcoin.Regtest:
	default:
		log.Fatalf("invalid -network %q", *networkFlag)
	}

	encryptionKey := mustRandom(32)
	encrypted, err := encryptAESGCM(encryptionKey, []byte(plaintext))
	if err != nil {
		log.Fatal(err)
	}

	encryptedHex := hex.EncodeToString(encrypted)
	encryptionKeyHex := hex.EncodeToString(encryptionKey)

	if strings.TrimSpace(*address) != "" {
		cfg := &bitcoin.TreasuryConfig{
			Enabled:            true,
			Address:            strings.TrimSpace(*address),
			SignerKeyID:        "btc_treasury_main",
			EncryptedKey:       encryptedHex,
			EncryptionKey:      encryptionKeyHex,
			KeyFormat:          strings.TrimSpace(*format),
			DerivationPath:     strings.TrimSpace(*path),
			MnemonicPassphrase: *passphrase,
		}
		btcCfg := &bitcoin.Config{Enabled: true, Network: network}
		if _, err := bitcoin.NewAESGCMTreasurySigner(cfg, btcCfg); err != nil {
			log.Fatalf("validacao falhou: a chave informada nao corresponde ao address/path: %v", err)
		}
	}

	fmt.Println("# Generated locally. Store only in Railway/.env; never commit secrets.")
	fmt.Printf("BTC_TREASURY_KEY_FORMAT=%s\n", strings.TrimSpace(*format))
	if strings.TrimSpace(*address) != "" {
		fmt.Printf("BTC_TREASURY_ADDRESS=%s\n", strings.TrimSpace(*address))
	}
	fmt.Printf("BTC_TREASURY_ENCRYPTED_KEY=%s\n", encryptedHex)
	fmt.Printf("BTC_TREASURY_ENCRYPTION_KEY=%s\n", encryptionKeyHex)
	if strings.TrimSpace(*path) != "" {
		fmt.Printf("BTC_TREASURY_DERIVATION_PATH=%s\n", strings.TrimSpace(*path))
	}
	if *passphrase != "" {
		fmt.Println("# BTC_TREASURY_MNEMONIC_PASSPHRASE was used during validation; set it separately in env.")
	}
}

func readSecretFromStdin() string {
	data, err := io.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		log.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func mustRandom(n int) []byte {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		log.Fatal(err)
	}
	return buf
}

func encryptAESGCM(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := mustRandom(gcm.NonceSize())
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}
