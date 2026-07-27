// Package mcp implements a minimal Model Context Protocol (MCP) server so
// LLM agents (Claude, ChatGPT, custom LangGraph/OpenAI agents, etc.) can
// discover and call ChainFX platform actions safely over HTTP.
//
// It intentionally speaks a simplified HTTP+JSON dialect of MCP rather than
// the stdio/JSON-RPC transport used by desktop MCP clients, so it can be
// exposed directly as part of the existing API server:
//
//	POST /mcp/initialize
//	POST /mcp/tools/list
//	POST /mcp/tools/call
//	POST /mcp/resources/list
//	POST /mcp/resources/read
package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"payment-gateway/internal/agents"
	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/webhooks"
	"payment-gateway/internal/workers"
)

const (
	protocolVersion    = "2024-11-05"
	mcpCatalogCacheTTL = 5 * time.Minute
	mcpRouteCacheTTL   = time.Minute
)

// PriceSource is the minimal price lookup the MCP server needs, satisfied
// by workers.PriceWorker.
type PriceSource interface {
	GetPrice(currency string) float64
	GetCurrentPrice() float64
}

// Server exposes ChainFX capabilities (quotes, orders, capability marketplace,
// AI analysis and automation webhooks) as MCP tools, resources and prompts.
type Server struct {
	db       *database.DB
	cfg      *config.Config
	prices   PriceSource
	agents   *agents.Client
	dispatch *webhooks.Dispatcher
	rl       *mcpRateLimiter // per-API-key sliding-window limiter for /mcp/tools/call

	initializeJSON []byte
	toolsListJSON  []byte
	resourcesJSON  []byte
	promptsJSON    []byte

	cacheMu sync.Mutex
	cache   map[string]cachedMCPValue
	builds  map[string]*cachedMCPBuild
}

type cachedMCPValue struct {
	value     any
	expiresAt time.Time
}

type cachedMCPBuild struct {
	done  chan struct{}
	value any
	err   error
}

// ─── Per-API-key rate limiter (sliding window, stdlib only) ──────────────────

// mcpRateLimiter enforces a per-API-key call budget on POST /mcp/tools/call.
// Limits (calls per minute):
//
//	"live" keys  (sk_live_*) : 2000/min
//	"test" keys  (sk_test_*) :  600/min
//	anonymous / no key       :   60/min
type mcpRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*mcpBucket
}

type mcpBucket struct {
	timestamps []time.Time
	limit      int
}

func newMCPRateLimiter() *mcpRateLimiter {
	return &mcpRateLimiter{buckets: make(map[string]*mcpBucket)}
}

// allow returns (allowed, remaining, resetAt).
func (rl *mcpRateLimiter) allow(key string, limit int) (bool, int, time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	window := time.Minute
	cutoff := now.Add(-window)
	resetAt := now.Add(window)

	b, ok := rl.buckets[key]
	if !ok {
		b = &mcpBucket{limit: limit}
		rl.buckets[key] = b
	}

	// Evict timestamps outside the sliding window
	idx := 0
	for idx < len(b.timestamps) && b.timestamps[idx].Before(cutoff) {
		idx++
	}
	b.timestamps = b.timestamps[idx:]

	remaining := b.limit - len(b.timestamps)
	if remaining <= 0 {
		if len(b.timestamps) > 0 {
			resetAt = b.timestamps[0].Add(window)
		}
		return false, 0, resetAt
	}
	b.timestamps = append(b.timestamps, now)
	return true, remaining - 1, resetAt
}

// tierLimit returns calls-per-minute for a given MCP API key and tool class.
func tierLimit(apiKey, toolClass string) int {
	tier := "anonymous"
	switch {
	case strings.HasPrefix(apiKey, "sk_live_"):
		tier = "live"
	case strings.HasPrefix(apiKey, "sk_test_"):
		tier = "test"
	}
	limits := map[string]map[string]int{
		"anonymous": {
			"mcp_tool_read":    300,
			"mcp_tool_write":   30,
			"mcp_ai_expensive": 120,
			"mcp_financial":    30,
			"mcp_abuse":        10,
		},
		"test": {
			"mcp_tool_read":    900,
			"mcp_tool_write":   120,
			"mcp_ai_expensive": 60,
			"mcp_financial":    120,
			"mcp_abuse":        20,
		},
		"live": {
			"mcp_tool_read":    1800,
			"mcp_tool_write":   300,
			"mcp_ai_expensive": 120,
			"mcp_financial":    300,
			"mcp_abuse":        30,
		},
	}
	if byClass, ok := limits[tier]; ok {
		if limit, ok := byClass[toolClass]; ok {
			return limit
		}
	}
	return limits[tier]["mcp_abuse"]
}

