package mistralvibe

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// The three bodies issue #2012's live probe captured on 2026-11-21 in the
// maintainer's Chrome (comment 7063552453). The two 200 bodies are byte-exact
// reconstructions: their lengths match the measured 108 and 115 bytes. The
// 401 body is a SHAPE: the probe measured 16 bytes, this spelling is 15, and
// the whitespace was not captured.
const (
	probeBefore   = "testdata/live_probe_2026-11-21T133745Z_usage.json"
	probeAfter    = "testdata/live_probe_2026-11-21T133833Z_usage_after_one_turn.json"
	probeNoSesson = "testdata/live_probe_2026-11-21T134412Z_no_session_401.shape.json"
)

var probeFetchedAt = time.Date(2026, 11, 21, 13, 38, 33, 0, time.UTC)

func readProbe(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return b
}

// TestLiveProbe_BodiesAreTheMeasuredBytes reads the probe's provenance
// record (testdata/live_probe_2026-11-21.record.json) and holds each body to
// it: a byte-exact capture must be exactly the measured length, so a
// reformat, a trailing newline or a hand edit fails here, and a body the
// record calls a SHAPE must say so rather than pass for a capture.
func TestLiveProbe_BodiesAreTheMeasuredBytes(t *testing.T) {
	var record struct {
		Captures []struct {
			Status        int    `json:"status"`
			MeasuredBytes int    `json:"measured_bytes"`
			File          string `json:"file"`
			Fidelity      string `json:"fidelity"`
		} `json:"captures"`
	}
	if err := json.Unmarshal(readProbe(t, "testdata/live_probe_2026-11-21.record.json"), &record); err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(record.Captures) != 3 {
		t.Fatalf("record lists %d captures, want 3", len(record.Captures))
	}
	exact := 0
	for _, c := range record.Captures {
		got := len(readProbe(t, "testdata/"+c.File))
		switch {
		case strings.HasPrefix(c.Fidelity, "byte-exact"):
			exact++
			if got != c.MeasuredBytes {
				t.Errorf("%s is %d bytes, but the probe measured %d", c.File, got, c.MeasuredBytes)
			}
		case strings.HasPrefix(c.Fidelity, "SHAPE"):
			if c.Status != 401 || !strings.Contains(c.File, ".shape.") {
				t.Errorf("%s: only the 401 body is a shape, and its file name must say so", c.File)
			}
		default:
			t.Errorf("%s: fidelity %q is neither byte-exact nor SHAPE", c.File, c.Fidelity)
		}
	}
	if exact != 2 {
		t.Fatalf("%d byte-exact captures, want the two 200 bodies", exact)
	}
}

// TestBuildSnapshot_ReadsUsagePercentageAsAPercentage is the target of
// mutation 5 (#2012 testing strategy): 0.081567 is 0.08 %, and a parser that
// read it as a fraction would publish 8 %.
func TestBuildSnapshot_ReadsUsagePercentageAsAPercentage(t *testing.T) {
	snap, err := BuildSnapshot(readProbe(t, probeAfter), probeFetchedAt)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	if len(snap.Windows) != 1 {
		t.Fatalf("windows = %+v, want exactly one", snap.Windows)
	}
	if got := snap.Windows[0].UsedPercent; got != 0.081567 {
		t.Fatalf("UsedPercent = %v, want 0.081567 — usage_percentage was read as a fraction and rescaled", got)
	}
	before, err := BuildSnapshot(readProbe(t, probeBefore), probeFetchedAt)
	if err != nil {
		t.Fatalf("BuildSnapshot(before): %v", err)
	}
	if got := before.Windows[0].UsedPercent; got != 0 {
		t.Fatalf("UsedPercent(before) = %v, want 0", got)
	}
}

// TestBuildSnapshot_IsOneMonthlyAllowanceWindow: the reading is a monthly
// window ending at reset_at, with the stamps #2012's design names.
func TestBuildSnapshot_IsOneMonthlyAllowanceWindow(t *testing.T) {
	snap, err := BuildSnapshot(readProbe(t, probeAfter), probeFetchedAt)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	w := snap.Windows[0]
	reset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if w.ResetsAt != reset.Unix() {
		t.Errorf("ResetsAt = %d, want %d (2026-12-01T00:00:00Z)", w.ResetsAt, reset.Unix())
	}
	if w.WindowMinutes != 30*24*60 {
		t.Errorf("WindowMinutes = %d, want 43200 (November 2026, the month ending at the reset)", w.WindowMinutes)
	}
	if !w.IsSubscriptionAllowance() || w.LimitKind != session.LimitKindAllowance {
		t.Errorf("LimitKind = %q, want an explicit allowance", w.LimitKind)
	}
	if w.Measure != "" {
		t.Errorf("Measure = %q, want empty: the response names no unit", w.Measure)
	}
	want := session.RateLimitSnapshot{
		Provider: session.ProviderMistral, Product: "mistral-vibe", AttributionEvidence: "user-asserted",
		ObservationSource: "browser_backed_api", AttributionQuality: session.AttributionQualityConfirmed,
	}
	if snap.Provider != want.Provider || snap.Product != want.Product || snap.AttributionEvidence != want.AttributionEvidence ||
		snap.ObservationSource != want.ObservationSource || snap.AttributionQuality != want.AttributionQuality {
		t.Errorf("stamps = %+v, want %+v", snap, want)
	}
	if snap.ConfirmedAccountRef != "" || snap.QuotaID != "" {
		t.Errorf("ConfirmedAccountRef=%q QuotaID=%q, want both empty: the response names no account", snap.ConfirmedAccountRef, snap.QuotaID)
	}
	if snap.SampledAt != probeFetchedAt.Unix() || snap.LastSuccessAt != probeFetchedAt.Unix() {
		t.Errorf("SampledAt=%d LastSuccessAt=%d, want the fetch time %d", snap.SampledAt, snap.LastSuccessAt, probeFetchedAt.Unix())
	}
	if snap.IsUnattributed() {
		t.Error("the snapshot is unattributed, so the repository would drop it on every load")
	}
}

