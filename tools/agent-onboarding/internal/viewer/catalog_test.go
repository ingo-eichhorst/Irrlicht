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
