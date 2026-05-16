package tailer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTailerForRateLimitTest(t *testing.T) *TranscriptTailer {
	t.Helper()
	// Path doesn't need to exist for IngestRateLimit / ingestRateLimit —
	// they don't touch the file system. Parser is unused but must be non-nil
	// for the constructor's invariants.
	return NewTranscriptTailer("/nonexistent", nil, "test")
}

func TestIngestRateLimit_FirstSampleAppended(t *testing.T) {
	tt := newTailerForRateLimitTest(t)
	snap := &RateLimitSnapshot{
		SampledAt: 1000,
		Windows:   []RateLimitWindow{{UsedPercent: 30, WindowMinutes: 300, ResetsAt: 2000}},
	}
	tt.IngestRateLimit(snap)
	if tt.rateLimit != snap {
		t.Fatal("expected latest snapshot to be retained")
	}
	if len(tt.rateLimitHistory) != 1 {
		t.Fatalf("expected history length 1, got %d", len(tt.rateLimitHistory))
	}
}

func TestIngestRateLimit_DuplicateNotAppended(t *testing.T) {
	tt := newTailerForRateLimitTest(t)
	snap1 := &RateLimitSnapshot{
		SampledAt: 1000,
		Windows:   []RateLimitWindow{{UsedPercent: 30, WindowMinutes: 300, ResetsAt: 2000}},
	}
	snap2 := &RateLimitSnapshot{
		SampledAt: 1100, // different timestamp, same readings
		Windows:   []RateLimitWindow{{UsedPercent: 30, WindowMinutes: 300, ResetsAt: 2000}},
	}
	tt.IngestRateLimit(snap1)
	tt.IngestRateLimit(snap2)
	if len(tt.rateLimitHistory) != 1 {
		t.Fatalf("expected duplicate to be deduped, history len = %d", len(tt.rateLimitHistory))
	}
}

func TestIngestRateLimit_RolloverResetsHistory(t *testing.T) {
	tt := newTailerForRateLimitTest(t)
	tt.IngestRateLimit(&RateLimitSnapshot{
		SampledAt: 1000,
		Windows:   []RateLimitWindow{{UsedPercent: 80, WindowMinutes: 300, ResetsAt: 2000}},
	})
	tt.IngestRateLimit(&RateLimitSnapshot{
		SampledAt: 1500,
		Windows:   []RateLimitWindow{{UsedPercent: 95, WindowMinutes: 300, ResetsAt: 2000}},
	})
	// Window rolls over: ResetsAt advances, percent drops back near zero.
	tt.IngestRateLimit(&RateLimitSnapshot{
		SampledAt: 2100,
		Windows:   []RateLimitWindow{{UsedPercent: 2, WindowMinutes: 300, ResetsAt: 20000}},
	})
	if len(tt.rateLimitHistory) != 1 {
		t.Fatalf("expected history reset on rollover; got %d entries", len(tt.rateLimitHistory))
	}
	if tt.rateLimitHistory[0].SampledAt != 2100 {
		t.Errorf("expected post-rollover history to start at the new sample, got SampledAt=%d", tt.rateLimitHistory[0].SampledAt)
	}
}

func TestIngestRateLimit_HistoryCappedAtFive(t *testing.T) {
	tt := newTailerForRateLimitTest(t)
	for i := range 10 {
		tt.IngestRateLimit(&RateLimitSnapshot{
			SampledAt: int64(1000 + i*60),
			Windows:   []RateLimitWindow{{UsedPercent: float64(i + 1), WindowMinutes: 300, ResetsAt: 99999}},
		})
	}
	if len(tt.rateLimitHistory) != rateLimitHistoryCap {
		t.Fatalf("expected history capped at %d, got %d", rateLimitHistoryCap, len(tt.rateLimitHistory))
	}
	// Oldest dropped: the first retained sample is i=5 → UsedPercent=6.
	if tt.rateLimitHistory[0].Windows[0].UsedPercent != 6 {
		t.Errorf("expected oldest dropped, first window pct = %v", tt.rateLimitHistory[0].Windows[0].UsedPercent)
	}
}

func TestIngestRateLimit_NilNoOp(t *testing.T) {
	tt := newTailerForRateLimitTest(t)
	tt.IngestRateLimit(nil)
	if tt.rateLimit != nil || len(tt.rateLimitHistory) != 0 {
		t.Fatal("nil snapshot must be a no-op")
	}
}

// TestComputeMetrics_PreservesStaleSnapshot pins the post-iteration
// behaviour: the daemon used to null stale snapshots, but that left
// the macOS overlay header empty when Claude Code's statusline
// stuttered. The UI now decorates stale data with a dimmer chip; the
// daemon must surface the snapshot unchanged so the UI has data to
// decorate.
func TestComputeMetrics_PreservesStaleSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tt := NewTranscriptTailer(path, nil, "test")
	staleResets := time.Now().Add(-1 * time.Hour).Unix()
	snap := &RateLimitSnapshot{
		SampledAt: time.Now().Add(-2 * time.Hour).Unix(),
		Windows: []RateLimitWindow{
			{UsedPercent: 47, WindowMinutes: 300, ResetsAt: staleResets},
		},
	}
	tt.IngestRateLimit(snap)
	m, err := tt.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m.RateLimit == nil {
		t.Fatal("expected stale snapshot to be preserved on metrics, got nil")
	}
	if m.RateLimit.Windows[0].UsedPercent != 47 {
		t.Errorf("unexpected snapshot mutation: %+v", m.RateLimit)
	}
}
