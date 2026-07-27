package eip712

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	TypeM2MIntent          = "M2MIntent"
	TypeMobileTransfer     = "MobileTransfer"
	TypeCapabilityPurchase = "CapabilityPurchase"
	TypePayIntent          = "PayIntent"
)

var (
	ErrUnknownIntentType = errors.New("unknown EIP-712 intent type")
	ErrExpiredIntent     = errors.New("EIP-712 intent deadline expired")
)

type Domain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           int64  `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

type Intent struct {
	IntentType     string         `json:"intentType"`
	Payer          string         `json:"payer,omitempty"`
	Payee          string         `json:"payee,omitempty"`
	From           string         `json:"from,omitempty"`
	To             string         `json:"to,omitempty"`
	Recipient      string         `json:"recipient,omitempty"`
	Agent          string         `json:"agent,omitempty"`
	Asset          string         `json:"asset"`
	Amount         string         `json:"amount"`
	FeeBps         uint64         `json:"feeBps,omitempty"`
	Quota          uint64         `json:"quota,omitempty"`
	Capability     string         `json:"capability,omitempty"`
	Nonce          string         `json:"nonce"`
	Deadline       uint64         `json:"deadline"`
	IdempotencyKey string         `json:"idempotencyKey"`
	Raw            map[string]any `json:"raw,omitempty"`
}

type TypedData struct {
	Types       map[string][]TypedField `json:"types"`
	PrimaryType string                  `json:"primaryType"`
	Domain      Domain                  `json:"domain"`
	Message     map[string]any          `json:"message"`
}

type TypedField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type PreparedIntent struct {
	IntentType     string         `json:"intentType"`
	Domain         Domain         `json:"domain"`
	TypedData      TypedData      `json:"typedData"`
	StructHash     string         `json:"structHash"`
	DomainHash     string         `json:"domainHash"`
	Digest         string         `json:"digest"`
	SignerField    string         `json:"signerField"`
	ExpectedSigner string         `json:"expectedSigner"`
	Nonce          string         `json:"nonce"`
	Deadline       uint64         `json:"deadline"`
	Rail           RailDecision   `json:"rail"`
	Message        map[string]any `json:"message"`
}

type RailDecision struct {
	Asset                     string `json:"asset"`
	Network                   string `json:"network"`
	TokenContract             string `json:"tokenContract,omitempty"`
	SupportsEIP2612           bool   `json:"supports_eip2612"`
	SupportsEIP3009           bool   `json:"supports_eip3009"`
	SupportsPermit2           bool   `json:"supports_permit2"`
	SupportsCustodialRelay    bool   `json:"custodial_relay"`
	PreferredRail             string `json:"preferredRail"`
	Phase                     string `json:"phase"`
	Reason                    string `json:"reason"`
	EIP4337SmartAccountStatus string `json:"eip4337SmartAccountStatus"`
	EIP7702DelegationStatus   string `json:"eip7702DelegationStatus"`
}

type AssetCapability struct {
	Symbol          string `json:"symbol"`
	Network         string `json:"network"`
	TokenContract   string `json:"tokenContract,omitempty"`
	Decimals        int    `json:"decimals"`
	SupportsEIP2612 bool   `json:"supports_eip2612"`
	SupportsEIP3009 bool   `json:"supports_eip3009"`
	SupportsPermit2 bool   `json:"supports_permit2"`
	CustodialRelay  bool   `json:"custodial_relay"`
	PreferredRail   string `json:"preferredRail"`
}

type Verification struct {
	PreparedIntent
	RecoveredSigner string `json:"recoveredSigner"`
	Signature       string `json:"signature"`
	Valid           bool   `json:"valid"`
}

func Prepare(domain Domain, intent Intent, assets []AssetCapability) (PreparedIntent, error) {
	domain = NormalizeDomain(domain)
	intent.IntentType = normalizeIntentType(intent.IntentType)
	if _, ok := typeDefinitions()[intent.IntentType]; !ok {
		return PreparedIntent{}, ErrUnknownIntentType
	}
	if intent.Deadline > 0 && time.Now().Unix() > int64(intent.Deadline) {
		return PreparedIntent{}, ErrExpiredIntent
	}
	msg, signerField, signer := messageForIntent(intent)
	typed := TypedData{
		Types:       typeDefinitions(),
		PrimaryType: intent.IntentType,
		Domain:      domain,
		Message:     msg,
	}
	dh := domainSeparator(domain)
	sh, err := structHash(intent.IntentType, msg)
	if err != nil {
		return PreparedIntent{}, err
	}
	digest := crypto.Keccak256Hash(append(append([]byte{0x19, 0x01}, dh.Bytes()...), sh.Bytes()...))
	return PreparedIntent{
		IntentType:     intent.IntentType,
		Domain:         domain,
		TypedData:      typed,
		StructHash:     sh.Hex(),
		DomainHash:     dh.Hex(),
		Digest:         digest.Hex(),
		SignerField:    signerField,
		ExpectedSigner: strings.ToLower(signer),
		Nonce:          fmt.Sprint(msg["nonce"]),
		Deadline:       intent.Deadline,
		Rail:           DecideRail(fmt.Sprint(msg["asset"]), assets),
		Message:        msg,
	}, nil
}

func Verify(domain Domain, intent Intent, signature string, assets []AssetCapability) (Verification, error) {
	prepared, err := Prepare(domain, intent, assets)
	if err != nil {
		return Verification{}, err
	}
	recovered, err := RecoverSigner(prepared.Digest, signature)
	if err != nil {
		return Verification{}, err
	}
	return Verification{
		PreparedIntent:  prepared,
		RecoveredSigner: recovered,
		Signature:       signature,
		Valid:           strings.EqualFold(recovered, prepared.ExpectedSigner),
	}, nil
}

func RecoverSigner(digestHex, signatureHex string) (string, error) {
	digest, err := decodeFixedHex(digestHex, 32)
	if err != nil {
		return "", fmt.Errorf("digest invalido: %w", err)
	}
	sig, err := decodeFixedHex(signatureHex, 65)
	if err != nil {
		return "", fmt.Errorf("signature invalida: %w", err)
	}
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	if sig[64] != 0 && sig[64] != 1 {
		return "", fmt.Errorf("signature recovery id invalido")
	}
	pub, err := crypto.SigToPub(digest, sig)
	if err != nil {
		return "", err
	}
	return strings.ToLower(crypto.PubkeyToAddress(*pub).Hex()), nil
}

func NormalizeDomain(domain Domain) Domain {
	if strings.TrimSpace(domain.Name) == "" {
		domain.Name = "ChainFX"
	}
	if strings.TrimSpace(domain.Version) == "" {
		domain.Version = "1"
	}
	if domain.ChainID == 0 {
		domain.ChainID = 56
	}
	if !common.IsHexAddress(domain.VerifyingContract) {
		domain.VerifyingContract = "0x0000000000000000000000000000000000000000"
	}
	domain.VerifyingContract = strings.ToLower(common.HexToAddress(domain.VerifyingContract).Hex())
	return domain
}

func DecideRail(asset string, assets []AssetCapability) RailDecision {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	for _, cap := range assets {
		if strings.EqualFold(cap.Symbol, asset) || (cap.TokenContract != "" && strings.EqualFold(cap.TokenContract, asset)) {
			return railFromCapability(cap)
		}
	}
	switch asset {
	case "USDC":
		return railFromCapability(AssetCapability{Symbol: "USDC", Network: "BSC", SupportsEIP3009: true, SupportsPermit2: true, CustodialRelay: true, PreferredRail: "eip3009_transfer_with_authorization"})
	case "USDT":
		return railFromCapability(AssetCapability{Symbol: "USDT", Network: "BSC", CustodialRelay: true, PreferredRail: "custodial_relay"})
	default:
		return railFromCapability(AssetCapability{Symbol: asset, Network: "BSC", SupportsPermit2: true, CustodialRelay: true, PreferredRail: "custodial_relay"})
	}
}

func BuildEIP2612PermitCalldata(owner, spender string, value *big.Int, deadline uint64, signature string) (string, error) {
	r, s, v, err := splitSignature(signature)
	if err != nil {
		return "", err
	}
	payload := append(crypto.Keccak256([]byte("permit(address,address,uint256,uint256,uint8,bytes32,bytes32)"))[:4],
		encodeAddress(owner)...)
	payload = append(payload, encodeAddress(spender)...)
	payload = append(payload, encodeUint(value)...)
	payload = append(payload, encodeUint(new(big.Int).SetUint64(deadline))...)
	payload = append(payload, encodeUint(new(big.Int).SetUint64(uint64(v)))...)
	payload = append(payload, r...)
	payload = append(payload, s...)
	return "0x" + hex.EncodeToString(payload), nil
}

func BuildEIP3009TransferWithAuthorizationCalldata(from, to string, value *big.Int, validAfter, validBefore uint64, nonceHex, signature string) (string, error) {
	nonce, err := decodeBytes32(nonceHex)
	if err != nil {
		return "", err
	}
	r, s, v, err := splitSignature(signature)
	if err != nil {
		return "", err
	}
	payload := append(crypto.Keccak256([]byte("transferWithAuthorization(address,address,uint256,uint256,uint256,bytes32,uint8,bytes32,bytes32)"))[:4],
		encodeAddress(from)...)
	payload = append(payload, encodeAddress(to)...)
	payload = append(payload, encodeUint(value)...)
	payload = append(payload, encodeUint(new(big.Int).SetUint64(validAfter))...)
	payload = append(payload, encodeUint(new(big.Int).SetUint64(validBefore))...)
	payload = append(payload, nonce...)
	payload = append(payload, encodeUint(new(big.Int).SetUint64(uint64(v)))...)
	payload = append(payload, r...)
	payload = append(payload, s...)
	return "0x" + hex.EncodeToString(payload), nil
}

func NormalizeIntent(input map[string]any, fallbackType string) Intent {
	intent := Intent{IntentType: firstString(input, "intentType", "type")}
	if intent.IntentType == "" {
		intent.IntentType = fallbackType
	}
	intent.Payer = firstString(input, "payer", "payerWallet", "agentWallet")
	intent.Payee = firstString(input, "payee", "merchant", "recipient")
	intent.From = firstString(input, "from", "payer", "payerWallet")
	intent.To = firstString(input, "to", "recipient", "payee")
	intent.Recipient = firstString(input, "recipient", "to", "payee")
	intent.Agent = firstString(input, "agent", "agentWallet")
	intent.Asset = strings.ToUpper(firstString(input, "asset", "paymentAsset", "payAsset"))
	if intent.Asset == "" {
		intent.Asset = "USDT"
	}
	intent.Amount = firstString(input, "amount", "amountRaw", "payAmount")
	if intent.Amount == "" {
		intent.Amount = "0"
	}
	intent.FeeBps = uint64FromAny(firstAny(input, "feeBps", "fee_bps"))
	intent.Quota = uint64FromAny(firstAny(input, "quota", "units"))
	intent.Capability = firstString(input, "capability", "capabilityId")
	intent.Nonce = firstString(input, "nonce")
	intent.Deadline = uint64FromAny(firstAny(input, "deadline", "expiresAtUnix"))
	intent.IdempotencyKey = firstString(input, "idempotencyKey", "idempotency_key")
	intent.Raw = input
	return intent
}

func typeDefinitions() map[string][]TypedField {
	return map[string][]TypedField{
		"EIP712Domain": {
			{Name: "name", Type: "string"},
			{Name: "version", Type: "string"},
			{Name: "chainId", Type: "uint256"},
			{Name: "verifyingContract", Type: "address"},
		},
		TypeM2MIntent: {
			{Name: "payer", Type: "address"},
			{Name: "recipient", Type: "address"},
			{Name: "asset", Type: "address"},
			{Name: "amount", Type: "uint256"},
			{Name: "feeBps", Type: "uint256"},
			{Name: "nonce", Type: "bytes32"},
			{Name: "deadline", Type: "uint256"},
			{Name: "idempotencyKey", Type: "bytes32"},
		},
		TypeMobileTransfer: {
			{Name: "from", Type: "address"},
			{Name: "to", Type: "address"},
			{Name: "asset", Type: "address"},
			{Name: "amount", Type: "uint256"},
			{Name: "nonce", Type: "bytes32"},
			{Name: "deadline", Type: "uint256"},
			{Name: "idempotencyKey", Type: "bytes32"},
		},
		TypeCapabilityPurchase: {
			{Name: "payer", Type: "address"},
			{Name: "agent", Type: "address"},
			{Name: "asset", Type: "address"},
			{Name: "capability", Type: "bytes32"},
			{Name: "amount", Type: "uint256"},
			{Name: "quota", Type: "uint256"},
			{Name: "nonce", Type: "bytes32"},
			{Name: "deadline", Type: "uint256"},
			{Name: "idempotencyKey", Type: "bytes32"},
		},
		TypePayIntent: {
			{Name: "payer", Type: "address"},
			{Name: "payee", Type: "address"},
			{Name: "asset", Type: "address"},
			{Name: "amount", Type: "uint256"},
			{Name: "feeBps", Type: "uint256"},
			{Name: "nonce", Type: "bytes32"},
			{Name: "deadline", Type: "uint256"},
			{Name: "idempotencyKey", Type: "bytes32"},
		},
	}
}

func messageForIntent(intent Intent) (map[string]any, string, string) {
	asset := addressOrZero(intent.Asset)
	amount := amountString(intent.Amount)
	nonce := bytes32Hex(intent.Nonce)
	idem := bytes32Hex(intent.IdempotencyKey)
	switch intent.IntentType {
	case TypeMobileTransfer:
		from := addressOrZero(firstNonEmpty(intent.From, intent.Payer))
		return map[string]any{"from": from, "to": addressOrZero(intent.To), "asset": asset, "amount": amount, "nonce": nonce, "deadline": intent.Deadline, "idempotencyKey": idem}, "from", from
	case TypeCapabilityPurchase:
		payer := addressOrZero(intent.Payer)
		return map[string]any{"payer": payer, "agent": addressOrZero(intent.Agent), "asset": asset, "capability": bytes32Hex(intent.Capability), "amount": amount, "quota": intent.Quota, "nonce": nonce, "deadline": intent.Deadline, "idempotencyKey": idem}, "payer", payer
	case TypePayIntent:
		payer := addressOrZero(intent.Payer)
		return map[string]any{"payer": payer, "payee": addressOrZero(intent.Payee), "asset": asset, "amount": amount, "feeBps": intent.FeeBps, "nonce": nonce, "deadline": intent.Deadline, "idempotencyKey": idem}, "payer", payer
	default:
		payer := addressOrZero(intent.Payer)
		return map[string]any{"payer": payer, "recipient": addressOrZero(intent.Recipient), "asset": asset, "amount": amount, "feeBps": intent.FeeBps, "nonce": nonce, "deadline": intent.Deadline, "idempotencyKey": idem}, "payer", payer
	}
}

func domainSeparator(domain Domain) common.Hash {
	typeHash := crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	return crypto.Keccak256Hash(
		typeHash.Bytes(),
		crypto.Keccak256Hash([]byte(domain.Name)).Bytes(),
		crypto.Keccak256Hash([]byte(domain.Version)).Bytes(),
		encodeUint(big.NewInt(domain.ChainID)),
		encodeAddress(domain.VerifyingContract),
	)
}

func structHash(primaryType string, msg map[string]any) (common.Hash, error) {
	switch primaryType {
	case TypeM2MIntent:
		return hashTyped(primaryType, "M2MIntent(address payer,address recipient,address asset,uint256 amount,uint256 feeBps,bytes32 nonce,uint256 deadline,bytes32 idempotencyKey)", msg)
	case TypeMobileTransfer:
		return hashTyped(primaryType, "MobileTransfer(address from,address to,address asset,uint256 amount,bytes32 nonce,uint256 deadline,bytes32 idempotencyKey)", msg)
	case TypeCapabilityPurchase:
		return hashTyped(primaryType, "CapabilityPurchase(address payer,address agent,address asset,bytes32 capability,uint256 amount,uint256 quota,bytes32 nonce,uint256 deadline,bytes32 idempotencyKey)", msg)
	case TypePayIntent:
		return hashTyped(primaryType, "PayIntent(address payer,address payee,address asset,uint256 amount,uint256 feeBps,bytes32 nonce,uint256 deadline,bytes32 idempotencyKey)", msg)
	default:
		return common.Hash{}, ErrUnknownIntentType
	}
}

func hashTyped(primaryType, typeString string, msg map[string]any) (common.Hash, error) {
	parts := [][]byte{crypto.Keccak256Hash([]byte(typeString)).Bytes()}
	fields := typeDefinitions()[primaryType]
	for _, f := range fields {
		switch f.Type {
		case "address":
			parts = append(parts, encodeAddress(fmt.Sprint(msg[f.Name])))
		case "uint256":
			n, err := parseUint256(msg[f.Name])
			if err != nil {
				return common.Hash{}, fmt.Errorf("%s invalido: %w", f.Name, err)
			}
			parts = append(parts, encodeUint(n))
		case "bytes32":
			v, err := decodeBytes32(fmt.Sprint(msg[f.Name]))
			if err != nil {
				return common.Hash{}, fmt.Errorf("%s invalido: %w", f.Name, err)
			}
			parts = append(parts, v)
		default:
			return common.Hash{}, fmt.Errorf("tipo EIP-712 nao suportado: %s", f.Type)
		}
	}
	return crypto.Keccak256Hash(parts...), nil
}

func railFromCapability(cap AssetCapability) RailDecision {
	if cap.PreferredRail == "" {
		switch {
		case cap.SupportsEIP3009:
			cap.PreferredRail = "eip3009_transfer_with_authorization"
		case cap.SupportsEIP2612:
			cap.PreferredRail = "eip2612_permit"
		default:
			cap.PreferredRail = "custodial_relay"
		}
	}
	reason := "EIP-712 typed intent is always required; selected rail follows token capability detection."
	return RailDecision{
		Asset:                     strings.ToUpper(cap.Symbol),
		Network:                   firstNonEmpty(cap.Network, "BSC"),
		TokenContract:             strings.ToLower(cap.TokenContract),
		SupportsEIP2612:           cap.SupportsEIP2612,
		SupportsEIP3009:           cap.SupportsEIP3009,
		SupportsPermit2:           cap.SupportsPermit2,
		SupportsCustodialRelay:    cap.CustodialRelay,
		PreferredRail:             cap.PreferredRail,
		Phase:                     "phase_1",
		Reason:                    reason,
		EIP4337SmartAccountStatus: "planned_phase_2",
		EIP7702DelegationStatus:   "planned_phase_3_guarded",
	}
}

func splitSignature(signature string) ([]byte, []byte, byte, error) {
	sig, err := decodeFixedHex(signature, 65)
	if err != nil {
		return nil, nil, 0, err
	}
	v := sig[64]
	if v < 27 {
		v += 27
	}
	return sig[:32], sig[32:64], v, nil
}

func parseUint256(value any) (*big.Int, error) {
	switch v := value.(type) {
	case json.Number:
		return parseUint256(v.String())
	case float64:
		return big.NewInt(int64(v)), nil
	case int:
		return big.NewInt(int64(v)), nil
	case int64:
		return big.NewInt(v), nil
	case uint64:
		return new(big.Int).SetUint64(v), nil
	case string:
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "0x") {
			n := new(big.Int)
			if _, ok := n.SetString(v[2:], 16); ok {
				return n, nil
			}
			return nil, fmt.Errorf("hex invalido")
		}
		n := new(big.Int)
		if _, ok := n.SetString(v, 10); ok {
			return n, nil
		}
		return nil, fmt.Errorf("numero invalido")
	default:
		return nil, fmt.Errorf("tipo %T nao suportado", value)
	}
}

func encodeAddress(value string) []byte {
	out := make([]byte, 32)
	addr := common.HexToAddress(value)
	copy(out[12:], addr.Bytes())
	return out
}

func encodeUint(value *big.Int) []byte {
	out := make([]byte, 32)
	if value == nil {
		return out
	}
	return value.FillBytes(out)
}

func decodeBytes32(value string) ([]byte, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "0x")
	if value == "" {
		value = "0"
	}
	if len(value) > 64 {
		sum := crypto.Keccak256Hash([]byte(value))
		return sum.Bytes(), nil
	}
	if len(value)%2 == 1 {
		value = "0" + value
	}
	raw, err := hex.DecodeString(value)
	if err != nil {
		sum := crypto.Keccak256Hash([]byte(value))
		return sum.Bytes(), nil
	}
	out := make([]byte, 32)
	copy(out[32-len(raw):], raw)
	return out, nil
}

func decodeFixedHex(value string, size int) ([]byte, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "0x")
	raw, err := hex.DecodeString(value)
	if err != nil {
		return nil, err
	}
	if len(raw) != size {
		return nil, fmt.Errorf("esperado %d bytes, recebido %d", size, len(raw))
	}
	return raw, nil
}

func addressOrZero(value string) string {
	if common.IsHexAddress(value) {
		return strings.ToLower(common.HexToAddress(value).Hex())
	}
	return "0x0000000000000000000000000000000000000000"
}

func bytes32Hex(value string) string {
	raw, _ := decodeBytes32(value)
	return "0x" + hex.EncodeToString(raw)
}

func amountString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "0"
	}
	return value
}

func normalizeIntentType(value string) string {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "m2m", "m2mintent", "payment":
		return TypeM2MIntent
	case "mobile", "mobiletransfer", "mobiletransferintent":
		return TypeMobileTransfer
	case "capability", "capabilitypurchase", "capabilitypurchaseintent":
		return TypeCapabilityPurchase
	case "pay", "payintent":
		return TypePayIntent
	default:
		return value
	}
}

func firstString(input map[string]any, keys ...string) string {
	v := firstAny(input, keys...)
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func firstAny(input map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := input[key]; ok {
			return v
		}
	}
	return nil
}

func uint64FromAny(value any) uint64 {
	n, err := parseUint256(value)
	if err != nil || n.Sign() < 0 {
		return 0
	}
	return n.Uint64()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
