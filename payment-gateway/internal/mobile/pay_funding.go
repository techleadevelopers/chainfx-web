package mobile

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

var mobilePayERC20TransferTopic = common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")

type mobilePayFundingReceipt struct {
	ChainID       int64
	TxHash        string
	LogIndex      int
	BlockNumber   uint64
	BlockHash     string
	From          string
	To            string
	TokenContract string
	AmountRaw     string
	RequiredRaw   string
	Confirmations uint64
}

type mobilePayFundingPendingError struct {
	status  string
	message string
}

func (e mobilePayFundingPendingError) Error() string { return e.message }

func isMobilePayFundingPending(err error) (string, bool) {
	var pending mobilePayFundingPendingError
	if errors.As(err, &pending) {
		return pending.status, true
	}
	return "", false
}

func (s *Server) mobilePayFundingSpec() (network, tokenContract string, decimals, chainID int, treasury string, err error) {
	treasury = strings.TrimSpace(s.cfg.TreasuryHot)
	if !common.IsHexAddress(treasury) {
		return "", "", 0, 0, "", fmt.Errorf("TREASURY_HOT nao configurado para pagamentos mobile")
	}
	network = "BSC"
	tokenContract, decimals, chainID, err = s.mobileTransferToken("USDT", network)
	if err != nil {
		return "", "", 0, 0, "", err
	}
	if !common.IsHexAddress(tokenContract) {
		return "", "", 0, 0, "", fmt.Errorf("contrato USDT %s nao configurado", network)
	}
	return network, common.HexToAddress(tokenContract).Hex(), decimals, chainID, common.HexToAddress(treasury).Hex(), nil
}

func (s *Server) mobilePayRequiredConfirmations(network string) uint64 {
	if s == nil || s.cfg == nil {
		return 3
	}
	switch strings.ToUpper(strings.TrimSpace(network)) {
	case "POLYGON":
		if s.cfg.PolygonMinConfirmations < 64 {
			return 64
		}
		return s.cfg.PolygonMinConfirmations
	default:
		if s.cfg.BSCMinConfirmations < 3 {
			return 3
		}
		return s.cfg.BSCMinConfirmations
	}
}

func mobilePayMicroToRaw(micros int64, decimals int) *big.Int {
	if micros <= 0 {
		return big.NewInt(0)
	}
	raw := big.NewInt(micros)
	if decimals == 6 {
		return raw
	}
	if decimals > 6 {
		return raw.Mul(raw, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals-6)), nil))
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(6-decimals)), nil)
	raw.Add(raw, new(big.Int).Sub(divisor, big.NewInt(1)))
	return raw.Div(raw, divisor)
}

func (s *Server) verifyMobilePayUSDTFunding(
	ctx context.Context,
	network string,
	txHash string,
	tokenContract string,
	decimals int,
	fromAddress string,
	toAddress string,
	requiredMicros int64,
) (mobilePayFundingReceipt, error) {
	txHash = strings.TrimSpace(txHash)
	if !common.IsHexHash(txHash) {
		return mobilePayFundingReceipt{}, fmt.Errorf("tx_hash invalido")
	}
	if !common.IsHexAddress(fromAddress) || !common.IsHexAddress(toAddress) || !common.IsHexAddress(tokenContract) {
		return mobilePayFundingReceipt{}, fmt.Errorf("wallet, tesouraria ou contrato USDT invalido")
	}
	expected := mobilePayMicroToRaw(requiredMicros, decimals)
	if expected.Sign() <= 0 {
		return mobilePayFundingReceipt{}, fmt.Errorf("valor USDT esperado invalido")
	}
	pool := s.evmPool(network)
	if pool == nil {
		return mobilePayFundingReceipt{}, fmt.Errorf("RPC %s nao configurado", network)
	}

	var out mobilePayFundingReceipt
	err := pool.Do(ctx, func(client *ethclient.Client) error {
		chainID, err := client.ChainID(ctx)
		if err != nil {
			return fmt.Errorf("falha ao validar chainId: %w", err)
		}
		expectedChainID := s.mobileTransferChainID(network)
		if chainID.Int64() != int64(expectedChainID) {
			return fmt.Errorf("chainId invalido: esperado %d recebido %d", expectedChainID, chainID.Int64())
		}
		receipt, err := client.TransactionReceipt(ctx, common.HexToHash(txHash))
		if err != nil {
			if errors.Is(err, ethereum.NotFound) {
				return mobilePayFundingPendingError{status: "awaiting_funding", message: "tx ainda nao encontrada na rede"}
			}
			return fmt.Errorf("falha ao buscar receipt: %w", err)
		}
		if receipt.Status != types.ReceiptStatusSuccessful {
			return fmt.Errorf("tx on-chain sem sucesso")
		}
		latest, err := client.HeaderByNumber(ctx, nil)
		if err != nil {
			return fmt.Errorf("falha ao validar bloco mais recente: %w", err)
		}
		if latest.Number == nil || receipt.BlockNumber == nil || latest.Number.Cmp(receipt.BlockNumber) < 0 {
			return mobilePayFundingPendingError{status: "funding_seen", message: "funding aguardando confirmacoes"}
		}
		confirmations := new(big.Int).Sub(latest.Number, receipt.BlockNumber).Uint64() + 1
		if required := s.mobilePayRequiredConfirmations(network); confirmations < required {
			return mobilePayFundingPendingError{status: "funding_seen", message: fmt.Sprintf("funding aguardando confirmacoes: %d/%d", confirmations, required)}
		}
		header, err := client.HeaderByNumber(ctx, receipt.BlockNumber)
		if err != nil {
			return fmt.Errorf("falha ao validar bloco do pagamento: %w", err)
		}
		if header.Hash() != receipt.BlockHash {
			return fmt.Errorf("blockHash do pagamento nao esta canonico")
		}

		from := common.HexToAddress(fromAddress)
		to := common.HexToAddress(toAddress)
		token := common.HexToAddress(tokenContract)
		for _, lg := range receipt.Logs {
			if lg.Address != token || len(lg.Topics) < 3 || lg.Topics[0] != mobilePayERC20TransferTopic {
				continue
			}
			if mobilePayTopicAddress(lg.Topics[1]) != from || mobilePayTopicAddress(lg.Topics[2]) != to {
				continue
			}
			paid := new(big.Int).SetBytes(lg.Data)
			if paid.Cmp(expected) < 0 {
				continue
			}
			out = mobilePayFundingReceipt{
				ChainID:       chainID.Int64(),
				TxHash:        strings.ToLower(txHash),
				LogIndex:      int(lg.Index),
				BlockNumber:   receipt.BlockNumber.Uint64(),
				BlockHash:     strings.ToLower(receipt.BlockHash.Hex()),
				From:          strings.ToLower(from.Hex()),
				To:            strings.ToLower(to.Hex()),
				TokenContract: strings.ToLower(token.Hex()),
				AmountRaw:     paid.String(),
				RequiredRaw:   expected.String(),
				Confirmations: confirmations,
			}
			return nil
		}
		return fmt.Errorf("tx nao contem Transfer USDT suficiente para a tesouraria")
	})
	return out, err
}

func mobilePayTopicAddress(topic common.Hash) common.Address {
	raw := topic.Bytes()
	if len(raw) >= 20 {
		return common.BytesToAddress(raw[len(raw)-20:])
	}
	return common.Address{}
}
