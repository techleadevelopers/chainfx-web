package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/webhooks"
)

// Resource describes an MCP resource: read-only context an agent can fetch.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

func (s *Server) resources() []Resource {
	return []Resource{
		{URI: "chainfx://rates/latest", Name: "Current rates", Description: "USDT/BRL, USDT/USD, BTC/USDT and supported pairs.", MimeType: "application/json"},
		{URI: "chainfx://marketplace/capabilities", Name: "Capability Network", Description: "Capability-first network for AI agents to discover, execute, meter, bill and settle digital capabilities.", MimeType: "application/json"},
		{URI: "chainfx://capability-contracts/{id}", Name: "Capability Contract", Description: "Versioned input/output contract for a capability. Replace {id} with document_ocr, llm_chat, etc.", MimeType: "application/json"},
		{URI: "chainfx://marketplace/products", Name: "Marketplace Products", Description: "Premium marketplace products and plans kept for product-level compatibility.", MimeType: "application/json"},
		{URI: "chainfx://agent/assets", Name: "Agent Rail Assets", Description: "Stablecoin assets enabled for Agent Rail and capability payments.", MimeType: "application/json"},
		{URI: "chainfx://webhooks/events", Name: "Automation events", Description: "Automation webhook events available to n8n/Zapier/Make including M2M and capability lifecycle events.", MimeType: "application/json"},
		{URI: "chainfx://webhooks/subscriptions", Name: "Webhook subscriptions", Description: "Currently configured automation subscriptions.", MimeType: "application/json"},
		{URI: "chainfx://orders/{id}", Name: "Order by id", Description: "Details for a buy/sell order. Replace {id} with the real id.", MimeType: "application/json"},
		{URI: "chainfx://agent/grants/{wallet}", Name: "Agent active grants", Description: "Active access grants (capability tokens with quota) for an agent wallet. Replace {wallet} with EVM address.", MimeType: "application/json"},
		{URI: "chainfx://agent/policy/{wallet}", Name: "Agent policy", Description: "Execution policy, spend limits and pricing overrides for an agent wallet. Replace {wallet} with EVM address.", MimeType: "application/json"},
		{URI: "chainfx://agent/intents/{wallet}", Name: "Agent payment intents", Description: "Recent M2M payment intents for an agent wallet. Replace {wallet} with EVM address.", MimeType: "application/json"},
		{URI: "chainfx://mcp/registry", Name: "MCP Capability Registry", Description: "Machine-readable capability catalog for MCP client discovery.", MimeType: "application/json"},
	}
}

func (s *Server) handleResourcesList(w http.ResponseWriter, r *http.Request) {
	writeCachedJSON(w, http.StatusOK, s.resourcesJSON)
}

type resourceReadRequest struct {
	URI string `json:"uri"`
}

func (s *Server) handleResourcesRead(w http.ResponseWriter, r *http.Request) {
	s.handleResourcesReadWithAuthorize(nil)(w, r)
}

func (s *Server) handleResourcesReadWithAuthorize(authorize Authorize) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req resourceReadRequest
		if err := decodeJSON(r, &req); err != nil {
			writeMCPError(w, http.StatusBadRequest, "JSON invalido")
			return
		}

		req.URI = strings.TrimSpace(req.URI)
		if req.URI == "" {
			writeMCPError(w, http.StatusBadRequest, "uri obrigatorio")
			return
		}

		if !isPublicMCPResource(req.URI) && authorize != nil {
			if mcpAPIKey(r) == "" {
				writeResourceContent(w, req.URI, map[string]any{
					"authRequired": true,
					"status":       "unauthorized",
					"message":      "Resource requires a ChainFX API key. Discovery remains public, but account-scoped data is not exposed anonymously.",
				})
				return
			}
			if !authorize(w, r) {
				return
			}
		}

		content, err := s.readResource(r.Context(), req.URI)
		if err != nil {
			writeMCPError(w, http.StatusNotFound, err.Error())
			return
		}
		writeResourceContent(w, req.URI, content)
	}
}

func writeResourceContent(w http.ResponseWriter, uri string, content any) {
	writeJSON(w, http.StatusOK, map[string]any{
		"contents": []map[string]any{{"uri": uri, "mimeType": "application/json", "json": content}},
	})
}

func isPublicMCPResource(uri string) bool {
	uri = strings.TrimSpace(uri)
	switch {
	case uri == "chainfx://rates/latest",
		uri == "chainfx://marketplace/capabilities",
		uri == "chainfx://marketplace/products",
		uri == "chainfx://agent/assets",
		uri == "chainfx://webhooks/events",
		uri == "chainfx://mcp/registry":
		return true
	case strings.HasPrefix(uri, "chainfx://capability-contracts/"):
		return true
	default:
		return false
	}
}

