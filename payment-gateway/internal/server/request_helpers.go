package server

import (
	"fmt"
	"net/http"
	"strings"
)

func requestID(r *http.Request) string {
	if value, ok := r.Context().Value(requestIDContextKey{}).(string); ok {
		return value
	}
	return ""
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func markLegacyRoute(w http.ResponseWriter, r *http.Request, successor string) {
	if r == nil || w == nil || !strings.HasPrefix(r.URL.Path, "/api/") {
		return
	}
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Sunset", "Wed, 31 Dec 2026 23:59:59 GMT")
	w.Header().Add("Link", fmt.Sprintf("<%s>; rel=\"successor-version\"", successor))
}

func customerAccessToken(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get("X-Customer-Access-Token")); token != "" {
		return token
	}
	if token := strings.TrimSpace(r.URL.Query().Get("accessToken")); token != "" {
		return token
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}
