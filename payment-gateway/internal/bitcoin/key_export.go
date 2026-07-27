package bitcoin

import "encoding/binary"

const hardenedOffset = uint32(0x80000000)

var xprivVersionByNetwork = map[Network][4]byte{
	Mainnet: {0x04, 0x88, 0xAD, 0xE4},
	Testnet: {0x04, 0x35, 0x83, 0x94},
	Signet:  {0x04, 0x35, 0x83, 0x94},
	Regtest: {0x04, 0x35, 0x83, 0x94},
}

var xpubVersionByNetwork = map[Network][4]byte{
	Mainnet: {0x04, 0x88, 0xB2, 0x1E},
	Testnet: {0x04, 0x35, 0x87, 0xCF},
	Signet:  {0x04, 0x35, 0x87, 0xCF},
	Regtest: {0x04, 0x35, 0x87, 0xCF},
}

// NewMasterKeyForNetwork derives a BIP32 master key and sets the serialization
// version for the requested Bitcoin network.
func NewMasterKeyForNetwork(seed []byte, network Network) (*ExtendedKey, error) {
	key, err := NewMasterKey(seed)
	if err != nil {
		return nil, err
	}
	key.network = network
	if version, ok := xprivVersionByNetwork[network]; ok {
		copy(key.version[:], version[:])
	}
	return key, nil
}

// DeriveAccountXPriv derives the BIP84 account private key:
// m/84'/0'/0' for mainnet and m/84'/1'/0' for testnet/signet/regtest.
func DeriveAccountXPriv(master *ExtendedKey, network Network) (*ExtendedKey, error) {
	coinType := uint32(0)
	if network == Testnet || network == Signet || network == Regtest {
		coinType = 1
	}
	purpose, err := master.PrivateChild(hardenedOffset + 84)
	if err != nil {
		return nil, err
	}
	coin, err := purpose.PrivateChild(hardenedOffset + coinType)
	if err != nil {
		return nil, err
	}
	account, err := coin.PrivateChild(hardenedOffset)
	if err != nil {
		return nil, err
	}
	account.network = network
	if version, ok := xprivVersionByNetwork[network]; ok {
		copy(account.version[:], version[:])
	}
	return account, nil
}

// Neuter returns the public extended key corresponding to a private key.
func (ek *ExtendedKey) Neuter() *ExtendedKey {
	out := *ek
	out.isPrivate = false
	if version, ok := xpubVersionByNetwork[ek.network]; ok {
		copy(out.version[:], version[:])
	}
	copy(out.key[:], ek.CompressedPubKey())
	return &out
}

// String serializes an extended key as Base58Check xpub/tpub/xpriv/tpriv.
func (ek *ExtendedKey) String() string {
	payload := make([]byte, 78)
	copy(payload[0:4], ek.version[:])
	payload[4] = ek.depth
	copy(payload[5:9], ek.fingerprint[:])
	binary.BigEndian.PutUint32(payload[9:13], ek.childNum)
	copy(payload[13:45], ek.chainCode[:])
	copy(payload[45:78], ek.key[:])
	return base58CheckEncode(payload)
}
