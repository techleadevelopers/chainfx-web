package server

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleAgentRegistries(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	s.writeCachedDiscoveryJSON(w, r, "agent-registries:"+base, time.Minute, func() (any, error) {
		return s.agentRegistryIndex(base), nil
	})
}

func (s *Server) handleAGNTCYWellKnown(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	s.writeCachedDiscoveryJSON(w, r, "agntcy-well-known:"+base, time.Minute, func() (any, error) {
		return s.agntcyRecord(base)
	})
}

func (s *Server) handleOASFWellKnown(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	s.writeCachedDiscoveryJSON(w, r, "oasf-well-known:"+base, time.Minute, func() (any, error) {
		return s.oasfRecord(base)
	})
}

func (s *Server) handleCapabilityGraphRegistryWellKnown(w http.ResponseWriter, r *http.Request) {
	s.handleAgentGraphRegistry(w, r)
}

func (s *Server) handleAgentGraphRegistry(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	s.writeCachedDiscoveryJSON(w, r, "agent-graph-registry:"+base, time.Minute, func() (any, error) {
		return s.capabilityGraphRegistryDocument(base), nil
	})
}

func (s *Server) handleAgentRegistryRecord(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	id := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	switch id {
	case "agntcy", "agntcy-oasf", "oasf":
		s.writeCachedDiscoveryJSON(w, r, "agent-registry-record:"+id+":"+base, time.Minute, func() (any, error) {
			return s.signedRegistryRecord(base, id)
		})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "registry record not found", "supported": []string{"agntcy-oasf", "oasf"}})
	}
}

func (s *Server) agentRegistryIndex(base string) map[string]any {
	return map[string]any{
		"agent":      "ChainFX Agent Pay",
		"version":    "1.0.0",
		"updated_at": time.Now().UTC().Format(time.RFC3339),
		"registries": []map[string]any{
			{
				"id":          "mcp-registry",
				"name":        "Model Context Protocol Registry",
				"status":      "published",
				"manifest":    base + "/mcp/initialize",
				"server_json": base + "/.well-known/mcp-server.json",
				"protocol":    "mcp",
			},
			{
				"id":       "a2a-agent-card",
				"name":     "A2A Agent Card",
				"status":   "available",
				"manifest": base + "/.well-known/agent-card.json",
				"protocol": "a2a",
			},
			{
				"id":       "agntcy-oasf",
				"name":     "AGNTCY / OASF Agent Directory Record",
				"status":   "ready_to_publish",
				"manifest": base + "/.well-known/agntcy.json",
				"record":   base + "/agent/v1/registry-records/agntcy-oasf",
				"protocol": "oasf",
			},
			{
				"id":       "openapi",
				"name":     "OpenAPI Catalog",
				"status":   "available",
				"manifest": base + "/openapi.json",
				"protocol": "openapi",
			},
			{
				"id":       "x402",
				"name":     "x402 Capability Payments",
				"status":   "available",
				"manifest": base + "/.well-known/x402.json",
				"protocol": "x402",
			},
			{
				"id":       "capability-graph-registry",
				"name":     "ChainFX Agent Capability Graph Registry",
				"status":   "available",
				"manifest": base + "/.well-known/capability-graph-registry.json",
				"api":      base + "/agent/v1/graph-registry",
				"protocol": "chainfx-capability-graph",
			},
		},
		"trust": map[string]any{
			"jwks":           base + "/.well-known/jwks.json",
			"agent_card_sig": base + "/.well-known/agent-card.signature",
			"reputation":     base + "/.well-known/agent-reputation.json",
			"sla":            base + "/.well-known/agent-sla.json",
		},
		"planning": map[string]any{
			"capability_graph":          base + "/.well-known/capability-graph.json",
			"capability_compositions":   base + "/.well-known/capability-compositions.json",
			"planner_api":               base + "/agent/v1/plans",
			"capability_graph_registry": base + "/.well-known/capability-graph-registry.json",
		},
	}
}

