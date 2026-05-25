package viewer

import "testing"

// TestAnnotateCatalogCodes covers the section.index numbering that the
// overview matrix renders. Section order follows first appearance; the
// per-section index resets at each new section; a missing section maps to
// "(other)". (Previously untested — server.go finding #6.)
func TestAnnotateCatalogCodes(t *testing.T) {
	top := map[string]any{
		"scenarios": []any{
			map[string]any{"id": "a", "section": "Lifecycle"},
			map[string]any{"id": "b", "section": "Lifecycle"},
			map[string]any{"id": "c", "section": "Models"},
			map[string]any{"id": "d"}, // no section → "(other)"
			map[string]any{"id": "e", "section": "Lifecycle"},
		},
	}
	annotateCatalogCodes(top)

	want := map[string]string{
		"a": "1.1", // Lifecycle is section 1
		"b": "1.2",
		"c": "2.1", // Models is section 2
		"d": "3.1", // (other) is section 3
		"e": "1.3", // back to Lifecycle, index continues
	}
	for _, raw := range top["scenarios"].([]any) {
		sc := raw.(map[string]any)
		id := sc["id"].(string)
		got, _ := sc["code"].(string)
		if got != want[id] {
			t.Errorf("scenario %q code = %q; want %q", id, got, want[id])
		}
	}
}

// TestAnnotateCatalogCodes_gracefulOnBadShape: a payload without a
// scenarios array is left untouched rather than panicking.
func TestAnnotateCatalogCodes_gracefulOnBadShape(t *testing.T) {
	top := map[string]any{"version": 1}
	annotateCatalogCodes(top) // must not panic
	if _, ok := top["scenarios"]; ok {
		t.Error("did not expect a scenarios key to be created")
	}
}

// TestDeriveDisplayState pins the #476 rollup: the orthogonal daemon /
// driver capability facts plus the measured recording status collapse to
// one display state, with daemon problems outranking driver gaps.
func TestDeriveDisplayState(t *testing.T) {
	cases := []struct {
		name         string
		supports     string
		daemon       string
		driver       string
		hasRecording bool
		want         string
	}{
		{"agent lacks feature", "no", "full", "ready", true, "n.a."},
		{"support unknown", "unknown", "full", "ready", false, "unknown"},
		{"support empty defaults unknown", "", "full", "ready", false, "unknown"},
		{"daemon n/a frozen", "yes", "n/a", "ready", false, "n.a."},
		{"daemon incapable", "yes", "incapable", "ready", false, "unobservable"},
		{"daemon bug", "yes", "bug", "ready", true, "blocked-daemon"},
		{"driver gap", "yes", "full", "gap:sigkill", false, "blocked-driver"},
		{"daemon outranks driver gap", "yes", "bug", "gap:sigkill", false, "blocked-daemon"},
		{"incapable outranks driver gap", "yes", "incapable", "gap:keys", false, "unobservable"},
		{"daemon unknown", "yes", "unknown", "ready", false, "unknown"},
		{"daemon empty unknown", "yes", "", "ready", false, "unknown"},
		{"capable not yet recorded", "yes", "full", "ready", false, "pending-record"},
		{"capable and recorded", "yes", "full", "ready", true, "observed"},
		{"partial support, full+ready, recorded", "partial", "full", "ready", true, "observed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveDisplayState(tc.supports, tc.daemon, tc.driver, tc.hasRecording)
			if got != tc.want {
				t.Errorf("deriveDisplayState(%q,%q,%q,%v) = %q, want %q",
					tc.supports, tc.daemon, tc.driver, tc.hasRecording, got, tc.want)
			}
		})
	}
}

// TestAnnotateDisplayState confirms the annotation pass reads the measured
// recording status off the cell and writes a derived display_state.
func TestAnnotateDisplayState(t *testing.T) {
	top := map[string]any{
		"scenarios": []any{
			map[string]any{
				"id": "s1",
				"coverage": map[string]any{
					"claudecode": map[string]any{
						"agent_supports":    "yes",
						"daemon_capability": "full",
						"driver_capability": "ready",
						"measurement":       map[string]any{"status": "pass"},
					},
					"opencode": map[string]any{
						"agent_supports":    "yes",
						"daemon_capability": "full",
						"driver_capability": "ready",
						"measurement":       map[string]any{"status": "no_recording"},
					},
				},
			},
		},
	}
	annotateDisplayState(top)
	cov := top["scenarios"].([]any)[0].(map[string]any)["coverage"].(map[string]any)
	if got := cov["claudecode"].(map[string]any)["display_state"]; got != "observed" {
		t.Errorf("claudecode display_state = %v, want observed", got)
	}
	if got := cov["opencode"].(map[string]any)["display_state"]; got != "pending-record" {
		t.Errorf("opencode display_state = %v, want pending-record", got)
	}
}
