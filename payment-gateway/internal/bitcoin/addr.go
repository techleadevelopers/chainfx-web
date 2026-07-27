package bitcoin

import "fmt"

// DeriveReceiveAddress deriva o endereço P2WPKH de recebimento para o índice dado.
// Usa o xpub da configuração e o caminho de derivação externo (0/index).
//
// Caminho derivado: account_xpub / 0 (external chain) / index
// Onde account_xpub é o xpub fornecido em BTC_XPUB (nível m/84'/network'/0').
func DeriveReceiveAddress(cfg *Config, index uint32) (address string, pubKey []byte, derivPath string, err error) {
	accountKey, err := ParseXPub(cfg.XPub)
	if err != nil {
		return "", nil, "", fmt.Errorf("addr: falha ao parsear xpub: %w", err)
	}

	// Derivar external chain key: account / 0
	externalChain, err := accountKey.PublicChild(0)
	if err != nil {
		return "", nil, "", fmt.Errorf("addr: falha ao derivar external chain: %w", err)
	}

	// Derivar chave no índice: external / index
	childKey, err := externalChain.PublicChild(index)
	if err != nil {
		return "", nil, "", fmt.Errorf("addr: falha ao derivar chave no índice %d: %w", index, err)
	}

	pubKey = childKey.CompressedPubKey()

	address, err = P2WPKHAddress(pubKey, cfg.HRP())
	if err != nil {
		return "", nil, "", fmt.Errorf("addr: falha ao gerar endereço bech32: %w", err)
	}

	derivPath = fmt.Sprintf("m/84'/0'/0'/0/%d", index)
	if cfg.Network == Testnet || cfg.Network == Signet || cfg.Network == Regtest {
		derivPath = fmt.Sprintf("m/84'/1'/0'/0/%d", index)
	}

	return address, pubKey, derivPath, nil
}

// DerivePrivKeyAtIndex deriva a chave privada para assinar o índice dado.
// Requer um ExtendedKey privado (xpriv) decifrado.
//
// Caminho: account_xpriv / 0 / index
func DerivePrivKeyAtIndex(accountXpriv *ExtendedKey, index uint32) (privKeyBytes []byte, pubKey []byte, err error) {
	externalChain, err := accountXpriv.PrivateChild(0)
	if err != nil {
		return nil, nil, fmt.Errorf("addr: falha ao derivar external chain privada: %w", err)
	}

	childKey, err := externalChain.PrivateChild(index)
	if err != nil {
		return nil, nil, fmt.Errorf("addr: falha ao derivar chave privada no índice %d: %w", index, err)
	}

	privKeyBytes, err = childKey.RawPrivKey()
	if err != nil {
		return nil, nil, err
	}

	pubKey = childKey.CompressedPubKey()
	return privKeyBytes, pubKey, nil
}