func (s *Server) capabilityGraphRegistryDocument(base string) map[string]any {
	now := time.Now().UTC().Format(time.RFC3339)
	return map[string]any{
		"agent":       "ChainFX",
		"name":        "ChainFX Agent Capability Graph Registry",
		"description": "Relational registry that exposes how ChainFX trust, planning, payment, settlement and marketplace capabilities connect.",
		"version":     "1.0.0",
		"updated_at":  now,
		"agent_card":  base + "/.well-known/agent-card.json",
		"graph": map[string][]string{
			"payment":           {"quote_required_usdt", "pay_pix_with_usdt", "pay_card_bill_with_usdt", "get_payment_status"},
			"marketplace":       {"capability_exchange", "document_ocr", "llm_chat", "semantic_memory"},
			"stablecoin":        {"USDT", "USDC", "BSC", "stablecoin_exchange"},
			"trust":             {"jwks", "agent_card_signature", "agent_policy", "agent_sla", "agent_reputation"},
			"planning":          {"capability_graph_v2", "capability_compositions", "planner_api"},
			"observability":     {"agent_episodes", "episode_derived_reputation", "phase_reports"},
			"payment_protocols": {"a2a", "mcp", "x402"},
		},
		"relations": []map[string]any{
			{"from": "agent_card", "relation": "announces", "to": []string{"skills", "identity", "planning", "registries"}},
			{"from": "agent_card_signature", "relation": "verifies", "to": "agent_card"},
			{"from": "agent_policy", "relation": "gates", "to": []string{"pay_pix_with_usdt", "pay_card_bill_with_usdt", "stablecoin_exchange", "capability_purchase"}},
			{"from": "quote_required_usdt", "relation": "precedes", "to": []string{"pay_pix_with_usdt", "pay_card_bill_with_usdt"}},
			{"from": "pay_pix_with_usdt", "relation": "produces", "to": []string{"payment_intent", "payment_address", "required_usdt"}},
			{"from": "payment_intent", "relation": "observed_by", "to": "get_payment_status"},
			{"from": "document_ocr", "relation": "can_feed", "to": []string{"llm_chat", "semantic_memory"}},
			{"from": "llm_chat", "relation": "can_feed", "to": "semantic_memory"},
			{"from": "x402", "relation": "funds", "to": []string{"document_ocr", "llm_chat", "semantic_memory"}},
			{"from": "episodes", "relation": "derive", "to": []string{"agent_reputation", "skill_success_rate", "latency_percentiles", "failure_modes"}},
			{"from": "planner_api", "relation": "uses", "to": []string{"agent_policy", "capability_graph_v2", "capability_compositions", "agent_reputation"}},
		},
		"locators": map[string]string{
			"agent_card":              base + "/.well-known/agent-card.json",
			"jwks":                    base + "/.well-known/jwks.json",
			"signature":               base + "/.well-known/agent-card.signature",
			"reputation":              base + "/.well-known/agent-reputation.json",
			"sla":                     base + "/.well-known/agent-sla.json",
			"policy":                  base + "/.well-known/agent-policy.json",
			"capability_graph":        base + "/.well-known/capability-graph.json",
			"capability_compositions": base + "/.well-known/capability-compositions.json",
			"planner_api":             base + "/agent/v1/plans",
			"episodes":                base + "/agent/v1/episodes",
			"registries":              base + "/agent/v1/registries",
			"x402":                    base + "/.well-known/x402.json",
			"mcp":                     base + "/mcp/initialize",
			"a2a":                     base + "/a2a",
		},
		"provider_comparison": map[string]any{
			"identity":    base + "/.well-known/jwks.json",
			"reputation":  base + "/.well-known/agent-reputation.json",
			"sla":         base + "/.well-known/agent-sla.json",
			"graph":       base + "/.well-known/capability-graph.json",
			"graph_index": base + "/.well-known/capability-graph-registry.json",
			"decision_fields": []string{
				"reputation.score",
				"reputation.success_rate",
				"reputation.latency_ms.p95",
				"reputation.by_skill",
				"graph.payment",
				"graph.marketplace",
				"graph.trust",
			},
		},
		"phase_report": map[string]any{
			"id":                       "reputation_graph_registry_report",
			"phase":                    "3",
			"metrics_source":           "agent_episodes",
			"metrics_by_skill":         base + "/.well-known/agent-reputation.json",
			"episodes_aggregated":      base + "/agent/v1/episodes",
			"failures_by_type":         "reputation.failures_by_type",
			"latency_percentiles":      "reputation.latency_ms",
			"score_calculated":         "reputation.score",
			"graph_registry_published": true,
			"agent_qa_validation": []string{
				"episode_reputation_validated",
				"graph_registry_fetched",
				"graph_registry_validated",
			},
			"acceptance": "Another agent can compare ChainFX as a provider using episode-derived reputation and understand how payment, marketplace, stablecoin, trust and planning capabilities connect.",
		},
	}
}

func (s *Server) agntcyRecord(base string) (map[string]any, error) {
	record := s.oasfRecordPayload(base)
	signed, err := s.signRegistryPayload(base, record)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"schema":      "agntcy.oasf.agent_record",
		"record":      record,
		"provenance":  signed,
		"publishable": true,
	}, nil
}

func (s *Server) oasfRecord(base string) (map[string]any, error) {
	record := s.oasfRecordPayload(base)
	signed, err := s.signRegistryPayload(base, record)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"schema":     "oasf.agent",
		"record":     record,
		"provenance": signed,
	}, nil
}

func (s *Server) signedRegistryRecord(base, id string) (map[string]any, error) {
	record := s.oasfRecordPayload(base)
	signed, err := s.signRegistryPayload(base, record)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":         id,
		"record":     record,
		"provenance": signed,
		"index":      base + "/agent/v1/registries",
	}, nil
}

