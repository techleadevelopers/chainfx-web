package main

import (
	"encoding/json"
	"log"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

func main() {
	privateKey := os.Getenv("EVM_PRIVATE_KEY")
	if len(os.Args) > 1 {
		privateKey = os.Args[1]
	}
	privateKey = strings.TrimPrefix(strings.TrimSpace(privateKey), "0x")
	if privateKey == "" {
		log.Fatal("provide private key as first argument or EVM_PRIVATE_KEY")
	}
	key, err := crypto.HexToECDSA(privateKey)
	if err != nil {
		log.Fatalf("invalid private key: %v", err)
	}
	out := map[string]string{
		"address": crypto.PubkeyToAddress(key.PublicKey).Hex(),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
