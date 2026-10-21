package accountfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/ports/outbound"
)

func parseRef(b []byte) (outbound.HomeAccountObservation, bool) {
	if len(b) == 0 || b[0] == '{' && len(b) < 2 {
		return outbound.HomeAccountObservation{}, false
	}
	return outbound.HomeAccountObservation{Ref: string(b)}, true
}

// A2-4 (red-first): a removed identity file (a logout that deletes it) is an
// answer — no account — not a transient failure that keeps the old account.
func TestCache_RemovedFileReportsNoAccount(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(p, []byte("acct-X"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCache(OSFS())
	if obs, ok := c.Lookup(p, parseRef); !ok || obs.Ref != "acct-X" {
		t.Fatalf("setup: obs=%+v ok=%v", obs, ok)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	obs, ok := c.Lookup(p, parseRef)
	if !ok || obs.Ref != "" || obs.Home != p || obs.ModTime != 0 {
		t.Errorf("after removal: obs=%+v ok=%v, want ok with empty ref, Home set, ModTime 0", obs, ok)
	}
	// The entry is gone: a recreated file is read afresh.
	if err := os.WriteFile(p, []byte("acct-Y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if obs, _ := c.Lookup(p, parseRef); obs.Ref != "acct-Y" {
		t.Errorf("after recreate: ref = %q, want acct-Y", obs.Ref)
	}
}

// Lock: any other stat error keeps the last good observation.
func TestCache_OtherStatErrorKeepsLastGood(t *testing.T) {
	fail := false
	fs := OSFS()
	real := fs.Stat
	fs.Stat = func(p string) (int64, int64, error) {
		if fail {
			return 0, 0, errors.New("EIO")
		}
		return real(p)
	}
	p := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(p, []byte("acct-X"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCache(fs)
	c.Lookup(p, parseRef)
	fail = true
	if obs, ok := c.Lookup(p, parseRef); !ok || obs.Ref != "acct-X" {
		t.Errorf("obs=%+v ok=%v, want last good acct-X", obs, ok)
	}
}
