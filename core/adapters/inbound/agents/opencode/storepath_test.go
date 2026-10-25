package opencode

import (
	"path/filepath"
	"testing"
)

// TestStorePath_HonoursOpencodesOverrides is #2106's red-first test for the
// store path. opencode resolves its database as `OPENCODE_DB ?? "opencode.db"`
// resolved against its data dir, `$XDG_DATA_HOME || ~/.local/share` joined
// with "opencode", ":memory:" excepted — read out of the 2.1.4 bundle with
// `strings` (`function LD(o){let e=process.env.OPENCODE_DB??(…"opencode.db"…);
// return e===":memory:"?e:r.resolve(o,e)}`, called as `LD(o.data)`). On main
// StorePath ignored both variables, so the daemon watched a database opencode
// was not writing.
func TestStorePath_HonoursOpencodesOverrides(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	custom := filepath.Join(t.TempDir(), "elsewhere.db")
	defaultPath := filepath.Join(home, ".local", "share", "opencode", "opencode.db")

	for _, c := range []struct {
		name, db, xdgData, want string
	}{
		{"neither", "", "", defaultPath},
		{"xdg_data_home", "", xdg, filepath.Join(xdg, "opencode", "opencode.db")},
		{"absolute_opencode_db", custom, xdg, custom},
		{"relative_opencode_db_joins_the_data_dir", "named.db", xdg, filepath.Join(xdg, "opencode", "named.db")},
		{"relative_opencode_db_without_xdg", "named.db", "", filepath.Join(home, ".local", "share", "opencode", "named.db")},
		{"in_memory_names_no_file", ":memory:", "", defaultPath},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("OPENCODE_DB", c.db)
			t.Setenv("XDG_DATA_HOME", c.xdgData)
			if got := StorePath(); got != c.want {
				t.Errorf("StorePath() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestStorePath_HookKeyFollowsTheOverride is a lock on #1361 under the new
// resolution: with the store relocated, the hook receiver's key and the
// watcher's key are still one string.
func TestStorePath_HookKeyFollowsTheOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENCODE_DB", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if hook, watcher := transcriptPathFor("ses_x"), New(0).transcriptPathFor("ses_x"); hook != watcher {
		t.Errorf("hook key %q != watcher key %q", hook, watcher)
	}
}
