package mobile

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"payment-gateway/internal/models"
	"payment-gateway/internal/privacy"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const ownerMobileEmail = "paulo@chainfx.com"
const defaultOwnerMobileWalletAddress = "0xb6B00781a81ee10E77f9417d24531FB2529955aA"

func (s *Server) ensureUserWallet(ctx context.Context, user *models.User) (*models.User, error) {
	if user == nil {
		return nil, nil
	}
	if strings.EqualFold(strings.TrimSpace(user.Email), ownerMobileEmail) {
		return s.ensureOwnerMobileWallet(ctx, user)
	}
	if user.WalletAddress != nil && strings.TrimSpace(*user.WalletAddress) != "" {
		return user, nil
	}
	if s == nil || s.db == nil {
		return user, nil
	}

	secret := s.mobileWalletEncryptionSecret()
	codec, err := privacy.New(secret)
	if err != nil {
		return nil, err
	}

	for attempts := 0; attempts < 3; attempts++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			return nil, err
		}
		privateKeyHex := "0x" + hex.EncodeToString(crypto.FromECDSA(key))
		encryptedKey, err := codec.Encrypt(privateKeyHex)
		if err != nil {
			return nil, err
		}
		address := crypto.PubkeyToAddress(key.PublicKey).Hex()

		updated, err := mobileDB(s.db).AttachSystemWallet(ctx, user.ID, address, encryptedKey)
		if err == nil {
			return updated, nil
		}
		if !strings.Contains(strings.ToLower(err.Error()), "duplicate") &&
			!strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, err
		}
	}
	return nil, fmt.Errorf("nao foi possivel gerar carteira unica para o usuario")
}

func (s *Server) ensureOwnerMobileWallet(ctx context.Context, user *models.User) (*models.User, error) {
	ownerAddress := strings.TrimSpace(envOr("OWNER_MOBILE_WALLET_ADDRESS", defaultOwnerMobileWalletAddress))
	if !common.IsHexAddress(ownerAddress) {
		return nil, fmt.Errorf("OWNER_MOBILE_WALLET_ADDRESS invalido")
	}
	checksummed := common.HexToAddress(ownerAddress).Hex()
	if err := s.ensureOwnerMobileWalletKey(ctx, user.ID, checksummed); err != nil {
		return nil, err
	}
	if user.WalletAddress != nil && strings.EqualFold(strings.TrimSpace(*user.WalletAddress), checksummed) {
		return user, nil
	}
	if s == nil || s.db == nil {
		user.WalletAddress = &checksummed
		return user, nil
	}
	if err := mobileDB(s.db).UpdateUser(ctx, user.ID, map[string]any{"wallet_address": checksummed}); err != nil {
		return nil, err
	}
	return mobileDB(s.db).GetUserByID(ctx, user.ID)
}

func (s *Server) ensureOwnerMobileWalletKey(ctx context.Context, userID, expectedAddress string) error {
	if s == nil || s.db == nil {
		return nil
	}
	privateKeyHex := strings.TrimSpace(envOr("OWNER_MOBILE_WALLET_PRIVATE_KEY", ""))
	if privateKeyHex == "" {
		return nil
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(privateKeyHex, "0x"))
	if err != nil {
		return fmt.Errorf("OWNER_MOBILE_WALLET_PRIVATE_KEY invalida: %w", err)
	}
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()
	if !strings.EqualFold(address, expectedAddress) {
		return fmt.Errorf("OWNER_MOBILE_WALLET_PRIVATE_KEY nao corresponde a OWNER_MOBILE_WALLET_ADDRESS")
	}
	secret := s.mobileWalletEncryptionSecret()
	codec, err := privacy.New(secret)
	if err != nil {
		return err
	}
	encryptedKey, err := codec.Encrypt("0x" + hex.EncodeToString(crypto.FromECDSA(key)))
	if err != nil {
		return err
	}
	return mobileDB(s.db).UpsertCustodialWalletKey(ctx, userID, expectedAddress, encryptedKey)
}

func (s *Server) mobileWalletEncryptionSecret() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	if secret := strings.TrimSpace(envOr("MOBILE_WALLET_ENCRYPTION_SECRET", "")); secret != "" {
		return secret
	}
	if secret := strings.TrimSpace(s.cfg.LGPDSecret); secret != "" {
		return secret
	}
	if secret := strings.TrimSpace(s.cfg.WebhookSecret); secret != "" {
		return secret
	}
	return strings.TrimSpace(s.mcfg.JWTSecret)
}
