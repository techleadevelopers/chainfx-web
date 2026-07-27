// Package certutil provides shared mTLS client-certificate loading for
// providers that require mutual TLS (currently Efí Bank PIX). Extracted so
// both the HTTP server (internal/server) and the PSP adapter wiring
// (cmd/api/main.go) load certificates identically instead of duplicating
// PKCS#12/PEM decoding logic.
package certutil

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/pkcs12"
)

// LoadCertificate loads a client TLS certificate from, in priority order:
//  1. p12Base64 — a base64-encoded PKCS#12 (.p12/.pfx) bundle
//  2. certPath ending in .p12/.pfx — a PKCS#12 file on disk
//  3. certPath + keyPath — a PEM certificate/key pair (keyPath defaults to
//     certPath when empty, for combined PEM files)
//
// p12Password is only used for PKCS#12 sources; it is ignored for PEM.
func LoadCertificate(certPath, keyPath, p12Base64, p12Password string) (tls.Certificate, error) {
	if rawBase64 := strings.Trim(strings.TrimSpace(p12Base64), `"'`); rawBase64 != "" {
		raw, err := base64.StdEncoding.DecodeString(rawBase64)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("certutil: invalid base64 p12 bundle: %w", err)
		}
		return decodeP12(raw, p12Password)
	}

	certPath = strings.TrimSpace(certPath)
	if certPath == "" {
		return tls.Certificate{}, fmt.Errorf("certutil: no certificate source configured (need cert path or base64 p12 bundle)")
	}

	if strings.HasSuffix(strings.ToLower(certPath), ".p12") || strings.HasSuffix(strings.ToLower(certPath), ".pfx") {
		raw, err := os.ReadFile(certPath)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("certutil: read p12 file: %w", err)
		}
		return decodeP12(raw, p12Password)
	}

	keyPath = strings.TrimSpace(keyPath)
	if keyPath == "" {
		keyPath = certPath
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certutil: invalid PEM/KEY certificate: %w", err)
	}
	if cert.Leaf == nil && len(cert.Certificate) > 0 {
		cert.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
	}
	return cert, nil
}

func decodeP12(raw []byte, password string) (tls.Certificate, error) {
	privateKey, cert, err := pkcs12.Decode(raw, password)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certutil: invalid p12 bundle; check password and GODEBUG=x509negativeserial=1: %w", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{cert.Raw},
		PrivateKey:  privateKey,
		Leaf:        cert,
	}, nil
}
