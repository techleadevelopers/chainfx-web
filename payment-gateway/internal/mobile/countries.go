package mobile

// countries.go — Phase 5: Multi-Country + Multi-Rail endpoints (mobile-only)
//
//	GET /api/mobile/countries                       — list active countries
//	GET /api/mobile/countries/{code}                — single country + rails
//	GET /api/mobile/countries/{code}/rails          — payment rails for country
//	GET /api/mobile/countries/detect                — detect country from IP/header

import (
	"net/http"
	"strings"

	"payment-gateway/internal/models"
)

// handleListCountries — GET /api/mobile/countries
func (s *Server) handleListCountries(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", mobileStaticCacheControl)
	if cached, ok := s.getMobileCache("countries:list"); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	countries, err := mobileDB(s.db).ListCountries(r.Context())
	if err != nil {
		countries = fallbackMobileCountries()
	}
	if countries == nil {
		countries = []models.Country{}
	}
	response := map[string]any{"countries": countries, "count": len(countries)}
	s.setMobileCache("countries:list", response, mobileCatalogCacheTTL)
	writeJSON(w, http.StatusOK, response)
}

// handleGetCountry — GET /api/mobile/countries/{code}
func (s *Server) handleGetCountry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", mobileStaticCacheControl)
	code := strings.ToUpper(r.PathValue("code"))
	cacheKey := "countries:get:" + code
	if cached, ok := s.getMobileCache(cacheKey); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	country, err := mobileDB(s.db).GetCountry(r.Context(), code)
	if err != nil {
		country = fallbackMobileCountry(code)
	}
	if country == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "país não encontrado"})
		return
	}
	rails, _ := mobileDB(s.db).ListRailsByCountry(r.Context(), code)
	if len(rails) == 0 {
		rails = fallbackMobileRails(code)
	}
	response := map[string]any{
		"country": country,
		"rails":   rails,
	}
	s.setMobileCache(cacheKey, response, mobileCatalogCacheTTL)
	writeJSON(w, http.StatusOK, response)
}

// handleListCountryRails — GET /api/mobile/countries/{code}/rails
func (s *Server) handleListCountryRails(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", mobileStaticCacheControl)
	code := strings.ToUpper(r.PathValue("code"))
	cacheKey := "countries:rails:" + code
	if cached, ok := s.getMobileCache(cacheKey); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	rails, err := mobileDB(s.db).ListRailsByCountry(r.Context(), code)
	if err != nil {
		rails = fallbackMobileRails(code)
	}
	if rails == nil {
		rails = []models.PaymentRail{}
	}
	response := map[string]any{"rails": rails, "count": len(rails)}
	s.setMobileCache(cacheKey, response, mobileCatalogCacheTTL)
	writeJSON(w, http.StatusOK, response)
}

// handleDetectCountry — GET /api/mobile/countries/detect
// Detects country from CF-IPCountry / X-Country-Code header or defaults to BR.
func (s *Server) handleDetectCountry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", mobileStaticCacheControl)
	code := r.Header.Get("CF-IPCountry")
	if code == "" {
		code = r.Header.Get("X-Country-Code")
	}
	if code == "" || code == "XX" {
		code = "BR"
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	cacheKey := "countries:detect:" + code
	if cached, ok := s.getMobileCache(cacheKey); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}

	country, _ := mobileDB(s.db).GetCountry(r.Context(), code)
	if country == nil {
		// fallback to BR
		country, _ = mobileDB(s.db).GetCountry(r.Context(), "BR")
	}
	if country == nil {
		country = fallbackMobileCountry("BR")
	}
	rails, _ := mobileDB(s.db).ListRailsByCountry(r.Context(), country.Code)
	if rails == nil {
		rails = fallbackMobileRails(country.Code)
	}
	response := map[string]any{
		"detected_code": code,
		"country":       country,
		"rails":         rails,
	}
	s.setMobileCache(cacheKey, response, mobileCatalogCacheTTL)
	writeJSON(w, http.StatusOK, response)
}

func fallbackMobileCountries() []models.Country {
	return []models.Country{*fallbackMobileCountry("BR")}
}

func fallbackMobileCountry(code string) *models.Country {
	if strings.ToUpper(strings.TrimSpace(code)) != "BR" {
		return nil
	}
	return &models.Country{Code: "BR", Name: "Brasil", Currency: "BRL", Language: "pt-BR", Active: true}
}

func fallbackMobileRails(code string) []models.PaymentRail {
	if strings.ToUpper(strings.TrimSpace(code)) != "BR" {
		return []models.PaymentRail{}
	}
	return []models.PaymentRail{
		{ID: "fallback-br-pix", CountryCode: "BR", Name: "PIX", Currency: "BRL", Active: true},
		{ID: "fallback-br-card", CountryCode: "BR", Name: "Cartao", Currency: "BRL", Active: true},
	}
}
