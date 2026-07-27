package settlement

import "strings"

const (
	StatusError    = "erro"
	StatusPaidFiat = "pago_fiat"
)

// PixWebhookStatus converte status do webhook PIX para status interno
func PixWebhookStatus(providerStatus string) string {
	status := strings.ToLower(strings.TrimSpace(providerStatus))

	// Considerar variações de "concluído" e "aprovado"
	if strings.Contains(status, "conclu") || strings.Contains(status, "aprov") {
		return StatusPaidFiat
	}

	// Status conhecidos de erro
	if status == "rejeitado" || status == "cancelado" || status == "expirado" {
		return StatusError
	}

	// Por padrão, qualquer outro status é erro
	return StatusError
}

// ShouldPublishBuyPaid verifica se o status deve disparar evento buy.paid
func ShouldPublishBuyPaid(status string) bool {
	return status == StatusPaidFiat
}

// IsError verifica se status é de erro
func IsError(status string) bool {
	return status == StatusError
}

// IsPaid verifica se status é de pagamento confirmado
func IsPaid(status string) bool {
	return status == StatusPaidFiat
}
