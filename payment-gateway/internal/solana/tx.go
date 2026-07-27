package solana

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
)

const systemProgramID = "11111111111111111111111111111111"

func BuildSOLTransfer(privateKey ed25519.PrivateKey, toAddress, recentBlockhash string, lamports int64) ([]byte, []byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, nil, fmt.Errorf("solana: private key invalida")
	}
	if lamports <= 0 {
		return nil, nil, fmt.Errorf("solana: lamports deve ser > 0")
	}
	from := privateKey.Public().(ed25519.PublicKey)
	to, err := base58Decode(toAddress)
	if err != nil || len(to) != 32 {
		return nil, nil, fmt.Errorf("solana: destino invalido")
	}
	blockhash, err := base58Decode(recentBlockhash)
	if err != nil || len(blockhash) != 32 {
		return nil, nil, fmt.Errorf("solana: blockhash invalido")
	}
	system, _ := base58Decode(systemProgramID)
	message := buildTransferMessage(from, to, system, blockhash, lamports)
	signature := ed25519.Sign(privateKey, message)
	tx := append(append([]byte{1}, signature...), message...)
	return tx, message, nil
}

func BuildUnsignedSOLTransferMessage(fromAddress, toAddress, recentBlockhash string, lamports int64) ([]byte, error) {
	from, err := base58Decode(fromAddress)
	if err != nil || len(from) != 32 {
		return nil, fmt.Errorf("solana: origem invalida")
	}
	to, err := base58Decode(toAddress)
	if err != nil || len(to) != 32 {
		return nil, fmt.Errorf("solana: destino invalido")
	}
	blockhash, err := base58Decode(recentBlockhash)
	if err != nil || len(blockhash) != 32 {
		return nil, fmt.Errorf("solana: blockhash invalido")
	}
	system, _ := base58Decode(systemProgramID)
	return buildTransferMessage(from, to, system, blockhash, lamports), nil
}

func buildTransferMessage(from, to, system, blockhash []byte, lamports int64) []byte {
	var msg []byte
	msg = append(msg, 1, 0, 1)
	msg = append(msg, compactU16(3)...)
	msg = append(msg, from...)
	msg = append(msg, to...)
	msg = append(msg, system...)
	msg = append(msg, blockhash...)
	msg = append(msg, compactU16(1)...)
	msg = append(msg, 2)
	msg = append(msg, compactU16(2)...)
	msg = append(msg, 0, 1)
	data := make([]byte, 12)
	binary.LittleEndian.PutUint32(data[:4], 2)
	binary.LittleEndian.PutUint64(data[4:], uint64(lamports))
	msg = append(msg, compactU16(len(data))...)
	msg = append(msg, data...)
	return msg
}

func compactU16(n int) []byte {
	var out []byte
	for {
		elem := byte(n & 0x7f)
		n >>= 7
		if n == 0 {
			out = append(out, elem)
			return out
		}
		out = append(out, elem|0x80)
	}
}
