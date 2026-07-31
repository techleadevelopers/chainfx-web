//go:build ignore

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/pkcs12"
)

const sandboxBaseURL = "https://pix-h.api.efipay.com.br"

type sandboxConfig struct {
	ClientID        string
	ClientSecret    string
	APIBaseURL      string
	CertificatePath string
	CertificateKey  string
	PayerPixKey     string
}

func main() {
	mode := flag.String("mode", "auth", "auth|success|rejection|idempotency|reconcile")
	idEnvio := flag.String("id-envio", "chainfx-sandbox-"+time.Now().UTC().Format("20060102150405"), "stable idEnvio for send/reconcile")
	flag.Parse()

	cfg, err := loadSandboxConfig(".env")
	if err != nil {
		fatalFlag("CONFIG_OK", err)
	}
	printEnvironmentProof(cfg)
	if strings.TrimRight(cfg.APIBaseURL, "/") != sandboxBaseURL {
		fmt.Println("EFI_SANDBOX_HOST_PROVEN=NO")
		fmt.Println("ABORT=YES")
		os.Exit(2)
	}
	fmt.Println("EFI_SANDBOX_HOST_PROVEN=YES")

	client, certOK, err := sandboxHTTPClient(cfg)
	if err != nil {
		fmt.Println("EFI_SANDBOX_CERTIFICATE_VALID=NO")
		fmt.Println("CERTIFICATE_ENV_MISMATCH=YES")
		fmt.Printf("CERTIFICATE_ERROR=%s\n", sanitizeError(err))
		fmt.Println("ABORT_BEFORE_PAYOUT=YES")
		os.Exit(3)
	}
	fmt.Printf("EFI_SANDBOX_CERTIFICATE_VALID=%s\n", yesNo(certOK))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	token, scope, err := oauth(ctx, client, cfg)
	if err != nil {
		fmt.Println("EFI_SANDBOX_AUTH_OK=NO")
		fmt.Println("EFI_PIX_SEND_SCOPE_OK=NO")
		fmt.Println("CERTIFICATE_ENV_MISMATCH=YES")
		fmt.Printf("AUTH_ERROR=%s\n", sanitizeError(err))
		fmt.Println("ABORT_BEFORE_PAYOUT=YES")
		os.Exit(4)
	}
	fmt.Println("EFI_SANDBOX_AUTH_OK=YES")
	fmt.Printf("EFI_PIX_SEND_SCOPE_OK=%s\n", yesNo(scopeHas(scope, "pix.send")))
	if !scopeHas(scope, "pix.send") {
		fmt.Println("ABORT_BEFORE_PAYOUT=YES")
		os.Exit(5)
	}

	switch *mode {
	case "auth":
		return
	case "success":
		send(ctx, client, cfg, token, *idEnvio, "1.00")
	case "rejection":
		send(ctx, client, cfg, token, *idEnvio, "11.00")
	case "idempotency":
		send(ctx, client, cfg, token, *idEnvio, "1.00")
		send(ctx, client, cfg, token, *idEnvio, "1.00")
	case "reconcile":
		reconcile(ctx, client, cfg, token, *idEnvio)
	default:
		fmt.Printf("UNKNOWN_MODE=%s\n", *mode)
		os.Exit(64)
	}
}

func loadSandboxConfig(path string) (sandboxConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return sandboxConfig{}, err
	}
	values := map[string]string{}
	inHomolog := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(strings.ToLower(trimmed), "homologacao") || strings.Contains(strings.ToLower(trimmed), "homologa") {
			inHomolog = true
			continue
		}
		if !inHomolog {
			continue
		}
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		if !strings.Contains(trimmed, "=") {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		key := strings.ToUpper(strings.TrimSpace(parts[0]))
		value := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		switch key {
		case "EFI_CLIENT_ID", "EFI_CLIENT_SECRET", "EFI_API_BASE_URL", "EFI_CERTIFICATE_PATH", "EFI_CERTIFICATE_KEY_PATH":
			values[key] = value
		}
	}
	cfg := sandboxConfig{
		ClientID:        values["EFI_CLIENT_ID"],
		ClientSecret:    values["EFI_CLIENT_SECRET"],
		APIBaseURL:      values["EFI_API_BASE_URL"],
		CertificatePath: values["EFI_CERTIFICATE_PATH"],
		CertificateKey:  values["EFI_CERTIFICATE_KEY_PATH"],
		PayerPixKey:     readActiveEnvValue(string(raw), "EFI_PIX_KEY"),
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = sandboxBaseURL
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.CertificatePath == "" {
		return cfg, fmt.Errorf("homologacao efi incompleta em .env")
	}
	if cfg.PayerPixKey == "" {
		return cfg, fmt.Errorf("EFI_PIX_KEY ativo ausente; necessario para pagador.chave nos testes PUT")
	}
	return cfg, nil
}

func readActiveEnvValue(raw, key string) string {
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, "=") {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if strings.EqualFold(strings.TrimSpace(parts[0]), key) {
			return strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		}
	}
	return ""
}

func printEnvironmentProof(cfg sandboxConfig) {
	fmt.Printf("EFI_API_BASE_URL=%s\n", cfg.APIBaseURL)
	fmt.Println("EFI_ENVIRONMENT=HOMOLOGACAO")
	if _, err := os.Stat(cfg.CertificatePath); err == nil {
		fmt.Println("certificate file exists YES")
	} else {
		fmt.Println("certificate file exists NO")
	}
}

