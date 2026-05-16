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

// TestComputeMetrics_NullsStaleSnapshot wires the stale-check through
// the real TailAndProcess path: ingest a snapshot with a past
// resets_at, run a pass against an empty transcript, and assert the
// surfaced metrics.RateLimit is nil. Catches regressions where the
// stale logic is renamed/moved but the call-site isn't updated.
func TestComputeMetrics_NullsStaleSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tt := NewTranscriptTailer(path, nil, "test")
	staleResets := time.Now().Add(-1 * time.Hour).Unix()
	tt.IngestRateLimit(&RateLimitSnapshot{
		SampledAt: time.Now().Add(-2 * time.Hour).Unix(),
		Windows: []RateLimitWindow{
			{UsedPercent: 47, WindowMinutes: 300, ResetsAt: staleResets},
		},
	})
	m, err := tt.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m.RateLimit != nil {
		t.Errorf("expected stale snapshot to be nulled, got %+v", m.RateLimit)
	}
	if len(m.RateLimitHistory) != 0 {
		t.Errorf("expected history to be cleared, got %d entries", len(m.RateLimitHistory))
	}
}

func TestRateLimitHasStaleWindow(t *testing.T) {
	now := time.Unix(2000, 0)
	cases := []struct {
		name string
		snap *RateLimitSnapshot
		want bool
	}{
		{"nil snapshot", nil, false},
		{"no windows", &RateLimitSnapshot{}, false},
		{"all future resets", &RateLimitSnapshot{Windows: []RateLimitWindow{
			{ResetsAt: 3000}, {ResetsAt: 4000},
		}}, false},
		{"any past reset triggers stale", &RateLimitSnapshot{Windows: []RateLimitWindow{
			{ResetsAt: 1000}, {ResetsAt: 4000},
		}}, true},
		{"all past", &RateLimitSnapshot{Windows: []RateLimitWindow{
			{ResetsAt: 1000}, {ResetsAt: 1500},
		}}, true},
		{"zero ResetsAt ignored (treated as no data)", &RateLimitSnapshot{Windows: []RateLimitWindow{
			{ResetsAt: 0}, {ResetsAt: 0},
		}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rateLimitHasStaleWindow(tc.snap, now); got != tc.want {
				t.Errorf("rateLimitHasStaleWindow = %v, want %v", got, tc.want)
			}
		})
	}
}
