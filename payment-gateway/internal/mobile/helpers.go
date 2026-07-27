package mobile

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"payment-gateway/internal/models"
)

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func decodeJSON(r *http.Request, dest any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dest)
}

func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

func firstPositiveIntMobile(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

// sanitizeUser removes sensitive fields before sending to client.
func sanitizeUser(u *models.User) map[string]any {
	out := map[string]any{
		"id":                 u.ID,
		"email":              u.Email,
		"phone":              u.Phone,
		"full_name":          u.FullName,
		"cpf":                u.CPF,
		"birth_date":         u.BirthDate,
		"avatar_url":         u.AvatarURL,
		"wallet_address":     u.WalletAddress,
		"pix_key":            u.PixKey,
		"kyc_status":         u.KYCStatus,
		"biometry_enabled":   u.BiometryEnabled,
		"two_factor_enabled": u.TwoFactorEnabled,
		"created_at":         u.CreatedAt,
	}
	if cpf := mobileUserCPF(u); cpf != "" {
		out["cpf"] = cpf
		out["document_number"] = cpf
	}
	address := mobileUserAddress(u)
	if len(address) > 0 {
		out["address"] = address
		for key, value := range address {
			out["address_"+key] = value
		}
	}
	return out
}

func sanitizeUserForMobile(u *models.User, adminBootstrapEmail string) map[string]any {
	out := sanitizeUser(u)
	adminEmail := strings.ToLower(strings.TrimSpace(adminBootstrapEmail))
	userEmail := strings.ToLower(strings.TrimSpace(u.Email))
	out["is_developer"] = adminEmail != "" && userEmail == adminEmail
	return out
}

func mobileUserString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func mobileUserCPF(u *models.User) string {
	if u == nil {
		return ""
	}
	if cpf := onlyDigitsMobile(mobileUserString(u.CPF)); cpf != "" {
		return cpf
	}
	if u.KYCDocuments == nil {
		return ""
	}
	var docs map[string]any
	if err := json.Unmarshal([]byte(*u.KYCDocuments), &docs); err != nil {
		return ""
	}
	for _, key := range []string{"cpf", "number", "document_number", "documentNumber"} {
		if value, ok := docs[key]; ok {
			if cpf := onlyDigitsMobile(value); cpf != "" {
				return cpf
			}
		}
	}
	return ""
}

func mobileUserAddress(u *models.User) map[string]any {
	if u == nil {
		return nil
	}
	out := map[string]any{}
	add := func(key string, value *string) {
		if trimmed := mobileUserString(value); trimmed != "" {
			out[key] = trimmed
		}
	}
	add("postal_code", u.AddressPostalCode)
	add("street", u.AddressStreet)
	add("number", u.AddressNumber)
	add("neighborhood", u.AddressNeighborhood)
	add("city", u.AddressCity)
	add("state", u.AddressState)
	add("country", u.AddressCountry)
	if len(out) == 0 {
		return nil
	}
	return out
}

func mobileUserKYCApproved(u *models.User) bool {
	if u == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(string(u.KYCStatus))) {
	case "approved", "verified", "verificado":
		return true
	default:
		return false
	}
}

func onlyDigitsMobile(value any) string {
	raw := strings.TrimSpace(valueToString(value))
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func valueToString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}
