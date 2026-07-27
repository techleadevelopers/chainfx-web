package solana

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"payment-gateway/internal/httpclient"
)

type RPCClient struct {
	urls   []string
	client *http.Client
	next   uint64
}

func NewRPCClient(rawURLs string) *RPCClient {
	var urls []string
	for _, item := range strings.FieldsFunc(rawURLs, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		if item = strings.TrimSpace(item); item != "" {
			urls = append(urls, item)
		}
	}
	if len(urls) == 0 {
		return nil
	}
	return &RPCClient{urls: urls, client: httpclient.Default()}
}

func (c *RPCClient) call(ctx context.Context, method string, params any, out any) error {
	if c == nil || len(c.urls) == 0 {
		return fmt.Errorf("solana: RPC nao configurado")
	}
	var lastErr error
	start := int(atomic.AddUint64(&c.next, 1))
	for i := 0; i < len(c.urls); i++ {
		url := c.urls[(start+i)%len(c.urls)]
		if err := c.callURL(ctx, url, method, params, out); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func (c *RPCClient) callURL(ctx context.Context, url, method string, params any, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      time.Now().UnixNano(),
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ChainFX-Solana/1.0")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("solana RPC %s status %d", method, resp.StatusCode)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if envelope.Error != nil {
		return fmt.Errorf("solana RPC %s error %d: %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, out)
}

func (c *RPCClient) GetBalance(ctx context.Context, address string) (int64, error) {
	var out struct {
		Value int64 `json:"value"`
	}
	err := c.call(ctx, "getBalance", []any{address, map[string]any{"commitment": "confirmed"}}, &out)
	return out.Value, err
}

func (c *RPCClient) GetLatestBlockhash(ctx context.Context) (string, int64, error) {
	var out struct {
		Value struct {
			Blockhash            string `json:"blockhash"`
			LastValidBlockHeight int64  `json:"lastValidBlockHeight"`
		} `json:"value"`
	}
	err := c.call(ctx, "getLatestBlockhash", []any{map[string]any{"commitment": "confirmed"}}, &out)
	return out.Value.Blockhash, out.Value.LastValidBlockHeight, err
}

func (c *RPCClient) GetFeeForMessage(ctx context.Context, msg []byte) (int64, error) {
	var out struct {
		Value *int64 `json:"value"`
	}
	err := c.call(ctx, "getFeeForMessage", []any{base64.StdEncoding.EncodeToString(msg), map[string]any{"commitment": "confirmed"}}, &out)
	if err != nil {
		return 0, err
	}
	if out.Value == nil {
		return 0, fmt.Errorf("solana: getFeeForMessage sem value")
	}
	return *out.Value, nil
}

func (c *RPCClient) SendTransaction(ctx context.Context, tx []byte) (string, error) {
	var sig string
	err := c.call(ctx, "sendTransaction", []any{
		base64.StdEncoding.EncodeToString(tx),
		map[string]any{"encoding": "base64", "skipPreflight": false, "preflightCommitment": "confirmed", "maxRetries": 3},
	}, &sig)
	return sig, err
}

type SignatureInfo struct {
	Signature          string `json:"signature"`
	Slot               int64  `json:"slot"`
	Err                any    `json:"err"`
	ConfirmationStatus string `json:"confirmationStatus"`
}

func (c *RPCClient) GetSignaturesForAddress(ctx context.Context, address, before string, limit int) ([]SignatureInfo, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	cfg := map[string]any{"limit": limit, "commitment": "confirmed"}
	if strings.TrimSpace(before) != "" {
		cfg["before"] = strings.TrimSpace(before)
	}
	var out []SignatureInfo
	err := c.call(ctx, "getSignaturesForAddress", []any{address, cfg}, &out)
	return out, err
}

func (c *RPCClient) GetTransaction(ctx context.Context, signature string) (map[string]any, error) {
	var raw json.RawMessage
	err := c.call(ctx, "getTransaction", []any{signature, map[string]any{
		"encoding":                       "json",
		"commitment":                     "confirmed",
		"maxSupportedTransactionVersion": 0,
	}}, &raw)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	err = dec.Decode(&out)
	return out, err
}

func (c *RPCClient) GetSignatureStatuses(ctx context.Context, signatures []string) (map[string]string, error) {
	var out struct {
		Value []struct {
			ConfirmationStatus string `json:"confirmationStatus"`
			Err                any    `json:"err"`
		} `json:"value"`
	}
	err := c.call(ctx, "getSignatureStatuses", []any{signatures, map[string]any{"searchTransactionHistory": true}}, &out)
	if err != nil {
		return nil, err
	}
	statuses := map[string]string{}
	for i, sig := range signatures {
		status := StatusPending
		if i < len(out.Value) {
			if out.Value[i].Err != nil {
				status = StatusFailed
			} else if out.Value[i].ConfirmationStatus == "confirmed" || out.Value[i].ConfirmationStatus == "finalized" {
				status = StatusConfirmed
			}
		}
		statuses[sig] = status
	}
	return statuses, nil
}