func sandboxHTTPClient(cfg sandboxConfig) (*http.Client, bool, error) {
	certPath := filepath.Clean(cfg.CertificatePath)
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return nil, false, err
	}
	if strings.HasSuffix(strings.ToLower(certPath), ".b64") {
		raw, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, false, fmt.Errorf("decode p12 base64: %w", err)
		}
	}
	privateKey, cert, err := pkcs12.Decode(raw, "")
	if err != nil {
		return nil, false, fmt.Errorf("decode p12: %w", err)
	}
	tlsCert := tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: privateKey, Leaf: cert}
	pool, _ := x509.SystemCertPool()
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:   tls.VersionTLS12,
				Certificates: []tls.Certificate{tlsCert},
				RootCAs:      pool,
			},
		},
		Timeout: 30 * time.Second,
	}, true, nil
}

func oauth(ctx context.Context, client *http.Client, cfg sandboxConfig) (string, string, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sandboxBaseURL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.SetBasicAuth(cfg.ClientID, cfg.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("oauth status %d body %s", resp.StatusCode, compact(raw))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", err
	}
	if out.AccessToken == "" {
		return "", out.Scope, fmt.Errorf("access_token vazio")
	}
	return out.AccessToken, out.Scope, nil
}

func send(ctx context.Context, client *http.Client, cfg sandboxConfig, token, idEnvio, value string) {
	payload := map[string]any{
		"valor": value,
		"pagador": map[string]any{
			"chave":       cfg.PayerPixKey,
			"infoPagador": "ChainFX sandbox payout test",
		},
		"favorecido": map[string]any{
			"chave": "efipay@sejaefi.com.br",
		},
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sandboxBaseURL+"/v3/gn/pix/"+idEnvio, bytes.NewReader(raw))
	if err != nil {
		fatalFlag("SANDBOX_SEND_REQUEST_OK", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fatalFlag("SANDBOX_SEND_REQUEST_OK", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	result := parseProviderResult(body)
	fmt.Printf("SANDBOX_PUT_HTTP_STATUS=%d\n", resp.StatusCode)
	fmt.Printf("SANDBOX_PROVIDER_IDENVIO_MATCH=%s\n", yesNo(result.IDEnvio == "" || result.IDEnvio == idEnvio))
	fmt.Printf("SANDBOX_PROVIDER_STATUS=%s\n", firstNonEmpty(result.Status, "UNKNOWN"))
	fmt.Printf("SANDBOX_PROVIDER_REFERENCE_PRESENT=%s\n", yesNo(result.E2EID != "" || result.EndToEndID != ""))
	if resp.StatusCode >= 400 {
		fmt.Printf("SANDBOX_PROVIDER_ERROR=%s\n", compact(body))
	}
}

func reconcile(ctx context.Context, client *http.Client, cfg sandboxConfig, token, idEnvio string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.APIBaseURL, "/")+"/v2/gn/pix/enviados/id-envio/"+idEnvio, nil)
	if err != nil {
		fatalFlag("SANDBOX_RECONCILE_REQUEST_OK", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		fatalFlag("SANDBOX_RECONCILE_REQUEST_OK", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	result := parseProviderResult(body)
	fmt.Printf("SANDBOX_RECONCILE_HTTP_STATUS=%d\n", resp.StatusCode)
	fmt.Printf("SANDBOX_RECONCILE_IDENVIO_MATCH=%s\n", yesNo(result.IDEnvio == "" || result.IDEnvio == idEnvio))
	fmt.Printf("SANDBOX_RECONCILE_PROVIDER_STATUS=%s\n", firstNonEmpty(result.Status, "UNKNOWN"))
	fmt.Printf("SANDBOX_RECONCILE_REFERENCE_PRESENT=%s\n", yesNo(result.E2EID != "" || result.EndToEndID != ""))
	if resp.StatusCode >= 400 {
		fmt.Printf("SANDBOX_RECONCILE_ERROR=%s\n", compact(body))
	}
}

type providerResult struct {
	IDEnvio     string `json:"idEnvio"`
	E2EID       string `json:"e2eId"`
	EndToEndID  string `json:"endToEndId"`
	Status      string `json:"status"`
	Nome        string `json:"nome"`
	Mensagem    string `json:"mensagem"`
	Title       string `json:"title"`
	Detail      string `json:"detail"`
	Description string `json:"description"`
}

func parseProviderResult(raw []byte) providerResult {
	var out providerResult
	_ = json.Unmarshal(raw, &out)
	return out
}

func scopeHas(scope, needle string) bool {
	for _, item := range strings.Fields(scope) {
		if item == needle {
			return true
		}
	}
	return strings.Contains(scope, needle)
}

func sanitizeError(err error) string {
	return compact([]byte(err.Error()))
}

func compact(raw []byte) string {
	value := strings.Join(strings.Fields(string(raw)), " ")
	if len(value) > 300 {
		return value[:300]
	}
	return value
}

func yesNo(ok bool) string {
	if ok {
		return "YES"
	}
	return "NO"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func fatalFlag(flag string, err error) {
	fmt.Printf("%s=NO\n", flag)
	fmt.Printf("ERROR=%s\n", sanitizeError(err))
	os.Exit(1)
}
