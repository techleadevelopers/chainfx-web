package server

import (
	"net"
	"net/http"
	"strings"
)

func clientIP(r *http.Request) string {
	if trustedForwardedHeaders(r) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if ip := strings.TrimSpace(strings.Split(xff, ",")[0]); ip != "" {
				return ip
			}
		}
		if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
			return realIP
		}
	}
	return remoteIP(r)
}

func remoteIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func ipAllowedByCIDRList(rawIP, csv string) bool {
	ipText := strings.TrimSpace(rawIP)
	if host, _, err := net.SplitHostPort(ipText); err == nil {
		ipText = host
	}
	ip := net.ParseIP(ipText)
	if ip == nil {
		return false
	}
	for _, item := range strings.Split(csv, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !strings.Contains(item, "/") {
			if allowedIP := net.ParseIP(item); allowedIP != nil && allowedIP.Equal(ip) {
				return true
			}
			continue
		}
		_, network, err := net.ParseCIDR(item)
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func trustedForwardedHeaders(r *http.Request) bool {
	if r == nil {
		return false
	}
	ip := net.ParseIP(remoteIP(r))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return true
	}
	if ipAllowedByCIDRList(ip.String(), "100.64.0.0/10,169.254.0.0/16,::1/128,fc00::/7,fe80::/10") {
		return true
	}
	return false
}
