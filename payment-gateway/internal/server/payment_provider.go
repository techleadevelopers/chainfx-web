package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/certutil"
)

func (s *Server) createPaymentIntent(ctx context.Context, buyID string, amountFiat float64, fiatCurrency, paymentMethod, asset, network string, customer paymentCustomerInput, card paymentCardInput) (map[string]any, error) {
	if paymentMethod == "credit_card" {
		return s.createEfiCreditCardCharge(ctx, buyID, amountFiat, fiatCurrency, customer, card)
	}
	if paymentMethod != "pix" {
		return nil, fmt.Errorf("rail de pagamento nao suportado")
	}
	if !s.efiPixConfigured() {
		if s.cfg.AllowSimulations && !s.cfg.IsProduction() {
			return map[string]any{"provider": "efi", "mode": "simulation", "pixKey": "chavepix@nexswap.com", "qrCodeUrl": "/images/qrcode.png", "buyId": buyID}, nil
		}
		return nil, fmt.Errorf("credenciais EfÃ­ Pix nao configuradas")
	}
	return s.createEfiPixCharge(ctx, buyID, amountFiat, asset, network, customer)
}

func (s *Server) efiChargesConfigured() bool {
	return strings.TrimSpace(s.cfg.EfiClientID) != "" &&
		strings.TrimSpace(s.cfg.EfiClientSecret) != "" &&
		strings.TrimSpace(s.cfg.EfiChargesBaseURL) != "" &&
		(strings.TrimSpace(s.cfg.EfiCertificatePath) != "" || strings.Trim(strings.TrimSpace(s.cfg.EfiCertificateP12), `"'`) != "")
}

func (s *Server) createEfiCreditCardCharge(ctx context.Context, buyID string, amountFiat float64, fiatCurrency string, customer paymentCustomerInput, card paymentCardInput) (map[string]any, error) {
	if fiatCurrency != "BRL" {
		return nil, fmt.Errorf("cartao Efí suporta apenas BRL neste fluxo")
	}
	if !s.efiChargesConfigured() {
		return nil, fmt.Errorf("credenciais Efí Cobranças nao configuradas")
	}
	card.PaymentToken = strings.TrimSpace(card.PaymentToken)
	card.Brand = normalizeEfiCardBrand(card.Brand)
	if card.PaymentToken == "" {
		return nil, fmt.Errorf("paymentToken Efí obrigatorio para cartao")
	}
	if !efiCardBrandSupported(card.Brand) {
		return nil, fmt.Errorf("bandeira de cartao nao suportada")
	}
	if card.Installments <= 0 {
		card.Installments = 1
	}
	creditCard, err := buildEfiCreditCardPayment(customer, card)
	if err != nil {
		return nil, err
	}
	client, err := s.efiHTTPClient()
	if err != nil {
		return nil, err
	}
	token, err := s.efiBillingAccessToken(ctx, client)
	if err != nil {
		return nil, err
	}
	amountCents := int64(math.Round(amountFiat * 100))
	createPayload := map[string]any{
		"items": []map[string]any{{
			"name":   "USDT Purchase",
			"value":  amountCents,
			"amount": 1,
		}},
		"metadata": map[string]any{
			"custom_id":        buyID,
			"notification_url": strings.TrimRight(s.cfg.EmailSiteURL, "/") + "/api/efi/charges/webhook/buy",
		},
	}
	charge, err := s.efiBillingRequestWithAuthRetry(ctx, client, token, http.MethodPost, "/v1/charge", createPayload)
	if err != nil {
		return nil, err
	}
	chargeID := mapString(nestedMap(charge, "data"), "charge_id")
	if chargeID == "" {
		chargeID = fmt.Sprint(nestedFloat(charge, "data", "charge_id"))
		chargeID = strings.TrimSuffix(strings.TrimSuffix(chargeID, "0"), ".")
	}
	if chargeID == "" {
		return nil, fmt.Errorf("Efí Cobranças nao retornou charge_id")
	}
	payPayload := map[string]any{
		"payment": map[string]any{
			"credit_card": creditCard,
		},
	}
	payment, err := s.efiBillingRequestWithAuthRetry(ctx, client, token, http.MethodPost, "/v1/charge/"+chargeID+"/pay", payPayload)
	if err != nil {
		return nil, err
	}
	data := nestedMap(payment, "data")
	status := mapString(data, "status")
	if status == "" {
		status = mapString(payment, "status")
	}
	out := map[string]any{
		"provider":          "efi",
		"paymentMethod":     "credit_card",
		"buyId":             buyID,
		"chargeId":          chargeID,
		"providerPaymentId": chargeID,
		"providerStatus":    status,
		"cardBrand":         card.Brand,
		"installments":      card.Installments,
		"amount":            amountFiat,
		"currency":          fiatCurrency,
		"settlementPolicy":  "release_crypto_only_after_efi_paid_notification",
		"rawStatus":         data,
	}
	if customer.Name != "" || customer.Email != "" || customer.CPF != "" || customer.Phone != "" || customer.BirthDate != "" || len(customer.Address) > 0 {
		out["customerSubmitted"] = buildCustomerAudit(s.cfg.LGPDSecret, customer.CPF, customer.Phone, customer.Email, customer.Name, customer.BirthDate, customer.Address)
	}
	return out, nil
}