// New builds an MCP server bound to the platform's shared services.
func New(db *database.DB, cfg *config.Config, prices *workers.PriceWorker, agentsClient *agents.Client, dispatcher *webhooks.Dispatcher) *Server {
	s := &Server{
		db:       db,
		cfg:      cfg,
		agents:   agentsClient,
		dispatch: dispatcher,
		rl:       newMCPRateLimiter(),
		cache:    make(map[string]cachedMCPValue),
		builds:   make(map[string]*cachedMCPBuild),
	}
	if prices != nil {
		s.prices = prices
	}
	s.initializeJSON = mustJSONBytes(initializePayload())
	s.toolsListJSON = mustJSONBytes(map[string]any{"tools": s.tools()})
	s.resourcesJSON = mustJSONBytes(map[string]any{"resources": s.resources()})
	s.promptsJSON = mustJSONBytes(map[string]any{"prompts": agents.ListPromptTemplates()})
	return s
}

// Authorize is called before every MCP request. It must write an error
// response (401/403) and return false when the caller is not allowed in;
// returning true lets the request proceed. This keeps internal/mcp free of
// any dependency on the main server's session/API-key machinery.
type Authorize func(w http.ResponseWriter, r *http.Request) bool

// RegisterRoutes wires the MCP HTTP endpoints onto mux, prefixing paths with
// /mcp as described in the package doc. Every route is gated by authorize —
// MCP tools can read order data and create/trigger webhook automations, so
// this must never be exposed unauthenticated.
func (s *Server) RegisterRoutes(mux *http.ServeMux, authorize Authorize) {
	mux.HandleFunc("POST /mcp/initialize", s.handleInitialize)
	mux.HandleFunc("POST /mcp/tools/list", s.handleToolsList)
	mux.HandleFunc("POST /mcp/tools/call", s.handleToolsCallWithAuthorize(authorize))
	mux.HandleFunc("POST /mcp/resources/list", s.handleResourcesList)
	mux.HandleFunc("POST /mcp/resources/read", s.handleResourcesReadWithAuthorize(authorize))
	mux.HandleFunc("POST /mcp/prompts/list", s.handlePromptsList)
}

func (s *Server) handleInitialize(w http.ResponseWriter, r *http.Request) {
	writeCachedJSON(w, http.StatusOK, s.initializeJSON)
}

func initializePayload() map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"serverInfo": map[string]any{
			"name":        "chainfx-mcp",
			"title":       "ChainFX Capability Network MCP",
			"version":     "1.2.0",
			"description": "Economic infrastructure for AI agents: discover, execute, meter, bill and settle capabilities with stablecoin payments.",
		},
		"capabilities": map[string]any{
			"tools":     map[string]any{},
			"resources": map[string]any{},
			"prompts":   map[string]any{},
		},
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeCachedJSON(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	w.WriteHeader(status)
	_, _ = ioCopy(w, payload)
}

func mustJSONBytes(payload any) []byte {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return append(raw, '\n')
}

func ioCopy(w http.ResponseWriter, payload []byte) (int64, error) {
	return bytes.NewReader(payload).WriteTo(w)
}

func writeMCPError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message}})
}

func decodeJSON(r *http.Request, dest any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dest)
}

func (s *Server) cachedValue(key string, ttl time.Duration, build func() (any, error)) (any, error) {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	now := time.Now()
	if s != nil {
		s.cacheMu.Lock()
		if s.cache != nil {
			if item, ok := s.cache[key]; ok && item.expiresAt.After(now) {
				s.cacheMu.Unlock()
				return item.value, nil
			}
		}
		if s.builds != nil {
			if inFlight, ok := s.builds[key]; ok {
				s.cacheMu.Unlock()
				<-inFlight.done
				return inFlight.value, inFlight.err
			}
		}
		if s.builds == nil {
			s.builds = make(map[string]*cachedMCPBuild)
		}
		s.builds[key] = &cachedMCPBuild{done: make(chan struct{})}
		s.cacheMu.Unlock()
	}

	value, err := build()
	if s != nil {
		s.cacheMu.Lock()
		if inFlight := s.builds[key]; inFlight != nil {
			inFlight.value = value
			inFlight.err = err
			close(inFlight.done)
			delete(s.builds, key)
		}
		if err == nil && s.cache == nil {
			s.cache = make(map[string]cachedMCPValue)
		}
		if err == nil {
			s.cache[key] = cachedMCPValue{value: value, expiresAt: now.Add(ttl)}
		}
		s.cacheMu.Unlock()
	}
	if err != nil {
		return nil, err
	}
	return value, nil
}
