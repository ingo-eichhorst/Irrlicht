package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The consumer's contract, transcribed from Factory's
// crates/factory-agents/src/usage.rs at commit c3caaafe4
// (repos/ingo-eichhorst/factory) — the owner of schema 1. These are NOT the
// types herdrusage.go writes with; they are an independent mirror, so a field
// this mode adds, renames or retypes fails to decode below.
//
// Two places it is stricter than Factory, on purpose:
//   - DisallowUnknownFields: Factory's serde drops unknown fields, which is
//     what keeps transcript text out of its store; refusing them here is how
//     "usage fields only" is checked at the source.
//   - Counts are uint64 as in Rust (u64), so a negative count fails to decode.
type factoryTokenCounts struct {
	Input      *uint64 `json:"input"`
	Output     *uint64 `json:"output"`
	CacheRead  *uint64 `json:"cache_read"`
	CacheWrite *uint64 `json:"cache_write"`
}

type factoryUsageCost struct {
	USD           *float64 `json:"usd"`
	PricingSource *string  `json:"pricing_source"`
}

type factorySubagentUsage struct {
	SessionID string             `json:"session_id"`
	Tokens    factoryTokenCounts `json:"tokens"`
	Cost      factoryUsageCost   `json:"cost"`
}

type factoryRateLimitWindow struct {
	WindowMinutes *uint32  `json:"window_minutes"`
	UsedPercent   *float64 `json:"used_percent"`
	ResetsAt      *string  `json:"resets_at"`
}

type factoryRateLimit struct {
	Provider           *string                  `json:"provider"`
	PlanType           *string                  `json:"plan_type"`
	AttributionQuality *string                  `json:"attribution_quality"`
	Windows            []factoryRateLimitWindow `json:"windows"`
}

type factoryHarnessUsage struct {
	SessionID      string                 `json:"session_id"`
	Adapter        *string                `json:"adapter"`
	Model          *string                `json:"model"`
	Tokens         factoryTokenCounts     `json:"tokens"`
	Cost           factoryUsageCost       `json:"cost"`
	ElapsedSeconds *float64               `json:"elapsed_seconds"`
	ActiveSeconds  *float64               `json:"active_seconds"`
	Subagents      []factorySubagentUsage `json:"subagents"`
	RateLimit      *factoryRateLimit      `json:"rate_limit"`
	Unavailable    map[string]string      `json:"unavailable"`
}

type factorySessionUsage struct {
	Schema    uint32                `json:"schema"`
	PaneID    *string               `json:"pane_id"` // Rust: `handle`, alias "pane_id"
	SampledAt *string               `json:"sampled_at"`
	Sessions  []factoryHarnessUsage `json:"sessions"`
}

// TestHerdrUsage_GoldensMatchFactorysSchema decodes every committed golden
// into the mirror and checks what SessionUsage::parse checks: schema == 1.
// It also checks each `unavailable` key names a field that is null, since a
// reason for a value that is present would contradict the value.
func TestHerdrUsage_GoldensMatchFactorysSchema(t *testing.T) {
	goldens, err := filepath.Glob(filepath.Join("testdata", "herdr-usage", "*", "want*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(goldens) < 9 {
		t.Fatalf("found %d goldens, want every case's (9); the glob must have missed them", len(goldens))
	}
	for _, path := range goldens {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			var doc factorySessionUsage
			if err := dec.Decode(&doc); err != nil {
				t.Fatalf("does not match Factory's schema 1: %v", err)
			}
			if doc.Schema != 1 {
				t.Errorf("schema = %d, want 1 (SessionUsage::parse refuses anything else)", doc.Schema)
			}
			if doc.Sessions == nil {
				t.Error("sessions is absent; an empty pane must still say sessions: []")
			}
			var generic map[string]any
			if err := json.Unmarshal(raw, &generic); err != nil {
				t.Fatal(err)
			}
			sessions, _ := generic["sessions"].([]any)
			for i, s := range doc.Sessions {
				for key := range s.Unavailable {
					if v, ok := lookupDotted(sessions[i], key); !ok || v != nil {
						t.Errorf("sessions[%d].unavailable names %q, which is %v (present %t), not null", i, key, v, ok)
					}
				}
			}
		})
	}
}

// lookupDotted reads a dotted path such as "subagents.0.tokens.input" from a
// decoded JSON value. ok is false when the path does not exist.
func lookupDotted(v any, path string) (any, bool) {
	cur := v
	for _, part := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i := 0
			for _, c := range part {
				if c < '0' || c > '9' {
					return nil, false
				}
				i = i*10 + int(c-'0')
			}
			if i >= len(node) {
				return nil, false
			}
			cur = node[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// TestHerdrUsage_ContractMirrorRefusesStrayFields proves the mirror check
// can fail: a document with a non-usage field, and one with a negative count,
// are both refused.
func TestHerdrUsage_ContractMirrorRefusesStrayFields(t *testing.T) {
	for name, doc := range map[string]string{
		"transcript text":  `{"schema":1,"sessions":[{"session_id":"x","message":"hello"}]}`,
		"negative count":   `{"schema":1,"sessions":[{"session_id":"x","tokens":{"input":-1}}]}`,
		"top-level extras": `{"schema":1,"transcript":"x","sessions":[]}`,
	} {
		dec := json.NewDecoder(strings.NewReader(doc))
		dec.DisallowUnknownFields()
		var got factorySessionUsage
		if err := dec.Decode(&got); err == nil {
			t.Errorf("%s: the mirror accepted %s", name, doc)
		}
	}
}