func (s *Server) efiPixConfigured() bool {
	return strings.TrimSpace(s.cfg.EfiClientID) != "" &&
		strings.TrimSpace(s.cfg.EfiClientSecret) != "" &&
		strings.TrimSpace(s.cfg.EfiPixKey) != "" &&
		(strings.TrimSpace(s.cfg.EfiCertificatePath) != "" || strings.Trim(strings.TrimSpace(s.cfg.EfiCertificateP12), `"'`) != "")
}

func (s *Server) efiCertificateSource() string {
	if strings.Trim(strings.TrimSpace(s.cfg.EfiCertificateP12), `"'`) != "" {
		return "base64"
	}
	if strings.TrimSpace(s.cfg.EfiCertificatePath) != "" {
		return "file"
	}
	return "missing"
}

func (s *Server) efiCertificateReady() (bool, string) {
	certPath := strings.TrimSpace(s.cfg.EfiCertificatePath)
	certKey := strings.TrimSpace(s.cfg.EfiCertificateKey)
	p12 := strings.Trim(strings.TrimSpace(s.cfg.EfiCertificateP12), `"'`)
	if certPath == "" && p12 == "" {
		return false, "certificado Efi nao configurado"
	}
	source := certPath + "\x00" + certKey + "\x00" + p12 + "\x00" + s.cfg.EfiCertificatePass
	now := time.Now()

	s.certReadyMu.Lock()
	if source == s.certReadySource && now.Sub(s.certReadyChecked) < 30*time.Second {
		ok, errText := s.certReadyOK, s.certReadyErr
		s.certReadyMu.Unlock()
		return ok, errText
	}
	s.certReadyMu.Unlock()

	ok, errText := true, ""
	if _, err := s.loadEfiCertificate(certPath, certKey); err != nil {
		ok, errText = false, err.Error()
	}

	s.certReadyMu.Lock()
	s.certReadySource = source
	s.certReadyChecked = now
	s.certReadyOK = ok
	s.certReadyErr = errText
	s.certReadyMu.Unlock()

	return ok, errText
}

