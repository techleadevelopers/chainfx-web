package bitcoin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// Provider é a interface que isola a rail BTC de qualquer API pública específica.
// Todos os dados de blockchain passam por aqui — nunca diretamente nos handlers.
type Provider interface {
	GetAddressUTXOs(ctx context.Context, address string) ([]ProviderUTXO, error)
	GetTransaction(ctx context.Context, txid string) (*ProviderTxStatus, error)
	EstimateFeeRate(ctx context.Context, targetBlocks int) (int64, error) // sat/vbyte
	BroadcastTransaction(ctx context.Context, rawTxHex string) (string, error)
	GetCurrentBlockHeight(ctx context.Context) (int64, error)
}

// MempoolProvider implementa Provider usando a API REST do mempool.space.
// Funciona com mainnet, testnet, signet e pode apontar para instância própria.
// Todas as chamadas GET usam retry com exponential backoff + jitter (3 tentativas).
type MempoolProvider struct {
	baseURL    string
	token      string
	client     *http.Client
	maxRetries int
}

// NewMempoolProvider cria um provider apontando para baseURL.
func NewMempoolProvider(cfg *Config) *MempoolProvider {
	return &MempoolProvider{
		baseURL:    strings.TrimRight(cfg.APIURL, "/"),
		token:      cfg.APIToken,
		maxRetries: 3,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// GetAddressUTXOs busca os UTXOs de um endereço.
// GET /address/{address}/utxo
func (p *MempoolProvider) GetAddressUTXOs(ctx context.Context, address string) ([]ProviderUTXO, error) {
	var utxos []ProviderUTXO
	err := p.retryGet(ctx, fmt.Sprintf("/address/%s/utxo", address), &utxos)
	return utxos, err
}

// GetTransaction busca o status de uma transação pelo txid.
// GET /tx/{txid}
func (p *MempoolProvider) GetTransaction(ctx context.Context, txid string) (*ProviderTxStatus, error) {
	var tx ProviderTxStatus
	if err := p.retryGet(ctx, fmt.Sprintf("/tx/%s", txid), &tx); err != nil {
		return nil, err
	}
	return &tx, nil
}

// EstimateFeeRate retorna a fee rate estimada em sat/vbyte para o número de blocos alvo.
// GET /v1/fees/recommended
func (p *MempoolProvider) EstimateFeeRate(ctx context.Context, targetBlocks int) (int64, error) {
	var fees ProviderFeeRecommended
	if err := p.retryGet(ctx, "/v1/fees/recommended", &fees); err != nil {
		return 0, err
	}
	switch {
	case targetBlocks <= 1:
		return fees.FastestFee, nil
	case targetBlocks <= 3:
		return fees.HalfHourFee, nil
	case targetBlocks <= 6:
		return fees.HourFee, nil
	default:
		return fees.EconomyFee, nil
	}
}

// BroadcastTransaction faz broadcast de uma transação assinada (raw hex).
// POST /tx — retorna o txid no body. Não faz retry para evitar double-broadcast.
func (p *MempoolProvider) BroadcastTransaction(ctx context.Context, rawTxHex string) (string, error) {
	url := p.baseURL + "/tx"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(rawTxHex))
	if err != nil {
		return "", fmt.Errorf("provider: erro ao criar request de broadcast: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")
	p.setAuth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("provider: erro de rede no broadcast: %w", ErrBroadcastUnknown)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	txid := strings.TrimSpace(string(body))

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("provider: broadcast retornou %d: %s", resp.StatusCode, txid)
	}
	return txid, nil
}

// GetCurrentBlockHeight retorna a altura atual do bloco da rede.
// GET /blocks/tip/height
func (p *MempoolProvider) GetCurrentBlockHeight(ctx context.Context) (int64, error) {
	url := p.baseURL + "/blocks/tip/height"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	p.setAuth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("provider: erro ao buscar block height: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("provider: block height retornou %d", resp.StatusCode)
	}

	var height int64
	if err := json.NewDecoder(resp.Body).Decode(&height); err != nil {
		return 0, fmt.Errorf("provider: erro ao decodificar block height: %w", err)
	}
	return height, nil
}

// ─── retry com exponential backoff + jitter ───────────────────────────────────

// retryGet executa uma chamada GET com até p.maxRetries tentativas usando
// exponential backoff + jitter. Erros 404 e 4xx (exceto 429) não são retriados.
func (p *MempoolProvider) retryGet(ctx context.Context, path string, dest any) error {
	var lastErr error
	for attempt := 0; attempt < p.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 200ms, 400ms, 800ms... + jitter ±50ms
			base := time.Duration(200<<uint(attempt-1)) * time.Millisecond
			jitter := time.Duration(rand.Int63n(100)-50) * time.Millisecond
			wait := base + jitter
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}

		err := p.get(ctx, path, dest)
		if err == nil {
			return nil
		}
		lastErr = err

		// Não fazer retry para erros 404 e 4xx permanentes
		if isNonRetriableProviderError(err) {
			return err
		}
	}
	return fmt.Errorf("provider: %d tentativas falharam para %s: %w", p.maxRetries, path, lastErr)
}

// isNonRetriableProviderError indica se o erro não deve ser retriado.
func isNonRetriableProviderError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "não encontrado") ||
		strings.Contains(s, "retornou 400") ||
		strings.Contains(s, "retornou 401") ||
		strings.Contains(s, "retornou 403") ||
		strings.Contains(s, "retornou 404") ||
		strings.Contains(s, "retornou 410")
}

// ─── helpers internos ─────────────────────────────────────────────────────────

func (p *MempoolProvider) get(ctx context.Context, path string, dest any) error {
	url := p.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("provider: %w", err)
	}
	p.setAuth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("provider: erro de rede em %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("provider: não encontrado %s", path)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("provider: %s retornou %d: %s", path, resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("provider: erro ao decodificar %s: %w", path, err)
	}
	return nil
}

func (p *MempoolProvider) setAuth(req *http.Request) {
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
}
