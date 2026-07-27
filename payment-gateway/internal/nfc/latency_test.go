package nfc

import (
	"sort"
	"testing"
	"time"
)

func TestTokenLatencyPercentiles(t *testing.T) {
	const (
		sampleCount = 1000
		batchSize   = 100
	)
	secret := "test_nfc_secret_32_chars_minimum"
	now := time.Unix(1_700_000_000, 0)
	samples := make([]time.Duration, 0, sampleCount)

	for i := 0; i < sampleCount; i++ {
		start := time.Now()
		for j := 0; j < batchSize; j++ {
			token, _, err := IssueToken(secret, "0x742d35cc6634c0532925a3b844bc454e4438f44e", "android-hce-test", "BSC", 2*time.Minute, now)
			if err != nil {
				t.Fatalf("IssueToken() error = %v", err)
			}
			if _, err := VerifyToken(secret, token, now.Add(time.Second)); err != nil {
				t.Fatalf("VerifyToken() error = %v", err)
			}
		}
		samples = append(samples, time.Since(start)/batchSize)
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("nfc token issue+verify latency per operation: samples=%d batch_size=%d total_ops=%d p50=%s p55=%s p95=%s p99=%s max=%s",
		sampleCount,
		batchSize,
		sampleCount*batchSize,
		percentile(samples, 50),
		percentile(samples, 55),
		percentile(samples, 95),
		percentile(samples, 99),
		samples[len(samples)-1],
	)
}

func percentile(samples []time.Duration, pct int) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	idx := (len(samples)*pct + 99) / 100
	if idx <= 0 {
		idx = 1
	}
	if idx > len(samples) {
		idx = len(samples)
	}
	return samples[idx-1]
}