func (s *Server) createEfiPixCharge(ctx context.Context, buyID string, amountFiat float64, asset, network string, customer paymentCustomerInput) (map[string]any, error) {
	client, err := s.efiHTTPClient()
	if err != nil {
		return nil, err
	}
	token, err := s.efiAccessToken(ctx, client)
	if err != nil {
		return nil, err
	}
	txid := efiTxIDFromBuyID(buyID)
	expirySec := s.cfg.PixChargeExpirySec
	if expirySec <= 0 {
		expirySec = 1800
	}
	asset = strings.ToUpper(strings.TrimSpace(defaultString(asset, "USDT")))
	network = strings.ToUpper(strings.TrimSpace(defaultString(network, "BSC")))
	payload := map[string]any{
		"calendario": map[string]any{"expiracao": expirySec},
		"valor":      map[string]any{"original": fmt.Sprintf("%.2f", amountFiat)},
		"chave":      s.cfg.EfiPixKey,
		"solicitacaoPagador": fmt.Sprintf(
			"Pedido %s - %s %s",
			buyID, asset, network,
		),
	}
	if debtor := buildEfiDebtor(customer); len(debtor) > 0 {
		payload["devedor"] = debtor
	}

	raw, _ := json.Marshal(payload)
	endpoint := strings.TrimRight(s.cfg.EfiApiBaseURL, "/") + "/v2/cob/" + txid
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("EfÃ­ rejeitou cobranca PIX: status %d body %s", resp.StatusCode, compactProviderBody(body))
	}
	var provider map[string]any
	if err := json.Unmarshal(body, &provider); err != nil {
		return nil, fmt.Errorf("EfÃ­ respondeu JSON invalido")
	}
	provider["provider"] = "efi"
	provider["buyId"] = buyID
	provider["txid"] = defaultString(fmt.Sprint(provider["txid"]), txid)
	provider["providerPaymentId"] = txid
	provider["providerStatus"] = resp.StatusCode
	provider["pixKey"] = s.cfg.EfiPixKey
	if qr, err := s.efiQRCode(ctx, client, token, provider); err == nil {
		for key, value := range qr {
			provider[key] = value
		}
	} else {
		return nil, err
	}
	if mapString(provider, "qrCodeUrl") == "" || mapString(provider, "pixCopiaECola") == "" {
		return nil, fmt.Errorf("EfÃ­ nao retornou QR Code Pix completo")
	}
	if feeBps := s.cfg.EfiPixFeeBps; feeBps > 0 {
		provider["providerFeeBps"] = feeBps
		provider["providerFeeFiatEstimated"] = math.Round((amountFiat*float64(feeBps)/10000)*100) / 100
	}
	if customer.Name != "" || customer.Email != "" || customer.CPF != "" || customer.Phone != "" || customer.BirthDate != "" || len(customer.Address) > 0 {
		provider["customerSubmitted"] = buildCustomerAudit(s.cfg.LGPDSecret, customer.CPF, customer.Phone, customer.Email, customer.Name, customer.BirthDate, customer.Address)
	}
	return provider, nil
}

func (s *Server) efiQRCode(ctx context.Context, client *http.Client, token string, charge map[string]any) (map[string]any, error) {
	locID := nestedFloat(charge, "loc", "id")
	if locID <= 0 {
		return nil, fmt.Errorf("cobranca EfÃ­ sem loc.id")
	}
	endpoint := fmt.Sprintf("%s/v2/loc/%d/qrcode", strings.TrimRight(s.cfg.EfiApiBaseURL, "/"), int64(locID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("EfÃ­ rejeitou QR Code Pix: status %d body %s", resp.StatusCode, compactProviderBody(body))
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return map[string]any{
		"pixCopiaECola":     mapString(data, "qrcode"),
		"qrCodeUrl":         mapString(data, "imagemQrcode"),
		"linkVisualizacao":  mapString(data, "linkVisualizacao"),
		"qrCodeProviderRaw": data,
	}, nil
}

func (s *Server) efiHTTPClient() (*http.Client, error) {
	certPath := strings.TrimSpace(s.cfg.EfiCertificatePath)
	keyPath := strings.TrimSpace(s.cfg.EfiCertificateKey)
	p12 := strings.Trim(strings.TrimSpace(s.cfg.EfiCertificateP12), `"'`)
	source := certPath + "\x00" + keyPath + "\x00" + p12 + "\x00" + s.cfg.EfiCertificatePass

	s.efiMu.Lock()
	if s.efiClient != nil && s.efiClientSource == source {
		client := s.efiClient
		s.efiMu.Unlock()
		return client, nil
	}
	s.efiMu.Unlock()

	cert, err := s.loadEfiCertificate(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}, MaxIdleConns: 100, MaxIdleConnsPerHost: 20, IdleConnTimeout: 90 * time.Second},
	}
	s.efiMu.Lock()
	s.efiClient = client
	s.efiClientSource = source
	s.efiMu.Unlock()
	return client, nil
}

// loadEfiCertificate delegates to the shared certutil loader (used by both
// this direct-charge HTTP client and the PSP EfiAdapter wired in cmd/api/main.go,
// so PKCS#12/PEM decoding logic lives in exactly one place).
func (s *Server) loadEfiCertificate(certPath, keyPath string) (tls.Certificate, error) {
	return certutil.LoadCertificate(certPath, keyPath, s.cfg.EfiCertificateP12, s.cfg.EfiCertificatePass)
}

