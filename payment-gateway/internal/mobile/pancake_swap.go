package mobile

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

const pancakeV2RouterABIJSONMobile = `[
	{"name":"getAmountsOut","type":"function","stateMutability":"view","inputs":[{"name":"amountIn","type":"uint256"},{"name":"path","type":"address[]"}],"outputs":[{"name":"amounts","type":"uint256[]"}]}
]`

type mobilePancakeQuote struct {
	EstimatedTo  float64
	Rate         float64
	AmountInRaw  string
	AmountOutRaw string
	MinOutRaw    string
	SlippageBPS  int
	Path         []string
	Router       string
}

func (s *Server) mobilePancakeQuote(ctx context.Context, fromAsset, toAsset string, amount, slippage float64) (*mobilePancakeQuote, error) {
	if s == nil || s.cfg == nil || !s.cfg.MobileSwapPancakeEnabled {
		return nil, fmt.Errorf("pancake swap desabilitado")
	}
	router := strings.TrimSpace(s.cfg.PancakeV2Router)
	if !common.IsHexAddress(router) {
		return nil, fmt.Errorf("PANCAKE_V2_ROUTER invalido")
	}
	if amount <= 0 {
		return nil, fmt.Errorf("amount deve ser positivo")
	}
	slippageBPS := slippageToBPS(slippage)
	fromToken, fromDecimals, err := s.mobilePancakeToken(fromAsset)
	if err != nil {
		return nil, err
	}
	toToken, toDecimals, err := s.mobilePancakeToken(toAsset)
	if err != nil {
		return nil, err
	}
	if fromToken == toToken {
		return nil, fmt.Errorf("tokens iguais")
	}
	amountInRaw, err := parseTokenAmount(fmt.Sprintf("%.18f", amount), fromDecimals)
	if err != nil {
		return nil, err
	}
	path := []common.Address{fromToken, toToken}
	amounts, err := s.callPancakeAmountsOut(ctx, common.HexToAddress(router), amountInRaw, path)
	if err != nil {
		return nil, err
	}
	if len(amounts) == 0 || amounts[len(amounts)-1].Sign() <= 0 {
		return nil, fmt.Errorf("pancake retornou saida vazia")
	}
	amountOutRaw := amounts[len(amounts)-1]
	estimatedTo := tokenRawToFloat(amountOutRaw, toDecimals)
	if estimatedTo <= 0 {
		return nil, fmt.Errorf("pancake retornou cotacao invalida")
	}
	return &mobilePancakeQuote{
		EstimatedTo:  estimatedTo,
		Rate:         estimatedTo / amount,
		AmountInRaw:  amountInRaw.String(),
		AmountOutRaw: amountOutRaw.String(),
		MinOutRaw:    minOutRaw(amountOutRaw, slippageBPS).String(),
		SlippageBPS:  slippageBPS,
		Path:         []string{fromToken.Hex(), toToken.Hex()},
		Router:       common.HexToAddress(router).Hex(),
	}, nil
}

func (s *Server) mobilePancakeToken(asset string) (common.Address, int, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if asset == "BNB" {
		return common.Address{}, 0, fmt.Errorf("swap com BNB nativo ainda nao suportado")
	}
	contract, decimals, _, err := s.mobileTransferToken(asset, "BSC")
	if err != nil {
		if fallback, fallbackDecimals, ok := defaultBSCPancakeToken(asset); ok {
			return common.HexToAddress(fallback), fallbackDecimals, nil
		}
		return common.Address{}, 0, err
	}
	if !common.IsHexAddress(contract) {
		return common.Address{}, 0, fmt.Errorf("%s BSC sem contrato ERC20 configurado", asset)
	}
	return common.HexToAddress(contract), decimals, nil
}

func (s *Server) callPancakeAmountsOut(ctx context.Context, router common.Address, amountIn *big.Int, path []common.Address) ([]*big.Int, error) {
	parsed, err := abi.JSON(strings.NewReader(pancakeV2RouterABIJSONMobile))
	if err != nil {
		return nil, err
	}
	data, err := parsed.Pack("getAmountsOut", amountIn, path)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, rpcURL := range s.mobileTransferRPCURLs("BSC") {
		callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		client, err := ethclient.DialContext(callCtx, rpcURL)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		raw, err := client.CallContract(callCtx, ethereum.CallMsg{To: &router, Data: data}, nil)
		client.Close()
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		out, err := parsed.Unpack("getAmountsOut", raw)
		if err != nil {
			return nil, err
		}
		amounts, ok := out[0].([]*big.Int)
		if !ok {
			return nil, fmt.Errorf("resposta Pancake invalida")
		}
		return amounts, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("BSC_RPC_URLS nao configurado")
}

func defaultBSCPancakeToken(asset string) (string, int, bool) {
	switch strings.ToUpper(strings.TrimSpace(asset)) {
	case "USDT":
		return "0x55d398326f99059fF775485246999027B3197955", 18, true
	case "USDC":
		return "0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d", 18, true
	case "BTC", "BTCB":
		return "0x7130d2A12B9BCbFAe4f2634d864A1Ee1Ce3Ead9c", 18, true
	case "ETH":
		return "0x2170Ed0880ac9A755fd29B2688956BD959F933F8", 18, true
	case "LINK":
		return "0xF8A0BF9cF54Bb92F17374d9e9A321E6a111a51bD", 18, true
	case "AVAX":
		return "0x1CE0c2827e2eF14D5C4f29a091d735A204794041", 18, true
	default:
		return "", 0, false
	}
}

func tokenRawToFloat(raw *big.Int, decimals int) float64 {
	if raw == nil || decimals < 0 {
		return 0
	}
	rat := new(big.Rat).SetInt(raw)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	rat.Quo(rat, new(big.Rat).SetInt(scale))
	out, _ := rat.Float64()
	return out
}

func slippageToBPS(slippage float64) int {
	bps := int(slippage*10_000 + 0.5)
	if bps <= 0 {
		return 50
	}
	if bps > 9_900 {
		return 9_900
	}
	return bps
}

func minOutRaw(expected *big.Int, slippageBPS int) *big.Int {
	if expected == nil || expected.Sign() <= 0 {
		return big.NewInt(0)
	}
	if slippageBPS < 0 {
		slippageBPS = 0
	}
	if slippageBPS > 9_900 {
		slippageBPS = 9_900
	}
	out := new(big.Int).Mul(expected, big.NewInt(int64(10_000-slippageBPS)))
	return out.Div(out, big.NewInt(10_000))
}
