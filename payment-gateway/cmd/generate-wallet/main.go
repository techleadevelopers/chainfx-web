package main

import (
	"encoding/hex"
	"encoding/json"
	"log"
	"os"

	"github.com/ethereum/go-ethereum/crypto"
)

func main() {
	key, err := crypto.GenerateKey()
	if err != nil {
		log.Fatalf("failed to generate wallet: %v", err)
	}
	out := map[string]string{
		"address":    crypto.PubkeyToAddress(key.PublicKey).Hex(),
		"privateKey": "0x" + hex.EncodeToString(crypto.FromECDSA(key)),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
