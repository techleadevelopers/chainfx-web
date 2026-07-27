package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"payment-gateway/internal/privacy"
)

type paymentCustomerInput struct {
	Name      string
	Email     string
	CPF       string
	Phone     string
	BirthDate string
	Address   map[string]any
}

type paymentCardInput struct {
	PaymentToken   string
	Brand          string
	Installments   int
	BillingAddress map[string]any
}

func nestedFloat(root map[string]any, keys ...string) float64 {
	var current any = root
	for _, key := range keys {
		obj, ok := current.(map[string]any)
		if !ok {
			return 0
		}
		current = obj[key]
	}
	switch value := current.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		out, _ := value.Float64()
		return out
	default:
		out, _ := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(value)), 64)
		return out
	}
}

func compactProviderBody(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if len(text) > 500 {
		return text[:500]
	}
	return text
}

func onlyDigits(value string) string {
	var builder strings.Builder
	for _, ch := range value {
		if ch >= '0' && ch <= '9' {
			builder.WriteRune(ch)
		}
	}
	return builder.String()
}

func validCPF(value string) bool {
	cpf := onlyDigits(value)
	if len(cpf) != 11 {
		return false
	}
	allEqual := true
	for i := 1; i < len(cpf); i++ {
		if cpf[i] != cpf[0] {
			allEqual = false
			break
		}
	}
	if allEqual {
		return false
	}
	digit := func(pos int) int {
		sum := 0
		weight := pos + 1
		for i := 0; i < pos; i++ {
			sum += int(cpf[i]-'0') * weight
			weight--
		}
		rest := (sum * 10) % 11
		if rest == 10 {
			return 0
		}
		return rest
	}
	return digit(9) == int(cpf[9]-'0') && digit(10) == int(cpf[10]-'0')
}

func validateEfiPixCustomer(customer paymentCustomerInput) error {
	if strings.TrimSpace(customer.Name) == "" {
		return fmt.Errorf("nome do cliente e obrigatorio para PIX Efi")
	}
	if onlyDigits(customer.CPF) == "" {
		return fmt.Errorf("cpf do cliente e obrigatorio para PIX Efi")
	}
	if !validCPF(customer.CPF) {
		return fmt.Errorf("cpf do cliente invalido")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonNilMap(values ...map[string]any) map[string]any {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func nestedString(root map[string]any, keys ...string) string {
	var current any = root
	for _, key := range keys {
		obj, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = obj[key]
	}
	if value, ok := current.(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func buildCustomerAudit(secret, cpf, phone, email, name, birthDate string, address map[string]any) map[string]any {
	customer := make(map[string]any)
	if hash := privacy.Hash(cpf, secret); hash != "" {
		customer["cpfHash"] = hash
	}
	if hash := privacy.Hash(phone, secret); hash != "" {
		customer["phoneHash"] = hash
	}
	if hash := privacy.Hash(email, secret); hash != "" {
		customer["emailHash"] = hash
	}
	if hash := privacy.Hash(name, secret); hash != "" {
		customer["nameHash"] = hash
	}
	if hash := privacy.Hash(birthDate, secret); hash != "" {
		customer["birthDateHash"] = hash
	}
	if len(address) > 0 {
		raw, _ := json.Marshal(address)
		if hash := privacy.Hash(string(raw), secret); hash != "" {
			customer["addressHash"] = hash
		}
	}
	return customer
}
