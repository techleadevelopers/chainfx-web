package mobile

import "time"

type mobileCacheEntry struct {
	expiresAt time.Time
	value     any
}

const (
	mobileHotCacheTTL     = 15 * time.Second
	mobileCatalogCacheTTL = 5 * time.Minute

	mobileStaticCacheControl = "public, max-age=60, stale-while-revalidate=300"
	mobileRateCacheControl   = "public, max-age=15, stale-while-revalidate=60"
)

func (s *Server) getMobileCache(key string) (any, bool) {
	if s == nil {
		return nil, false
	}
	now := time.Now()
	s.cacheMu.RLock()
	entry, ok := s.cache[key]
	s.cacheMu.RUnlock()
	if !ok || now.After(entry.expiresAt) {
		if ok {
			s.cacheMu.Lock()
			if stale, exists := s.cache[key]; exists && now.After(stale.expiresAt) {
				delete(s.cache, key)
			}
			s.cacheMu.Unlock()
		}
		return nil, false
	}
	return entry.value, true
}

func (s *Server) setMobileCache(key string, value any, ttl time.Duration) {
	if s == nil || ttl <= 0 {
		return
	}
	s.cacheMu.Lock()
	if s.cache == nil {
		s.cache = make(map[string]mobileCacheEntry)
	}
	s.cache[key] = mobileCacheEntry{expiresAt: time.Now().Add(ttl), value: value}
	s.cacheMu.Unlock()
}
