package server

import (
	"runtime"
	"strings"
)

func (s *Server) adminObservability() map[string]any {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	out := map[string]any{
		"cpu": map[string]any{
			"logicalCores": runtime.NumCPU(),
			"gomaxprocs":   runtime.GOMAXPROCS(0),
			"usagePercent": nil,
			"note":         "process CPU percent requires host/runtime exporter",
		},
		"memory": map[string]any{
			"allocMB":     bytesToMB(mem.Alloc),
			"sysMB":       bytesToMB(mem.Sys),
			"heapAllocMB": bytesToMB(mem.HeapAlloc),
			"heapInUseMB": bytesToMB(mem.HeapInuse),
			"nextGCMB":    bytesToMB(mem.NextGC),
			"numGC":       mem.NumGC,
		},
		"goroutines": runtime.NumGoroutine(),
		"requestLogs": map[string]any{
			"queueDepth": queueDepth(s),
			"queueCap":   queueCap(s),
			"dropped":    requestLogDrops(s),
		},
		"redis": redisObservability(s),
	}

	if s != nil && s.db != nil && s.db.SQL != nil {
		stats := s.db.SQL.Stats()
		out["postgres"] = map[string]any{
			"maxOpenConnections": stats.MaxOpenConnections,
			"openConnections":    stats.OpenConnections,
			"inUse":              stats.InUse,
			"idle":               stats.Idle,
			"waitCount":          stats.WaitCount,
			"waitDurationMs":     stats.WaitDuration.Milliseconds(),
			"maxIdleClosed":      stats.MaxIdleClosed,
			"maxIdleTimeClosed":  stats.MaxIdleTimeClosed,
			"maxLifetimeClosed":  stats.MaxLifetimeClosed,
		}
	} else {
		out["postgres"] = map[string]any{"status": "unavailable"}
	}

	return out
}

func bytesToMB(v uint64) int64 {
	return int64(v / 1024 / 1024)
}

func queueDepth(s *Server) int {
	if s == nil || s.requestLogQueue == nil {
		return 0
	}
	return len(s.requestLogQueue)
}

func queueCap(s *Server) int {
	if s == nil || s.requestLogQueue == nil {
		return 0
	}
	return cap(s.requestLogQueue)
}

func requestLogDrops(s *Server) int64 {
	if s == nil {
		return 0
	}
	return s.requestLogDrops.Load()
}

func redisObservability(s *Server) map[string]any {
	if s == nil || s.cfg == nil {
		return map[string]any{"status": "unavailable"}
	}
	requestedBackend := strings.ToLower(strings.TrimSpace(s.cfg.RateLimitBackend))
	if requestedBackend == "" {
		requestedBackend = "memory"
	}
	if strings.TrimSpace(s.cfg.RedisURL) == "" {
		return map[string]any{
			"status":           "not_configured",
			"urlConfigured":    false,
			"requestedBackend": requestedBackend,
			"rateLimitBackend": s.cfg.RateLimitBackend,
			"effectiveBackend": "memory",
		}
	}
	global := map[string]any{"status": "unavailable"}
	if s.globalLimiter != nil {
		global = s.globalLimiter.Stats()
	}
	orders := map[string]any{"status": "unavailable"}
	if s.limiter != nil {
		orders = s.limiter.Stats()
	}
	effectiveBackend := limiterBackend(global)
	if orderBackend := limiterBackend(orders); orderBackend != "" && effectiveBackend != "" && orderBackend != effectiveBackend {
		effectiveBackend = "mixed"
	} else if effectiveBackend == "" {
		effectiveBackend = orderBackend
	}
	if effectiveBackend == "" {
		effectiveBackend = requestedBackend
	}
	return map[string]any{
		"status":           "configured",
		"urlConfigured":    true,
		"requestedBackend": requestedBackend,
		"effectiveBackend": effectiveBackend,
		"rateLimitBackend": s.cfg.RateLimitBackend,
		"globalLimiter":    global,
		"orderLimiter":     orders,
	}
}

func limiterBackend(stats map[string]any) string {
	if stats == nil {
		return ""
	}
	if backend, ok := stats["backend"].(string); ok {
		return strings.ToLower(strings.TrimSpace(backend))
	}
	return ""
}
