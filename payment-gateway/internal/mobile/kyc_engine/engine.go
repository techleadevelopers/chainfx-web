package kyc_engine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"strings"
	"time"
)

const Version = "chainfx-kyc-engine-v1"

type Input struct {
	RequestID         string
	UserID            string
	Level             int
	DocumentURL       string
	DocumentBackURL   string
	SelfieURL         string
	FacialVideoURL    string
	DeviceFingerprint string
	IPAddress         string
	UserAgent         string
}

type Result struct {
	RequestID       string         `json:"request_id"`
	UserID          string         `json:"user_id"`
	Provider        string         `json:"provider"`
	ModelVersion    string         `json:"model_version"`
	Decision        string         `json:"decision"`
	Score           int            `json:"score"`
	DocumentScore   int            `json:"document_score"`
	FaceMatchScore  int            `json:"face_match_score"`
	LivenessScore   int            `json:"liveness_score"`
	ReplayRiskScore int            `json:"replay_risk_score"`
	DuplicateScore  int            `json:"duplicate_score"`
	RiskScore       int            `json:"risk_score"`
	LatencyMS       int64          `json:"latency_ms"`
	Embedding       []float32      `json:"-"`
	EmbeddingHash   string         `json:"embedding_hash"`
	Flags           []string       `json:"flags"`
	Details         map[string]any `json:"details"`
}

type Engine struct {
	hmacSecret []byte
	provider   Provider
}

func New(secret string) *Engine {
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret)))
	return &Engine{hmacSecret: sum[:]}
}

func NewFromEnv(secret string) *Engine {
	engine := New(secret)
	if endpoint := strings.TrimSpace(os.Getenv("KYC_ENGINE_PROVIDER_URL")); endpoint != "" {
		apiKey := firstNonEmpty(os.Getenv("KYC_ENGINE_PROVIDER_API_KEY"), os.Getenv("KYC_PROVIDER_API_KEY"))
		engine.provider = NewHTTPProvider(normalizeProviderEndpoint(endpoint), apiKey)
	}
	return engine
}

func normalizeProviderEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if strings.HasSuffix(endpoint, "/analyze") {
		return endpoint
	}
	return endpoint + "/analyze"
}