func (s *Server) oasfRecordPayload(base string) map[string]any {
	return map[string]any{
		"name":        "ChainFX Agent Pay",
		"displayName": "ChainFX Agent Pay",
		"description": "Verifiable A2A payment agent and capability network for autonomous agents using BSC stablecoins.",
		"version":     "1.0.0",
		"provider": map[string]any{
			"name": "ChainFX",
			"url":  "https://www.chainfx.store",
		},
		"locators": map[string]any{
			"a2a":            base + "/a2a",
			"a2a_tasks":      base + "/a2a/tasks",
			"mcp":            base + "/mcp/initialize",
			"openapi":        base + "/openapi.json",
			"x402":           base + "/.well-known/x402.json",
			"agent_card":     base + "/.well-known/agent-card.json",
			"registry":       base + "/agent/v1/registries",
			"agent_pay":      base + "/agent-pay.json",
			"capabilities":   base + "/marketplace/capabilities",
			"policy":         base + "/.well-known/agent-policy.json",
			"graph":          base + "/.well-known/capability-graph.json",
			"compositions":   base + "/.well-known/capability-compositions.json",
			"graph_registry": base + "/.well-known/capability-graph-registry.json",
			"planner":        base + "/agent/v1/plans",
		},
		"skills": []map[string]any{
			{"id": "pay_pix_with_usdt", "category": "payments", "protocols": []string{"a2a"}, "assets": []string{"USDT"}, "networks": []string{"BSC"}, "countries": []string{"BR"}},
			{"id": "pay_card_bill_with_usdt", "category": "payments", "protocols": []string{"a2a"}, "assets": []string{"USDT"}, "networks": []string{"BSC"}, "countries": []string{"BR"}},
			{"id": "stablecoin_exchange", "category": "settlement", "protocols": []string{"a2a", "rest"}, "assets": []string{"USDT", "USDC"}, "networks": []string{"BSC"}},
			{"id": "capability_exchange", "category": "marketplace", "protocols": []string{"a2a", "mcp", "rest", "x402"}},
			{"id": "document_ocr", "category": "capability", "protocols": []string{"a2a", "mcp", "x402"}},
			{"id": "llm_chat", "category": "capability", "protocols": []string{"a2a", "mcp", "x402"}},
			{"id": "semantic_memory", "category": "capability", "protocols": []string{"a2a", "mcp", "x402"}},
		},
		"capability_constraints": map[string]any{
			"auth":              []string{"bearer", "x402_payment"},
			"task_lifecycle":    []string{"submitted", "working", "input_required", "completed", "failed", "canceled", "rejected"},
			"supported_assets":  []string{"USDT", "USDC"},
			"supported_network": []string{"BSC"},
			"supported_country": []string{"BR"},
		},
		"trust": map[string]any{
			"identity":      s.agentIdentityMetadata(base),
			"jwks":          base + "/.well-known/jwks.json",
			"signature":     base + "/.well-known/agent-card.signature",
			"reputation":    base + "/.well-known/agent-reputation.json",
			"sla":           base + "/.well-known/agent-sla.json",
			"observability": base + "/agent/v1/episodes",
		},
		"planning": map[string]any{
			"policy_discovery": base + "/.well-known/agent-policy.json",
			"capability_graph": base + "/.well-known/capability-graph.json",
			"compositions":     base + "/.well-known/capability-compositions.json",
			"graph_registry":   base + "/.well-known/capability-graph-registry.json",
			"planner_api":      base + "/agent/v1/plans",
			"semantic_aliases": []string{"pay pix", "quote usdt", "stablecoin swap", "ocr", "llm chat", "semantic memory", "pay-per-call api"},
		},
		"economics": map[string]any{
			"agent_pay": map[string]any{
				"funding_asset":   "USDT",
				"funding_network": "BSC",
				"payment_methods": []string{"pix", "credit_card"},
				"fees_bps":        map[string]int{"pix": s.cfg.M2MPixFeeBps, "credit_card": s.cfg.M2MCreditFeeBps},
			},
			"x402": map[string]any{
				"status":     "available",
				"endpoint":   base + "/x402/capabilities/{capability}/execute",
				"asset":      "USDT",
				"network":    "BSC",
				"settlement": "ERC20 transfer receipt verification",
			},
		},
		"metadata": map[string]any{
			"release_channel": "production",
			"registry_ready":  true,
			"generated_at":    time.Now().UTC().Format(time.RFC3339),
		},
	}
}

func (s *Server) signRegistryPayload(base string, payload any) (map[string]any, error) {
	kid, _, priv := s.agentSigningMaterial(base)
	hash, canonical, err := canonicalJSONHash(payload)
	if err != nil {
		return nil, err
	}
	signature := ed25519.Sign(priv, canonical)
	return map[string]any{
		"algorithm":          agentIdentityAlg,
		"public_key_id":      kid,
		"jwks_url":           base + "/.well-known/jwks.json",
		"record_hash":        hash,
		"signature_encoding": "base64url",
		"signature":          base64.RawURLEncoding.EncodeToString(signature),
		"signed_at":          time.Now().UTC().Format(time.RFC3339),
	}, nil
}
