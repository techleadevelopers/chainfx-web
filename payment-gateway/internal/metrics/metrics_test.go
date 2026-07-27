package metrics

import "testing"

func TestRoutePatternUsesMuxPattern(t *testing.T) {
	got := RoutePattern("GET", "/api/order/ord_12345678901234567890", "GET /api/order/{id}")
	if got != "/api/order/{id}" {
		t.Fatalf("route pattern = %q, want /api/order/{id}", got)
	}
}

func TestRoutePatternNormalizesDynamicSegments(t *testing.T) {
	cases := map[string]string{
		"/api/order/ord_12345678901234567890":                         "/api/order/{id}",
		"/api/nfc/balance/0x000000000000000000000000000000000000dEaD": "/api/nfc/balance/{id}",
		"/api/buy/1234567890abcdef":                                   "/api/buy/{id}",
		"/api/mobile/assets/USDT":                                     "/api/mobile/assets/USDT",
	}
	for path, want := range cases {
		if got := RoutePattern("GET", path, ""); got != want {
			t.Fatalf("RoutePattern(%q) = %q, want %q", path, got, want)
		}
	}
}