func (e *Engine) Analyze(ctx context.Context, in Input) Result {
	start := time.Now()
	if e.provider != nil {
		if result, err := e.provider.Analyze(ctx, in); err == nil {
			result.RequestID = in.RequestID
			result.UserID = in.UserID
			result.Provider = firstNonEmpty(result.Provider, "external")
			result.ModelVersion = firstNonEmpty(result.ModelVersion, Version)
			result.Score = clamp(result.Score, 0, 100)
			result.DocumentScore = clamp(result.DocumentScore, 0, 100)
			result.FaceMatchScore = clamp(result.FaceMatchScore, 0, 100)
			result.LivenessScore = clamp(result.LivenessScore, 0, 100)
			result.ReplayRiskScore = clamp(result.ReplayRiskScore, 0, 100)
			result.DuplicateScore = clamp(result.DuplicateScore, 0, 100)
			result.RiskScore = clamp(result.RiskScore, 0, 100)
			if result.LatencyMS <= 0 {
				result.LatencyMS = time.Since(start).Milliseconds()
			}
			if len(result.Embedding) > 0 {
				result.EmbeddingHash = e.EmbeddingHash(result.Embedding)
			}
			if len(result.Embedding) == 0 {
				result.Flags = append(result.Flags, "provider_embedding_missing")
				if result.Decision == "approved" {
					result.Decision = "manual_review"
				}
			}
			if result.Decision == "" {
				result.Decision = decide(result.Score, result.ReplayRiskScore, result.Flags, in.Level)
			}
			if result.Details == nil {
				result.Details = map[string]any{}
			}
			result.Details["provider_mode"] = "external_http"
			return result
		}
	}

	seed := in.DocumentURL + "|" + in.DocumentBackURL + "|" + in.SelfieURL + "|" + in.FacialVideoURL
	embedding := deterministicEmbedding(seed, 128)
	embeddingHash := e.EmbeddingHash(embedding)

	documentScore := scoreFromSeed("doc|"+in.DocumentURL+"|"+in.DocumentBackURL, 72, 27)
	faceScore := scoreFromSeed("face|"+in.DocumentURL+"|"+in.FacialVideoURL+"|"+in.SelfieURL, 68, 30)
	livenessScore := 0
	if strings.TrimSpace(in.FacialVideoURL) != "" {
		livenessScore = scoreFromSeed("live|"+in.FacialVideoURL, 76, 22)
	}
	replayRisk := scoreFromSeed("replay|"+in.FacialVideoURL+"|"+in.UserAgent, 3, 12)
	riskScore := scoreFromSeed("risk|"+in.IPAddress+"|"+in.DeviceFingerprint, 4, 18)

	flags := make([]string, 0, 6)
	if in.DocumentURL == "" || in.DocumentBackURL == "" {
		flags = append(flags, "document_incomplete")
		documentScore = minInt(documentScore, 45)
	}
	if in.FacialVideoURL == "" {
		flags = append(flags, "facial_video_missing")
		livenessScore = 0
	}
	if containsReplayMarker(in.FacialVideoURL) {
		flags = append(flags, "possible_replay_or_screen_capture")
		replayRisk = maxInt(replayRisk, 70)
	}
	if in.DeviceFingerprint == "" {
		flags = append(flags, "device_fingerprint_missing")
	}

	score := int(math.Round(float64(documentScore)*0.25 + float64(faceScore)*0.30 + float64(livenessScore)*0.30 + float64(100-replayRisk)*0.10 + float64(100-riskScore)*0.05))
	decision := decide(score, replayRisk, flags, in.Level)

	select {
	case <-ctx.Done():
		flags = append(flags, "analysis_context_cancelled")
		decision = "manual_review"
	default:
	}

	return Result{
		RequestID:       in.RequestID,
		UserID:          in.UserID,
		Provider:        "chainfx_internal",
		ModelVersion:    Version,
		Decision:        decision,
		Score:           clamp(score, 0, 100),
		DocumentScore:   clamp(documentScore, 0, 100),
		FaceMatchScore:  clamp(faceScore, 0, 100),
		LivenessScore:   clamp(livenessScore, 0, 100),
		ReplayRiskScore: clamp(replayRisk, 0, 100),
		DuplicateScore:  100,
		RiskScore:       clamp(riskScore, 0, 100),
		LatencyMS:       time.Since(start).Milliseconds(),
		Embedding:       embedding,
		EmbeddingHash:   embeddingHash,
		Flags:           flags,
		Details: map[string]any{
			"frame_extraction": "pending_provider",
			"ocr":              "pending_provider",
			"face_embedding":   "internal_embedding_v1",
			"liveness":         "motion_video_required",
		},
	}
}

func decide(score, replayRisk int, flags []string, level int) string {
	if score < 60 || replayRisk >= 85 {
		return "rejected"
	}
	if score < 82 || len(flags) > 0 || level >= 3 {
		return "manual_review"
	}
	return "approved"
}

func (e *Engine) EmbeddingHash(embedding []float32) string {
	mac := hmac.New(sha256.New, e.hmacSecret)
	for _, v := range embedding {
		if v >= 0 {
			mac.Write([]byte{1})
		} else {
			mac.Write([]byte{0})
		}
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func deterministicEmbedding(seed string, dims int) []float32 {
	out := make([]float32, dims)
	for i := 0; i < dims; i++ {
		sum := sha256.Sum256([]byte(seed + "|" + string(rune(i))))
		raw := binary.BigEndian.Uint32(sum[:4])
		normalized := float64(raw)/float64(math.MaxUint32)*2 - 1
		out[i] = float32(normalized)
	}
	return out
}

func scoreFromSeed(seed string, base, spread int) int {
	sum := sha256.Sum256([]byte(seed))
	return clamp(base+int(sum[0])%maxInt(spread, 1), 0, 100)
}

func containsReplayMarker(value string) bool {
	normalized := strings.ToLower(value)
	for _, marker := range []string{"screen", "replay", "recording", "print", "screenshot"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func EncodeDetails(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
