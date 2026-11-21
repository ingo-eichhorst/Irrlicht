package settingsenv

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ReadEnv returns only the keys it is asked for, so the endpoint
// permission's caller can never receive the token in the same env block.
func TestReadEnv_ReturnsOnlyNamedKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.local.json")
	write(t, p, `{"env":{"ANTHROPIC_BASE_URL":"https://api.z.ai/api/anthropic","ANTHROPIC_AUTH_TOKEN":"sk-secret","N":3},"permissions":{}}`)

	got, err := ReadEnv(p, map[string]struct{}{"ANTHROPIC_BASE_URL": {}})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"ANTHROPIC_BASE_URL": "https://api.z.ai/api/anthropic"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadEnv = %v, want %v (no other key, the token least of all)", got, want)
	}
}

func TestReadEnv_MissingFileIsNotExist(t *testing.T) {
	_, err := ReadEnv(filepath.Join(t.TempDir(), "nope.json"), map[string]struct{}{"K": {}})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestReadEnv_EmptyValueIsPresent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	write(t, p, `{"env":{"K":""}}`)
	got, err := ReadEnv(p, map[string]struct{}{"K": {}})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got["K"]; !ok || v != "" {
		t.Fatalf("got %v, want K present and empty", got)
	}
}

func TestReadEnv_OversizedFileIsUnreadable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	big := make([]byte, maxSettingsBytes+10)
	for i := range big {
		big[i] = ' '
	}
	copy(big, `{"env":{}}`)
	write(t, p, string(big))
	if _, err := ReadEnv(p, map[string]struct{}{"K": {}}); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want an unreadable (not missing) error", err)
	}
}

func TestFlagsTouchSettings(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want bool
	}{
		{[]string{"claude", "-p", "hi"}, false},
		{[]string{"claude", "-p", "explain --settings please"}, false},
		{[]string{"claude", "--settings", "{}"}, true},
		{[]string{"node", "cli.js", "--settings=/x.json"}, true},
		{[]string{"claude", "--setting-sources", "user,project"}, true},
		{[]string{"claude", "--setting-sources=user"}, true},
		{[]string{"claude", "--settingsx"}, false},
	} {
		if got := FlagsTouchSettings(tc.argv); got != tc.want {
			t.Errorf("FlagsTouchSettings(%q) = %v, want %v", tc.argv, got, tc.want)
		}
	}
}

// A linked worktree's local file lives at the MAIN checkout's root, per the
// settings documentation.
func TestSources_WorktreeLocalFileIsAtMainCheckoutRoot(t *testing.T) {
	main := t.TempDir()
	gitdir := filepath.Join(main, ".git", "worktrees", "wt1")
	write(t, filepath.Join(gitdir, "commondir"), "../..\n")
	wt := filepath.Join(t.TempDir(), "wt1")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+gitdir+"\n")
	cwd := filepath.Join(wt, "sub")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	got := localSources(Inputs{CWD: cwd, Home: t.TempDir()})

	want := []Source{
		{Layer: LayerLocal, Path: filepath.Join(main, ".claude", "settings.local.json")},
		{Layer: LayerLocal, Path: filepath.Join(cwd, ".claude", "settings.local.json")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("localSources = %+v, want %+v", got, want)
	}
}

// A .git file that cannot be resolved makes the local layer opaque rather
// than silently falling back to the starting directory.
func TestSources_UnresolvableGitFileIsOpaque(t *testing.T) {
	wt := t.TempDir()
	write(t, filepath.Join(wt, ".git"), "not a gitdir line\n")
	got := localSources(Inputs{CWD: wt})
	if len(got) != 1 || !got[0].Opaque {
		t.Fatalf("localSources = %+v, want one opaque local source", got)
	}
}

// A repository rooted at the home directory keeps the local file beside the
// project file, in the starting directory.
func TestSources_RepoAtHomeKeepsStartDirectory(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(home, "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	got := localSources(Inputs{CWD: cwd, Home: home})
	want := []Source{{Layer: LayerLocal, Path: filepath.Join(cwd, ".claude", "settings.local.json")}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("localSources = %+v, want %+v", got, want)
	}
}

// Full precedence order: managed (MDM, drop-ins newest-name first, file),
// cli, local, project, user.
func TestSources_PrecedenceOrder(t *testing.T) {
	managed := t.TempDir()
	write(t, filepath.Join(managed, "managed-settings.d", "10-a.json"), "{}")
	write(t, filepath.Join(managed, "managed-settings.d", "20-b.json"), "{}")
	mdm := filepath.Join(t.TempDir(), "com.anthropic.claudecode.plist")
	write(t, mdm, "")
	cwd := t.TempDir()
	home := t.TempDir()

	got := Sources(Inputs{CWD: cwd, Home: home, ManagedDir: managed, MDMProfiles: []string{mdm}, CLIOpaque: true})

	want := []Source{
		{Layer: LayerManaged, Opaque: true},
		{Layer: LayerManaged, Path: filepath.Join(managed, "managed-settings.d", "20-b.json")},
		{Layer: LayerManaged, Path: filepath.Join(managed, "managed-settings.d", "10-a.json")},
		{Layer: LayerManaged, Path: filepath.Join(managed, "managed-settings.json")},
		{Layer: LayerCLI, Opaque: true},
		{Layer: LayerLocal, Path: filepath.Join(cwd, ".claude", "settings.local.json")},
		{Layer: LayerProject, Path: filepath.Join(cwd, ".claude", "settings.json")},
		{Layer: LayerUser, Path: filepath.Join(home, ".claude", "settings.json")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Sources =\n%+v\nwant\n%+v", got, want)
	}
}

// An existing MDM profile stops the walk before any file: its content is
// never read, so a lower value cannot be trusted past it.
func TestLookup_OpaqueSourceStopsTheWalk(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".claude", "settings.local.json"), `{"env":{"K":"v"}}`)
	mdm := filepath.Join(t.TempDir(), "p.plist")
	write(t, mdm, "")

	r := Lookup(Sources(Inputs{CWD: cwd, Home: t.TempDir(), MDMProfiles: []string{mdm}}), "K")

	if r.Status != StatusOpaque || r.Source.Layer != LayerManaged {
		t.Fatalf("Lookup = %+v, want opaque managed", r)
	}
}
