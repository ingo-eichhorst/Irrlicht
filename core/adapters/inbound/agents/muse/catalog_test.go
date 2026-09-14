package muse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeCatalog writes one model-catalog JSON file under
// home/.local/share/muse/model-catalog/, mirroring the real on-disk shape
// (top-level "rows" array) confirmed live on this machine — see catalog.go's
// doc comment. filename lets a test create more than one file in the
// directory, the way a real install could.
func writeCatalog(t *testing.T, home, filename string, rows ...modelCatalogRow) {
	t.Helper()
	dir := filepath.Join(home, catalogRootDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(modelCatalogFile{Rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// setHome points $HOME at a fresh, empty temp dir for the duration of the
// test — every contextWindowForModel test must isolate $HOME, or it silently
// reads whatever real ~/.local/share/muse/model-catalog/ happens to exist on
// the machine running the test (it does on this one) instead of the
// controlled fixture the test actually sets up.
func setHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestContextWindowForModel_MatchFound(t *testing.T) {
	home := setHome(t)
	writeCatalog(t, home, "catalog.json",
		modelCatalogRow{ModelID: "muse-spark-1.3", ContextLimit: 500000},
		modelCatalogRow{ModelID: "muse-spark-1.3-contributor", ContextLimit: 1007997},
	)
	got, ok := contextWindowForModel("muse-spark-1.3-contributor")
	if !ok || got != 1007997 {
		t.Errorf("contextWindowForModel = (%d, %v), want (1007997, true)", got, ok)
	}
}

// TestContextWindowForModel_NoDirectory pins the honest-degrade path this
// fix is required to take (per the model-context-display cell's caveats):
// no catalog directory at all must read exactly like "no context window
// available", never like a fabricated zero-width window.
func TestContextWindowForModel_NoDirectory(t *testing.T) {
	setHome(t) // empty temp $HOME — no ~/.local/share/muse/model-catalog/ at all
	got, ok := contextWindowForModel("muse-spark-1.3-contributor")
	if ok {
		t.Errorf("contextWindowForModel = (%d, true), want ok=false when the catalog directory doesn't exist", got)
	}
}

// TestContextWindowForModel_NoMatchingRow pins the same honest-degrade path
// for a catalog that exists but simply doesn't know this model — e.g. a
// stale cache from before a model switch.
func TestContextWindowForModel_NoMatchingRow(t *testing.T) {
	home := setHome(t)
	writeCatalog(t, home, "catalog.json", modelCatalogRow{ModelID: "some-other-model", ContextLimit: 200000})
	got, ok := contextWindowForModel("muse-spark-1.3-contributor")
	if ok {
		t.Errorf("contextWindowForModel = (%d, true), want ok=false for an unmatched model", got)
	}
}

// TestContextWindowForModel_MalformedFileSkipped confirms one unparseable
// file in the directory doesn't abort the scan — a real install could carry
// a partially-written or corrupt file alongside a good one.
func TestContextWindowForModel_MalformedFileSkipped(t *testing.T) {
	home := setHome(t)
	dir := filepath.Join(home, catalogRootDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCatalog(t, home, "good.json", modelCatalogRow{ModelID: "muse-spark-1.3-contributor", ContextLimit: 1007997})

	got, ok := contextWindowForModel("muse-spark-1.3-contributor")
	if !ok || got != 1007997 {
		t.Errorf("contextWindowForModel = (%d, %v), want (1007997, true) — the malformed sibling file must not block the good one", got, ok)
	}
}

// TestContextWindowForModel_ZeroLimitNotTrusted guards against a row present
// but carrying a zero or negative context_limit (e.g. a field muse hasn't
// resolved yet) being reported as a real answer.
func TestContextWindowForModel_ZeroLimitNotTrusted(t *testing.T) {
	home := setHome(t)
	writeCatalog(t, home, "catalog.json", modelCatalogRow{ModelID: "muse-spark-1.3-contributor", ContextLimit: 0})
	got, ok := contextWindowForModel("muse-spark-1.3-contributor")
	if ok {
		t.Errorf("contextWindowForModel = (%d, true), want ok=false for a zero context_limit row", got)
	}
}

func TestContextWindowForModel_EmptyModel(t *testing.T) {
	home := setHome(t)
	writeCatalog(t, home, "catalog.json", modelCatalogRow{ModelID: "muse-spark-1.3-contributor", ContextLimit: 1007997})
	got, ok := contextWindowForModel("")
	if ok {
		t.Errorf("contextWindowForModel(\"\") = (%d, true), want ok=false", got)
	}
}
