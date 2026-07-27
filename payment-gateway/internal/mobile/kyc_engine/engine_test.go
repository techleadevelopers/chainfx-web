package kyc_engine

import (
	"context"
	"testing"
)

func TestAnalyzeReturnsDeterministicEmbeddingAndLatency(t *testing.T) {
	engine := New("test-secret")
	input := Input{
		RequestID:       "req-1",
		UserID:          "user-1",
		Level:           1,
		DocumentURL:     "https://cdn/doc-front.jpg",
		DocumentBackURL: "https://cdn/doc-back.jpg",
		FacialVideoURL:  "https://cdn/face-video.mp4",
	}

	a := engine.Analyze(context.Background(), input)
	b := engine.Analyze(context.Background(), input)

	if a.EmbeddingHash == "" || a.EmbeddingHash != b.EmbeddingHash {
		t.Fatalf("embedding hash should be deterministic: %q != %q", a.EmbeddingHash, b.EmbeddingHash)
	}
	if len(a.Embedding) != 128 {
		t.Fatalf("expected 128 dims, got %d", len(a.Embedding))
	}
	if a.Score < 0 || a.Score > 100 {
		t.Fatalf("score out of range: %d", a.Score)
	}
	if a.LatencyMS < 0 {
		t.Fatalf("latency should be non-negative")
	}
}

func TestEncryptDecryptEmbedding(t *testing.T) {
	engine := New("test-secret")
	result := engine.Analyze(context.Background(), Input{UserID: "u", FacialVideoURL: "https://cdn/video.mp4"})
	encrypted, err := EncryptEmbedding("bio-secret", result.Embedding)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := DecryptEmbedding("bio-secret", encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if SimilarityPercent(result.Embedding, decrypted) != 100 {
		t.Fatalf("expected perfect similarity after decrypt")
	}
}
