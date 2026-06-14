package codex

import (
	"math"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
)

func TestCostUSD(t *testing.T) {
	tests := []struct {
		name  string
		model runtime.Model
		usage runtime.Usage
		want  float64
	}{
		{
			// 6_654 fresh in × $5/M + 3_456 cache × $0.50/M + 35 out × $30/M.
			name:  "gpt-5.5 mixed cache hit",
			model: "gpt-5.5",
			usage: runtime.Usage{InputTokens: 10110, CacheReadTokens: 3456, OutputTokens: 35},
			want:  6654*5.00/1e6 + 3456*0.50/1e6 + 35*30.00/1e6,
		},
		{
			name:  "gpt-5.4-mini all fresh",
			model: "gpt-5.4-mini",
			usage: runtime.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
			want:  0.75 + 4.50,
		},
		{
			name:  "gpt-5.4 cache exceeds input — treat fresh as zero",
			model: "gpt-5.4",
			usage: runtime.Usage{InputTokens: 100, CacheReadTokens: 500, OutputTokens: 0},
			want:  500 * 0.25 / 1e6,
		},
		{
			name:  "unknown model returns 0",
			model: "gpt-9000",
			usage: runtime.Usage{InputTokens: 1000, OutputTokens: 1000},
			want:  0,
		},
		{
			name:  "zero usage returns 0",
			model: "gpt-5.5",
			usage: runtime.Usage{},
			want:  0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := costUSD(tc.model, tc.usage)
			if math.Abs(got-tc.want) > 1e-12 {
				t.Fatalf("costUSD = %v, want %v", got, tc.want)
			}
		})
	}
}
