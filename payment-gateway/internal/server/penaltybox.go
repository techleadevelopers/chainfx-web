package server

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/metrics"
)

type penaltyBox struct {
	mu              sync.Mutex
	enabled         bool
	threshold       int
	window          time.Duration
	baseBan         time.Duration
	escalatedBan    time.Duration
	maxBan          time.Duration
	offenseReset    time.Duration
	maxEntries      int
	entries         map[string]*penaltyEntry
	cleanupDeadline time.Time
}

type penaltyEntry struct {
	windowStart  time.Time
	violations   int
	bannedUntil  time.Time
	offenses     int
	lastActivity time.Time
}

func newPenaltyBoxFromConfig(cfg *config.Config) *penaltyBox {
	if cfg == nil {
		return newPenaltyBox(true, 10, 2*time.Minute, 15*time.Minute, time.Hour, 24*time.Hour)
	}
	return newPenaltyBox(
		cfg.PenaltyBoxEnabled,
		cfg.PenaltyViolationLimit,
		time.Duration(cfg.PenaltyViolationWindowSec)*time.Second,
		time.Duration(cfg.PenaltyBaseBanSec)*time.Second,
		time.Duration(cfg.PenaltyEscalatedBanSec)*time.Second,
		time.Duration(cfg.PenaltyMaxBanSec)*time.Second,
	)
}

func newPenaltyBox(enabled bool, threshold int, window, baseBan, escalatedBan, maxBan time.Duration) *penaltyBox {
	if threshold <= 0 {
		threshold = 10
	}
	if window <= 0 {
		window = 2 * time.Minute
	}
	if baseBan <= 0 {
		baseBan = 15 * time.Minute
	}
	if escalatedBan <= 0 {
		escalatedBan = time.Hour
	}
	if maxBan <= 0 {
		maxBan = 24 * time.Hour
	}
	return &penaltyBox{
		enabled:      enabled,
		threshold:    threshold,
		window:       window,
		baseBan:      baseBan,
		escalatedBan: escalatedBan,
		maxBan:       maxBan,
		offenseReset: 24 * time.Hour,
		maxEntries:   100000,
		entries:      make(map[string]*penaltyEntry),
	}
}

func (p *penaltyBox) banned(key string, now time.Time) (bool, time.Time) {
	if p == nil || !p.enabled || key == "" {
		return false, time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanupLocked(now)
	entry := p.entries[key]
	if entry == nil {
		return false, time.Time{}
	}
	entry.lastActivity = now
	if now.Before(entry.bannedUntil) {
		metrics.IncPenaltyBoxBlockedRequest()
		metrics.SetPenaltyBoxActiveBans(p.activeBansLocked(now))
		return true, entry.bannedUntil
	}
	metrics.SetPenaltyBoxActiveBans(p.activeBansLocked(now))
	return false, time.Time{}
}

func (p *penaltyBox) recordViolation(key string, now time.Time) (bool, time.Time) {
	if p == nil || !p.enabled || key == "" {
		return false, time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanupLocked(now)
	entry := p.entries[key]
	if entry == nil {
		entry = &penaltyEntry{windowStart: now}
		p.entries[key] = entry
	}
	entry.lastActivity = now
	if now.Sub(entry.windowStart) > p.window {
		entry.windowStart = now
		entry.violations = 0
	}
	if !entry.bannedUntil.IsZero() && now.Sub(entry.bannedUntil) > p.offenseReset {
		entry.offenses = 0
	}
	entry.violations++
	metrics.IncPenaltyBoxViolation()
	if entry.violations < p.threshold {
		metrics.SetPenaltyBoxActiveBans(p.activeBansLocked(now))
		return false, time.Time{}
	}
	entry.offenses++
	entry.violations = 0
	entry.windowStart = now
	entry.bannedUntil = now.Add(p.banDuration(entry.offenses))
	metrics.IncPenaltyBoxBan()
	if entry.offenses > 1 {
		metrics.IncPenaltyBoxEscalation()
	}
	metrics.SetPenaltyBoxActiveBans(p.activeBansLocked(now))
	return true, entry.bannedUntil
}

func (p *penaltyBox) banDuration(offenses int) time.Duration {
	switch {
	case offenses <= 1:
		return p.baseBan
	case offenses == 2:
		return p.escalatedBan
	default:
		return p.maxBan
	}
}

func (p *penaltyBox) cleanupLocked(now time.Time) {
	if now.Before(p.cleanupDeadline) && len(p.entries) <= p.maxEntries {
		return
	}
	p.cleanupDeadline = now.Add(time.Minute)
	for key, entry := range p.entries {
		if entry == nil || (now.After(entry.bannedUntil) && now.Sub(entry.lastActivity) > p.offenseReset) {
			delete(p.entries, key)
		}
	}
	metrics.SetPenaltyBoxActiveBans(p.activeBansLocked(now))
}

func (p *penaltyBox) activeBansLocked(now time.Time) int {
	active := 0
	for _, entry := range p.entries {
		if entry != nil && now.Before(entry.bannedUntil) {
			active++
		}
	}
	return active
}

func penaltyKeyForRequest(r *http.Request, routeClass string) string {
	ip := clientIP(r)
	if ip == "" {
		ip = "unknown"
	}
	credential := chainFXAPIKeyFromHeader(r)
	if credential == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if auth != "" {
			credential = auth
		}
	}
	if credential != "" {
		credential = shortSecretHash(credential)
	}
	return "ip:" + ip + "|credential:" + credential + "|route:" + routeClass
}
