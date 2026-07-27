package mobile

// helpers_phase5.go — shared helpers for Phase 5 mobile handlers.

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"payment-gateway/internal/workers"
)

// workerEvent builds a workers.Event from a type and payload map.
// Used by mobile handlers to publish events to the WorkerManager bus.
func workerEvent(eventType string, payload map[string]any) workers.Event {
	return workers.Event{Type: eventType, Payload: payload}
}

// validateWebhookTargetURL enforces SSRF policy on user-supplied webhook URLs.
// Rules (applied at creation AND when the delivery worker fires):
//  1. Must be http:// or https://
//  2. Must not resolve to a private, loopback, link-local, or multicast address
//  3. Must not be localhost / 127.x / ::1 / 0.0.0.0
func validateWebhookTargetURL(rawURL string) error {
	u, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return fmt.Errorf("URL inválida: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme não permitido: %s (use http ou https)", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("host ausente na URL")
	}

	// Block by name
	lhost := strings.ToLower(host)
	blocked := []string{"localhost", "0.0.0.0", "::1", "metadata.google.internal", "169.254.169.254"}
	for _, b := range blocked {
		if lhost == b {
			return fmt.Errorf("host bloqueado por política SSRF: %s", host)
		}
	}

	// Block by resolved IP
	addrs, err := net.LookupHost(host)
	if err != nil {
		// SECURITY: DNS resolution failed — deny by default (fail-closed).
		// Allowing on DNS failure enables DNS-rebinding attacks where a host
		// resolves at creation time but later points to an internal IP.
		return fmt.Errorf("nao foi possivel resolver o host da targetUrl (DNS failure): %w", err)
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		if isPrivateOrReservedIP(ip) {
			return fmt.Errorf("URL aponta para endereço interno/privado: %s → %s", host, addr)
		}
	}
	return nil
}

// isPrivateOrReservedIP returns true for loopback, private, link-local and
// multicast ranges that should never receive outbound webhook payloads.
func isPrivateOrReservedIP(ip net.IP) bool {
	private := []string{
		"127.0.0.0/8",    // loopback
		"::1/128",        // IPv6 loopback
		"10.0.0.0/8",     // RFC1918
		"172.16.0.0/12",  // RFC1918
		"192.168.0.0/16", // RFC1918
		"169.254.0.0/16", // link-local
		"fe80::/10",      // IPv6 link-local
		"fc00::/7",       // IPv6 unique local
		"0.0.0.0/8",      // this network
		"240.0.0.0/4",    // reserved
		"224.0.0.0/4",    // multicast
		"100.64.0.0/10",  // shared address space (RFC6598)
	}
	for _, cidr := range private {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