func (s *Server) readResource(ctx context.Context, uri string) (any, error) {
	switch {
	case uri == "chainfx://rates/latest":
		return s.toolGetRates(), nil
	case uri == "chainfx://marketplace/capabilities":
		if s.db == nil {
			return fallbackCapabilities(), nil
		}
		value, err := s.cachedValue("resource:marketplace:capabilities", mcpCatalogCacheTTL, func() (any, error) {
			return s.db.ListMarketplaceCapabilities(ctx, database.MarketplaceProductFilters{})
		})
		capabilities, _ := value.([]*database.MarketplaceCapability)
		if err != nil || len(capabilities) == 0 {
			return fallbackCapabilities(), nil
		}
		return capabilities, nil
	case strings.HasPrefix(uri, "chainfx://capability-contracts/"):
		id := strings.TrimPrefix(uri, "chainfx://capability-contracts/")
		id = normalizeCapabilityID(id)
		if s.db == nil {
			return fallbackCapabilityContract(id), nil
		}
		value, err := s.cachedValue("resource:capability-contract:"+strings.ToLower(id)+":v1", mcpCatalogCacheTTL, func() (any, error) {
			return s.db.GetMarketplaceCapabilityContract(ctx, id, "v1")
		})
		if err != nil {
			return fallbackCapabilityContract(id), nil
		}
		contract, _ := value.(*database.MarketplaceCapabilityContract)
		if contract == nil {
			return fallbackCapabilityContract(id), nil
		}
		return contract, nil
	case uri == "chainfx://marketplace/products":
		if s.db == nil {
			return map[string]any{"products": []any{}, "source": "fallback"}, nil
		}
		return s.cachedValue("resource:marketplace:products", mcpCatalogCacheTTL, func() (any, error) {
			return s.db.ListMarketplaceProducts(ctx, database.MarketplaceProductFilters{})
		})
	case uri == "chainfx://agent/assets":
		if s.db == nil {
			return fallbackAgentAssets(), nil
		}
		assets, err := s.db.ListAgentSupportedAssets(ctx)
		if err != nil || len(assets) == 0 {
			return fallbackAgentAssets(), nil
		}
		return assets, nil
	case uri == "chainfx://webhooks/events":
		return webhooks.AllEvents(), nil
	case uri == "chainfx://webhooks/subscriptions":
		return s.db.ListWebhookSubscriptions(ctx)
	case strings.HasPrefix(uri, "chainfx://orders/"):
		id := strings.TrimPrefix(uri, "chainfx://orders/")
		return s.toolGetOrderStatus(ctx, map[string]any{"orderId": id})
	case strings.HasPrefix(uri, "chainfx://agent/grants/"):
		wallet := strings.TrimPrefix(uri, "chainfx://agent/grants/")
		return s.toolListAgentGrants(ctx, map[string]any{"agentWallet": wallet})
	case strings.HasPrefix(uri, "chainfx://agent/policy/"):
		wallet := strings.TrimPrefix(uri, "chainfx://agent/policy/")
		return s.toolGetAgentPolicy(ctx, map[string]any{"agentWallet": wallet})
	case strings.HasPrefix(uri, "chainfx://agent/intents/"):
		wallet := strings.TrimPrefix(uri, "chainfx://agent/intents/")
		return s.toolListAgentPaymentIntents(ctx, map[string]any{"agentWallet": wallet})
	case uri == "chainfx://mcp/registry":
		if s.db == nil {
			return fallbackCapabilities(), nil
		}
		return s.cachedValue("resource:mcp:registry", mcpCatalogCacheTTL, func() (any, error) {
			return s.db.ListMarketplaceCapabilities(ctx, database.MarketplaceProductFilters{})
		})
	default:
		return nil, fmt.Errorf("recurso desconhecido: %s", uri)
	}
}

func normalizeCapabilityID(id string) string {
	id = strings.TrimSpace(id)
	switch id {
	case "", "{id}", ":id", "id":
		return "llm_chat"
	default:
		return id
	}
}

func fallbackCapabilities() []map[string]any {
	now := time.Now().UTC()
	return []map[string]any{
		{
			"id":          "llm_chat",
			"slug":        "llm_chat",
			"displayName": "LLM Chat",
			"description": "General text generation capability for agent workflows.",
			"category":    "ai",
			"routingMode": "best_available",
			"status":      "active",
			"operations":  []string{"chat", "summarize", "classify"},
			"providers":   []string{"chainfx-mock"},
			"createdAt":   now,
			"source":      "fallback",
		},
		{
			"id":          "document_ocr",
			"slug":        "document_ocr",
			"displayName": "Document OCR",
			"description": "Extract text and structured fields from documents.",
			"category":    "documents",
			"routingMode": "best_available",
			"status":      "active",
			"operations":  []string{"extract_text", "extract_fields"},
			"providers":   []string{"chainfx-mock"},
			"createdAt":   now,
			"source":      "fallback",
		},
	}
}

func fallbackCapabilityContract(id string) map[string]any {
	id = normalizeCapabilityID(id)
	now := time.Now().UTC()
	inputSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"input": map[string]any{"type": "object"},
		},
	}
	outputSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{"type": "string"},
			"output": map[string]any{"type": "object"},
		},
	}
	return map[string]any{
		"id":           id + ":v1",
		"capability":   id,
		"version":      "v1",
		"status":       "active",
		"inputSchema":  mustJSON(inputSchema),
		"outputSchema": mustJSON(outputSchema),
		"examples":     mustJSON([]map[string]any{{"input": map[string]any{"prompt": "hello"}, "output": map[string]any{"status": "ok"}}}),
		"metadata":     mustJSON(map[string]any{"source": "fallback", "note": "Fallback contract returned because no active contract was found in the catalog."}),
		"createdAt":    now,
		"updatedAt":    now,
	}
}

func fallbackAgentAssets() []map[string]any {
	now := time.Now().UTC()
	return []map[string]any{
		{
			"symbol":          "USDT",
			"network":         "BSC",
			"contractAddress": "0x55d398326f99059fF775485246999027B3197955",
			"decimals":        18,
			"feeBps":          0,
			"minAmount":       1,
			"status":          "active",
			"enabled":         true,
			"createdAt":       now,
			"source":          "fallback",
		},
	}
}

func mustJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}
