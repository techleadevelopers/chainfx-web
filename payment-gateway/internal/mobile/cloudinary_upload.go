package mobile

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxMobileImageUploadBytes = 12 << 20
	maxMobileVideoUploadBytes = 60 << 20
)

type cloudinaryConfig struct {
	CloudName string
	APIKey    string
	APISecret string
}

type cloudinaryUploadResult struct {
	SecureURL    string `json:"secure_url"`
	PublicID     string `json:"public_id"`
	ResourceType string `json:"resource_type"`
	Format       string `json:"format"`
	Bytes        int64  `json:"bytes"`
}

func loadCloudinaryConfig() (*cloudinaryConfig, error) {
	cfg := &cloudinaryConfig{
		APIKey:    strings.TrimSpace(os.Getenv("CLOUDINARY_API_KEY")),
		APISecret: strings.TrimSpace(os.Getenv("CLOUDINARY_API_SECRET")),
	}

	rawURL := strings.Trim(strings.TrimSpace(os.Getenv("CLOUDINARY_URL")), `"`)
	if rawURL != "" {
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, fmt.Errorf("CLOUDINARY_URL invalida: %w", err)
		}
		cfg.CloudName = strings.TrimSpace(u.Host)
		if u.User != nil {
			if key := u.User.Username(); key != "" {
				cfg.APIKey = key
			}
			if secret, ok := u.User.Password(); ok && secret != "" {
				cfg.APISecret = secret
			}
		}
	}

	if cfg.CloudName == "" {
		cfg.CloudName = strings.TrimSpace(os.Getenv("CLOUDINARY_CLOUD_NAME"))
	}
	if cfg.CloudName == "" || cfg.APIKey == "" || cfg.APISecret == "" {
		return nil, errors.New("Cloudinary nao configurado: defina CLOUDINARY_URL ou CLOUDINARY_CLOUD_NAME/CLOUDINARY_API_KEY/CLOUDINARY_API_SECRET")
	}
	return cfg, nil
}

func (s *Server) handleUploadAvatar(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	upload, _, err := uploadMobileMultipartMedia(w, r, uid, "avatar")
	if err != nil {
		writeUploadError(w, err)
		return
	}

	db := mobileDB(s.db)
	if err := db.ensureMobileMediaSchema(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao preparar schema de midia"})
		return
	}
	if err := db.UpdateUser(r.Context(), uid, map[string]any{"avatar_url": upload.SecureURL}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao salvar avatar"})
		return
	}
	user, _ := db.GetUserByID(r.Context(), uid)
	var safeUser any
	if user != nil {
		safeUser = s.sanitizeUser(user)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"avatar_url": upload.SecureURL,
		"upload":     upload,
		"user":       safeUser,
	})
}