func TestParseUsage_RefusesWhatItCannotTrust(t *testing.T) {
	cases := []struct {
		name, body string
		want       error
	}{
		{"above 100", `[{"result":{"data":{"json":{"usage_percentage":100.5}}}}]`, ErrOutOfRange},
		{"negative", `[{"result":{"data":{"json":{"usage_percentage":-0.1}}}}]`, ErrOutOfRange},
		{"absent", `[{"result":{"data":{"json":{"payg_enabled":false}}}}]`, ErrNoUsage},
		{"null", `[{"result":{"data":{"json":{"usage_percentage":null}}}}]`, ErrNoUsage},
		{"tRPC error element", `[{"error":{"json":{"message":"UNAUTHORIZED","code":-32001}}}]`, ErrUnexpectedShape},
		{"the 401 body's shape", `{"detail":null}`, ErrUnexpectedShape},
		{"empty batch", `[]`, ErrUnexpectedShape},
		{"two elements", `[{"result":{"data":{"json":{"usage_percentage":1}}}},{"result":{"data":{"json":{"usage_percentage":2}}}}]`, ErrUnexpectedShape},
		{"reset_at not RFC 3339", `[{"result":{"data":{"json":{"usage_percentage":1,"reset_at":"December 1"}}}}]`, ErrUnexpectedShape},
		{"not JSON", `<html>`, ErrUnexpectedShape},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseUsage([]byte(tc.body)); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if _, err := BuildSnapshot([]byte(tc.body), probeFetchedAt); err == nil {
				t.Fatal("BuildSnapshot succeeded on a body ParseUsage refuses")
			}
		})
	}
	if _, err := ParseUsage(readProbe(t, probeNoSesson)); !errors.Is(err, ErrUnexpectedShape) {
		t.Fatalf("the committed 401 shape parsed as usage: %v", err)
	}
}

// TestParseUsage_BoundsAndMissingReset: 0 and 100 are in range, and a
// missing reset_at stays missing (ResetsAt 0) with a nominal month.
func TestParseUsage_BoundsAndMissingReset(t *testing.T) {
	for _, body := range []string{
		`[{"result":{"data":{"json":{"usage_percentage":0}}}}]`,
		`[{"result":{"data":{"json":{"usage_percentage":100}}}}]`,
	} {
		snap, err := BuildSnapshot([]byte(body), probeFetchedAt)
		if err != nil {
			t.Fatalf("BuildSnapshot(%s): %v", body, err)
		}
		if snap.Windows[0].ResetsAt != 0 || snap.Windows[0].WindowMinutes != nominalMonthMinutes {
			t.Errorf("missing reset_at: ResetsAt=%d WindowMinutes=%d", snap.Windows[0].ResetsAt, snap.Windows[0].WindowMinutes)
		}
	}
}

// TestRedactUsage_KeepsOnlyTheThreeFields: CodexBar's test fixture carries a
// fourth field (quota_changed_this_month); the allowlist drops it, and the
// result is the object replaydata/providers/mistral's fixture holds.
func TestRedactUsage_KeepsOnlyTheThreeFields(t *testing.T) {
	raw := `[{"result":{"data":{"json":{"usage_percentage":2.81,"quota_changed_this_month":false,"payg_enabled":false,"reset_at":"2026-07-01T00:00:00Z","email":"x@example.com"}}}}]`
	got, err := RedactUsage([]byte(raw))
	if err != nil {
		t.Fatalf("RedactUsage: %v", err)
	}
	if want := `{"usage_percentage":2.81,"payg_enabled":false,"reset_at":"2026-07-01T00:00:00Z"}`; string(got) != want {
		t.Fatalf("RedactUsage = %s, want %s", got, want)
	}
	fromProbe, err := RedactUsage(readProbe(t, probeAfter))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fromProbe), `"usage_percentage":0.081567`) {
		t.Fatalf("RedactUsage(probe) = %s", fromProbe)
	}
}
