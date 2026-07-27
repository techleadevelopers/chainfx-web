package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type cachedDiscoveryDocument struct {
	body      []byte
	etag      string
	expiresAt time.Time
}

func (s *Server) writeCachedDiscoveryJSON(w http.ResponseWriter, r *http.Request, key string, ttl time.Duration, build func() (any, error)) {
	if ttl <= 0 {
		ttl = time.Minute
	}
	now := time.Now()
	doc, ok := s.cachedDiscoveryDocument(key, now)
	if !ok {
		payload, err := build()
		if err != nil {
			var statusErr httpStatusError
			if errors.As(err, &statusErr) {
				writeAPIError(w, r, statusErr.status, statusErr.code, statusErr.message)
				return
			}
			writeError(w, err)
			return
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			writeError(w, err)
			return
		}
		raw = append(raw, '\n')
		sum := sha256.Sum256(raw)
		doc = cachedDiscoveryDocument{
			body:      raw,
			etag:      `"` + hex.EncodeToString(sum[:8]) + `"`,
			expiresAt: now.Add(ttl),
		}
		s.storeDiscoveryDocument(key, doc)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	w.Header().Set("ETag", doc.etag)
	if strings.TrimSpace(r.Header.Get("If-None-Match")) == doc.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc.body)
}

func (s *Server) cachedDiscoveryDocument(key string, now time.Time) (cachedDiscoveryDocument, bool) {
	if s == nil {
		return cachedDiscoveryDocument{}, false
	}
	s.discoveryCacheMu.Lock()
	defer s.discoveryCacheMu.Unlock()
	doc, ok := s.discoveryCache[key]
	if !ok || !doc.expiresAt.After(now) {
		return cachedDiscoveryDocument{}, false
	}
	return doc, true
}

func (s *Server) storeDiscoveryDocument(key string, doc cachedDiscoveryDocument) {
	if s == nil {
		return
	}
	s.discoveryCacheMu.Lock()
	defer s.discoveryCacheMu.Unlock()
	if s.discoveryCache == nil {
		s.discoveryCache = make(map[string]cachedDiscoveryDocument)
	}
	s.discoveryCache[key] = doc
}

type httpStatusError struct {
	status  int
	code    string
	message string
}

func (e httpStatusError) Error() string {
	return e.message
}

func notFoundError(message string) error {
	return httpStatusError{status: http.StatusNotFound, code: "NOT_FOUND", message: message}
}