func (s *Server) handleUploadKYCMedia(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	upload, normalizedKind, err := uploadMobileMultipartMedia(w, r, uid, kind)
	if err != nil {
		writeUploadError(w, err)
		return
	}
	response := map[string]any{
		"kind":   normalizedKind,
		"url":    upload.SecureURL,
		"upload": upload,
	}
	if normalizedKind == "document_back" {
		if extracted, err := s.extractMobileKYCDocumentFields(r.Context(), upload.SecureURL); err == nil && len(extracted) > 0 {
			response["extracted"] = extracted
			response["ocr_status"] = "read"
		} else if err != nil {
			response["ocr_status"] = "unreadable"
			response["ocr_error"] = err.Error()
		} else {
			response["ocr_status"] = "unavailable"
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) extractMobileKYCDocumentFields(ctx context.Context, fileURL string) (map[string]any, error) {
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.CapabilityOCRURL) == "" {
		return nil, nil
	}
	form := url.Values{}
	form.Set("apikey", strings.TrimSpace(s.cfg.CapabilityOCRAPIKey))
	form.Set("url", fileURL)
	form.Set("language", "por")
	form.Set("isOverlayRequired", "false")
	form.Set("detectOrientation", "true")
	form.Set("scale", "true")
	form.Set("OCREngine", "2")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.CapabilityOCRURL, "/"), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if apiKey := strings.TrimSpace(s.cfg.CapabilityOCRAPIKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("apikey", apiKey)
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("X-API-Key", apiKey)
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OCR retornou status %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if parsed := extractMobileKYCOCRFields(out); len(parsed) > 0 {
		out["full_name"] = parsed["full_name"]
		out["cpf"] = parsed["cpf"]
		out["birth_date"] = parsed["birth_date"]
	}
	for _, key := range []string{"extracted", "document", "fields", "data", "ocr"} {
		if nested, ok := out[key].(map[string]any); ok {
			for k, v := range nested {
				out[k] = v
			}
		}
	}
	return out, nil
}

func extractMobileKYCOCRFields(response any) map[string]any {
	records := collectMobileOCRObjects(response, nil)
	text := strings.Join(collectMobileOCRText(response, nil), "\n")
	fullName := pickMobileOCRString(records, []string{"full_name", "fullName", "name", "nome", "nome_completo", "holder_name", "holderName"})
	cpf := onlyDigitsMobile(pickMobileOCRString(records, []string{"cpf", "document_number", "documentNumber", "tax_id", "taxId", "numero_cpf", "cpf_number"}))
	birthDate := pickMobileOCRString(records, []string{"birth_date", "birthDate", "date_of_birth", "dateOfBirth", "data_nascimento", "nascimento"})
	if cpf == "" {
		if match := regexp.MustCompile(`(?i)CPF\s*[\r\n\s:.-]*([0-9.\s/-]{11,18})`).FindStringSubmatch(text); len(match) > 1 {
			cpf = onlyDigitsMobile(match[1])
		}
		if cpf == "" {
			cpfPattern := regexp.MustCompile(`\b\d{3}[.\s/-]?\d{3}[.\s/-]?\d{3}[.\s/-]?\d{2}\b`)
			cpf = onlyDigitsMobile(cpfPattern.FindString(text))
		}
	}
	if birthDate == "" {
		datePattern := regexp.MustCompile(`\b(?:0?[1-9]|[12]\d|3[01])[\/\-.](?:0?[1-9]|1[0-2])[\/\-.](?:19|20)?\d{2}\b`)
		lines := splitMobileOCRLines(text)
		for i, line := range lines {
			if regexp.MustCompile(`(?i)NATURALIDADE`).MatchString(line) {
				limit := i + 5
				if limit > len(lines) {
					limit = len(lines)
				}
				for _, candidate := range lines[i+1 : limit] {
					if match := datePattern.FindString(candidate); match != "" {
						birthDate = match
						break
					}
				}
				break
			}
		}
		if birthDate == "" {
			birthDate = datePattern.FindString(text)
		}
	}
	if fullName == "" {
		lines := splitMobileOCRLines(text)
		for i, line := range lines {
			if regexp.MustCompile(`(?i)\bNOME\b|NOME\s+CIVIL|NOME\s+COMPLETO`).MatchString(line) {
				for _, candidate := range lines[i+1:] {
					if looksLikeMobileOCRName(candidate) {
						fullName = candidate
						break
					}
				}
				break
			}
		}
		if fullName == "" {
			for _, candidate := range lines {
				if looksLikeMobileOCRName(candidate) && !regexp.MustCompile(`(?i)\bREP[ÚU]BLICA\b|\bBRASIL\b|\bCPF\b|\bIDENTIDADE\b|\bREGISTRO\b`).MatchString(candidate) {
					fullName = candidate
					break
				}
			}
		}
	}
	out := map[string]any{}
	if fullName != "" {
		out["full_name"] = fullName
	}
	if len(cpf) >= 11 {
		out["cpf"] = cpf[:11]
	}
	if birthDate != "" {
		out["birth_date"] = birthDate
	}
	return out
}

func collectMobileOCRObjects(value any, output []map[string]any) []map[string]any {
	switch v := value.(type) {
	case map[string]any:
		output = append(output, v)
		for _, nested := range v {
			output = collectMobileOCRObjects(nested, output)
		}
	case []any:
		for _, nested := range v {
			output = collectMobileOCRObjects(nested, output)
		}
	}
	return output
}

func collectMobileOCRText(value any, output []string) []string {
	switch v := value.(type) {
	case map[string]any:
		for key, nested := range v {
			if str, ok := nested.(string); ok && regexp.MustCompile(`(?i)parsedtext|raw_text|ocr_text|\btext\b`).MatchString(key) {
				output = append(output, str)
			}
			output = collectMobileOCRText(nested, output)
		}
	case []any:
		for _, nested := range v {
			output = collectMobileOCRText(nested, output)
		}
	}
	return output
}

func pickMobileOCRString(records []map[string]any, keys []string) string {
	for _, record := range records {
		for _, key := range keys {
			if value := strings.TrimSpace(fmt.Sprint(record[key])); value != "" && value != "<nil>" {
				return value
			}
		}
	}
	return ""
}

func splitMobileOCRLines(text string) []string {
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

func looksLikeMobileOCRName(value string) bool {
	value = strings.TrimSpace(value)
	if len([]rune(value)) < 8 || strings.ContainsAny(value, "0123456789") {
		return false
	}
	return regexp.MustCompile(`^[A-ZÀ-Ú][A-ZÀ-Ú\s'.-]+$`).MatchString(value)
}

func uploadMobileMultipartMedia(w http.ResponseWriter, r *http.Request, userID, kind string) (*cloudinaryUploadResult, string, error) {
	maxBytes := int64(maxMobileVideoUploadBytes)
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		return nil, "", uploadClientError("arquivo invalido ou acima do limite")
	}
	if kind == "" {
		kind = strings.TrimSpace(r.FormValue("kind"))
	}
	kind = normalizeUploadKind(kind)
	if kind == "" {
		return nil, "", uploadClientError("tipo de upload invalido")
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		return nil, "", uploadClientError("campo file e obrigatorio")
	}
	defer file.Close()

	allowedBytes := int64(maxMobileImageUploadBytes)
	if kind == "facial_video" {
		allowedBytes = maxMobileVideoUploadBytes
	}
	if header.Size > allowedBytes {
		return nil, "", uploadClientError("arquivo acima do limite permitido")
	}

	cfg, err := loadCloudinaryConfig()
	if err != nil {
		return nil, "", err
	}

	folder := fmt.Sprintf("chainfx/mobile/users/%s/avatar", userID)
	if kind != "avatar" {
		folder = fmt.Sprintf("chainfx/mobile/users/%s/kyc/%s", userID, kind)
	}
	publicID := fmt.Sprintf("%s_%d", kind, time.Now().UnixNano())
	upload, err := uploadToCloudinary(r.Context(), cfg, file, header, folder, publicID)
	return upload, kind, err
}

func uploadToCloudinary(ctx context.Context, cfg *cloudinaryConfig, file multipart.File, header *multipart.FileHeader, folder, publicID string) (*cloudinaryUploadResult, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	fileName := sanitizeUploadFileName(header.Filename)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return nil, err
	}

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	params := map[string]string{
		"folder":    folder,
		"public_id": publicID,
		"timestamp": timestamp,
	}
	params["signature"] = signCloudinaryParams(params, cfg.APISecret)
	params["api_key"] = cfg.APIKey
	for key, value := range params {
		if err := writer.WriteField(key, value); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/auto/upload", url.PathEscape(cfg.CloudName))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var payload struct {
		SecureURL    string `json:"secure_url"`
		PublicID     string `json:"public_id"`
		ResourceType string `json:"resource_type"`
		Format       string `json:"format"`
		Bytes        int64  `json:"bytes"`
		Error        struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(payload.Error.Message)
		if msg == "" {
			msg = "falha ao enviar arquivo para Cloudinary"
		}
		return nil, fmt.Errorf("cloudinary: %s", msg)
	}
	if payload.SecureURL == "" {
		return nil, errors.New("cloudinary nao retornou secure_url")
	}
	return &cloudinaryUploadResult{
		SecureURL:    payload.SecureURL,
		PublicID:     payload.PublicID,
		ResourceType: payload.ResourceType,
		Format:       payload.Format,
		Bytes:        payload.Bytes,
	}, nil
}

func signCloudinaryParams(params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		if key != "file" && key != "api_key" && key != "signature" && params[key] != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+params[key])
	}
	sum := sha1.Sum([]byte(strings.Join(parts, "&") + secret))
	return hex.EncodeToString(sum[:])
}

func normalizeUploadKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "avatar":
		return "avatar"
	case "document", "document_front", "front":
		return "document_front"
	case "document_back", "back":
		return "document_back"
	case "selfie", "face", "facial_photo":
		return "selfie"
	case "facial_video", "video":
		return "facial_video"
	default:
		return ""
	}
}

func sanitizeUploadFileName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "upload"
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, base)
}

type uploadError string

func (e uploadError) Error() string { return string(e) }

func uploadClientError(message string) error { return uploadError(message) }

func writeUploadError(w http.ResponseWriter, err error) {
	var clientErr uploadError
	if errors.As(err, &clientErr) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": clientErr.Error()})
		return
	}
	writeJSON(w, http.StatusInternalServerError, mobileProductError("NETWORK_UNAVAILABLE", "Servico indisponivel no momento."))
}