func (s *Server) efiAccessToken(ctx context.Context, client *http.Client) (string, error) {
	if token, wait := s.beginEfiTokenRefresh(false); token != "" || wait != nil {
		if token == "" {
			select {
			case <-wait:
				return s.efiAccessToken(ctx, client)
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return token, nil
	}
	defer s.finishEfiTokenRefresh(false)
	raw := []byte(`{"grant_type":"client_credentials"}`)
	endpoint := strings.TrimRight(s.cfg.EfiApiBaseURL, "/") + "/oauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(s.cfg.EfiClientID, s.cfg.EfiClientSecret)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("EfÃ­ OAuth rejeitou credenciais: status %d", resp.StatusCode)
	}
	var data struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &data); err != nil || strings.TrimSpace(data.AccessToken) == "" {
		return "", fmt.Errorf("EfÃ­ OAuth respondeu token inválido")
	}
	s.storeEfiToken(false, data.AccessToken)
	return data.AccessToken, nil
}

func (s *Server) efiBillingAccessToken(ctx context.Context, client *http.Client) (string, error) {
	if token, wait := s.beginEfiTokenRefresh(true); token != "" || wait != nil {
		if token == "" {
			select {
			case <-wait:
				return s.efiBillingAccessToken(ctx, client)
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return token, nil
	}
	defer s.finishEfiTokenRefresh(true)
	raw := []byte(`{"grant_type":"client_credentials"}`)
	endpoint := strings.TrimRight(s.cfg.EfiChargesBaseURL, "/") + "/v1/authorize"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(s.cfg.EfiClientID, s.cfg.EfiClientSecret)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Efí Cobranças OAuth rejeitou credenciais: status %d body %s", resp.StatusCode, compactProviderBody(body))
	}
	var data struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &data); err != nil || strings.TrimSpace(data.AccessToken) == "" {
		return "", fmt.Errorf("Efí Cobranças OAuth respondeu token invalido")
	}
	s.storeEfiToken(true, data.AccessToken)
	return data.AccessToken, nil
}

func (s *Server) beginEfiTokenRefresh(billing bool) (string, <-chan struct{}) {
	s.efiMu.Lock()
	defer s.efiMu.Unlock()
	now := time.Now().UTC().Add(30 * time.Second)
	if billing {
		if s.efiBillAccessToken != "" && now.Before(s.efiBillAccessTokenExp) {
			return s.efiBillAccessToken, nil
		}
		if s.efiBillRefreshing {
			return "", s.efiBillRefreshDone
		}
		s.efiBillRefreshing = true
		s.efiBillRefreshDone = make(chan struct{})
		return "", nil
	}
	if s.efiPixAccessToken != "" && now.Before(s.efiPixAccessTokenExp) {
		return s.efiPixAccessToken, nil
	}
	if s.efiPixRefreshing {
		return "", s.efiPixRefreshDone
	}
	s.efiPixRefreshing = true
	s.efiPixRefreshDone = make(chan struct{})
	return "", nil
}

func (s *Server) finishEfiTokenRefresh(billing bool) {
	s.efiMu.Lock()
	defer s.efiMu.Unlock()
	if billing {
		if s.efiBillRefreshDone != nil {
			close(s.efiBillRefreshDone)
		}
		s.efiBillRefreshing = false
		s.efiBillRefreshDone = nil
		return
	}
	if s.efiPixRefreshDone != nil {
		close(s.efiPixRefreshDone)
	}
	s.efiPixRefreshing = false
	s.efiPixRefreshDone = nil
}

func (s *Server) invalidateEfiToken(billing bool) {
	s.efiMu.Lock()
	defer s.efiMu.Unlock()
	if billing {
		s.efiBillAccessToken = ""
		s.efiBillAccessTokenExp = time.Time{}
		return
	}
	s.efiPixAccessToken = ""
	s.efiPixAccessTokenExp = time.Time{}
}

func (s *Server) storeEfiToken(billing bool, token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	exp := time.Now().UTC().Add(45 * time.Minute)
	s.efiMu.Lock()
	defer s.efiMu.Unlock()
	if billing {
		s.efiBillAccessToken = token
		s.efiBillAccessTokenExp = exp
		return
	}
	s.efiPixAccessToken = token
	s.efiPixAccessTokenExp = exp
}

func (s *Server) efiBillingRequest(ctx context.Context, client *http.Client, token, method, path string, payload any) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
	}
	endpoint := strings.TrimRight(s.cfg.EfiChargesBaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Efí Cobranças rejeitou %s %s: status %d body %s", method, path, resp.StatusCode, compactProviderBody(raw))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("Efí Cobranças respondeu JSON invalido")
	}
	return out, nil
}

