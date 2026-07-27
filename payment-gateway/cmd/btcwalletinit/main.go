package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"strings"

	"payment-gateway/internal/bitcoin"
)

func main() {
	networkFlag := flag.String("network", "testnet", "Bitcoin network: mainnet, testnet, signet, regtest")
	withWithdrawals := flag.Bool("enable-withdrawals", false, "print withdrawals enabled and emergency lockdown disabled")
	flag.Parse()

	network := bitcoin.Network(strings.ToLower(strings.TrimSpace(*networkFlag)))
	switch network {
	case bitcoin.Mainnet, bitcoin.Testnet, bitcoin.Signet, bitcoin.Regtest:
	default:
		log.Fatalf("invalid -network %q", *networkFlag)
	}

	seed := mustRandom(32)
	encryptionKey := mustRandom(32)

	master, err := bitcoin.NewMasterKeyForNetwork(seed, network)
	if err != nil {
		log.Fatal(err)
	}
	accountXPriv, err := bitcoin.DeriveAccountXPriv(master, network)
	if err != nil {
		log.Fatal(err)
	}
	accountXPub := accountXPriv.Neuter()

	encryptedXPriv, err := encryptAESGCM(encryptionKey, []byte(accountXPriv.String()))
	if err != nil {
		log.Fatal(err)
	}

	withdrawalsEnabled := "false"
	emergencyLockdown := "true"
	if *withWithdrawals {
		withdrawalsEnabled = "true"
		emergencyLockdown = "false"
	}

	fmt.Println("# Generated locally. Store these only in Railway/.env, never in git.")
	fmt.Println("BTC_ENABLED=true")
	fmt.Printf("BTC_NETWORK=%s\n", network)
	fmt.Println("BTC_PROVIDER_TYPE=mempool")
	fmt.Println("BTC_API_TOKEN=")
	fmt.Printf("BTC_XPUB=%s\n", accountXPub.String())
	fmt.Printf("BTC_ENCRYPTED_SEED=%s\n", hex.EncodeToString(encryptedXPriv))
	fmt.Printf("BTC_ENCRYPTION_KEY=%s\n", hex.EncodeToString(encryptionKey))
	fmt.Printf("BTC_WITHDRAWALS_ENABLED=%s\n", withdrawalsEnabled)
	fmt.Printf("BTC_EMERGENCY_LOCKDOWN=%s\n", emergencyLockdown)
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