func (s *Server) efiBillingRequestWithAuthRetry(ctx context.Context, client *http.Client, token, method, path string, payload any) (map[string]any, error) {
	out, err := s.efiBillingRequest(ctx, client, token, method, path, payload)
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		return out, err
	}
	s.invalidateEfiToken(true)
	freshToken, refreshErr := s.efiBillingAccessToken(ctx, client)
	if refreshErr != nil {
		return nil, refreshErr
	}
	return s.efiBillingRequest(ctx, client, freshToken, method, path, payload)
}

func buildEfiDebtor(customer paymentCustomerInput) map[string]any {
	cpf := onlyDigits(customer.CPF)
	name := strings.TrimSpace(customer.Name)
	if cpf == "" || name == "" || !validCPF(cpf) {
		return nil
	}
	return map[string]any{
		"cpf":  cpf,
		"nome": name,
	}
}

func buildEfiCreditCardPayment(customer paymentCustomerInput, card paymentCardInput) (map[string]any, error) {
	cpf := onlyDigits(customer.CPF)
	phone := onlyDigits(customer.Phone)
	name := strings.TrimSpace(customer.Name)
	email := strings.TrimSpace(customer.Email)
	if name == "" || cpf == "" || email == "" || phone == "" {
		return nil, fmt.Errorf("nome, cpf, email e telefone sao obrigatorios para cartao Efí")
	}
	creditCard := map[string]any{
		"customer": map[string]any{
			"name":         name,
			"cpf":          cpf,
			"email":        email,
			"phone_number": phone,
		},
		"installments":    card.Installments,
		"payment_token":   card.PaymentToken,
		"message":         "ChainFX USDT Purchase",
		"billing_address": buildEfiBillingAddress(card.BillingAddress),
	}
	if birth := strings.TrimSpace(customer.BirthDate); birth != "" {
		creditCard["customer"].(map[string]any)["birth"] = birth
	}
	if len(creditCard["billing_address"].(map[string]any)) == 0 {
		delete(creditCard, "billing_address")
	}
	return creditCard, nil
}

func buildEfiBillingAddress(address map[string]any) map[string]any {
	out := make(map[string]any)
	copyAddressField(out, address, "street", "street", "logradouro", "addressLine1")
	copyAddressField(out, address, "number", "number", "numero")
	copyAddressField(out, address, "neighborhood", "neighborhood", "bairro")
	copyAddressField(out, address, "zipcode", "zipcode", "zipCode", "postalCode", "cep")
	copyAddressField(out, address, "city", "city", "cidade")
	copyAddressField(out, address, "state", "state", "uf", "province")
	copyAddressField(out, address, "complement", "complement", "complemento", "addressLine2")
	if zip := mapString(out, "zipcode"); zip != "" {
		out["zipcode"] = onlyDigits(zip)
	}
	return out
}

func normalizeEfiCardBrand(brand string) string {
	switch strings.ToLower(strings.TrimSpace(brand)) {
	case "master", "mastercard":
		return "mastercard"
	case "americanexpress", "american_express", "amex":
		return "amex"
	default:
		return strings.ToLower(strings.TrimSpace(brand))
	}
}

func efiCardBrandSupported(brand string) bool {
	switch brand {
	case "visa", "mastercard", "amex", "elo":
		return true
	default:
		return false
	}
}

func efiTxIDFromBuyID(buyID string) string {
	clean := onlyAlphaNum(buyID)
	if len(clean) >= 26 && len(clean) <= 35 {
		return clean
	}
	if len(clean) > 35 {
		return clean[:35]
	}
	return strings.ToUpper(clean + strings.Repeat("0", 26-len(clean)))
}

func buyIDFromEfiTxID(txid string) string {
	clean := onlyAlphaNum(txid)
	if len(clean) == 32 {
		return fmt.Sprintf("%s-%s-%s-%s-%s", clean[0:8], clean[8:12], clean[12:16], clean[16:20], clean[20:32])
	}
	return txid
}

func onlyAlphaNum(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - ('a' - 'A'))
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func copyAddressField(out, in map[string]any, target string, keys ...string) {
	if value := firstMapString(in, keys...); value != "" {
		out[target] = value
	}
}

func firstMapString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := values[key]; ok {
			if text := strings.TrimSpace(fmt.Sprint(raw)); text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func mapString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	if raw, ok := values[key]; ok {
		if text := strings.TrimSpace(fmt.Sprint(raw)); text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}

func nestedMap(root map[string]any, keys ...string) map[string]any {
	var current any = root
	for _, key := range keys {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = obj[key]
	}
	out, _ := current.(map[string]any)
	return out
}
