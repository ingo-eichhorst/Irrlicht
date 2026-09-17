package dsh

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

func TestAgentIdentity(t *testing.T) {
	a := Agent()
	if a.Identity.Name != AdapterName || a.Identity.DisplayName != "DeepSeek Harness" {
		t.Errorf("identity = %+v", a.Identity)
	}
	if a.Identity.IconSVGLight == "" || a.Identity.IconSVGDark == "" {
		t.Error("both icon variants must be present")
	}
}

func TestAgentProcessDeclaration(t *testing.T) {
	a := Agent()
	if _, ok := a.Process.Match.(agent.CommandPattern); !ok {
		t.Errorf("process match = %T, want CommandPattern", a.Process.Match)
	}
	if a.Process.PIDForSession == nil {
		t.Error("PIDForSession is nil")
	}
}

func TestAgentSourceDeclaration(t *testing.T) {
	a := Agent()
	source, ok := a.Source.(agent.FilesUnderRoot)
	if !ok {
		t.Fatalf("source = %T, want FilesUnderRoot", a.Source)
	}
	if source.DirFunc == nil || source.SessionIDFromPath == nil {
		t.Error("source must resolve DSH_HOME lazily and derive directory-based session IDs")
	}
	if _, ok := source.Parser.(agent.JSONLineParser); !ok {
		t.Errorf("parser = %T, want JSONLineParser", source.Parser)
	}
}

func TestAgentPermissionDeclaration(t *testing.T) {
	a := Agent()
	if len(a.Permissions) != 1 {
		t.Fatalf("permission count = %d, want 1", len(a.Permissions))
	}
	p := a.Permissions[0]
	if p.Key != PermissionKeyTranscripts {
		t.Errorf("permission key = %q, want %q", p.Key, PermissionKeyTranscripts)
	}
	if p.Kind != permission.KindObserve {
		t.Errorf("permission kind = %q, want observe", p.Kind)
	}
	if p.Apply != nil {
		t.Error("observe permission Apply is not nil")
	}
	if p.Remove != nil {
		t.Error("observe permission Remove is not nil")
	}
	if p.Writes != nil {
		t.Error("observe permission Writes is not nil")
	}
}

func TestSessionsDirHonorsAbsoluteDSHHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(dshHomeEnvVar, home)
	if got, want := sessionsDir(), filepath.Join(home, "sessions"); got != want {
		t.Errorf("sessionsDir() = %q, want %q", got, want)
	}

	t.Setenv(dshHomeEnvVar, "")
	if got := sessionsDir(); got != defaultRootDir {
		t.Errorf("default sessionsDir() = %q, want %q", got, defaultRootDir)
	}
}

func TestSessionIDFromPathSelectsHighestGeneration(t *testing.T) {
	const id = "session-600e7941-bf4f-4da4-9ef6-489168e13724"
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"session.v2.jsonl.zstd",
		"session.v3.jsonl.zstd",
		"session.lock",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if got := sessionIDFromPath(filepath.Join(dir, "session.v2.jsonl.zstd")); got != "" {
		t.Errorf("older generation minted session %q", got)
	}
	if got := sessionIDFromPath(filepath.Join(dir, "session.v3.jsonl.zstd")); got != id {
		t.Errorf("highest generation ID = %q, want %q", got, id)
	}
	if got := sessionIDFromPath(filepath.Join(dir, "session.lock")); got != "" {
		t.Errorf("lock file minted session %q", got)
	}
	if got := sessionIDFromPath(filepath.Join(filepath.Dir(dir), "not-a-session", "session.v3.jsonl.zstd")); got != "" {
		t.Errorf("invalid session directory minted session %q", got)
	}
}

func TestSessionIDFromPathIdentifiesRemovedHighestGeneration(t *testing.T) {
	const id = "session-600e7941-bf4f-4da4-9ef6-489168e13724"
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(dir, "session.v2.jsonl.zstd")
	removed := filepath.Join(dir, "session.v3.jsonl.zstd")
	for _, path := range []string{older, removed} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	if got := sessionIDFromPath(removed); got != id {
		t.Errorf("removed highest generation ID = %q, want %q", got, id)
	}
}

func TestGenerationComparisonDoesNotOverflow(t *testing.T) {
	if compareGenerations("100000000000000000000000000000", "99999999999999999999999999999") <= 0 {
		t.Error("larger generation did not sort after smaller generation")
	}
	if got, ok := transcriptGeneration("session.v0003.jsonl.zstd"); !ok || got != "3" {
		t.Errorf("normalized generation = %q, %v; want 3, true", got, ok)
	}
}

func TestProcessCmdRegex(t *testing.T) {
	for _, cmd := range []string{
		"node /Users/ingo/.nvm/versions/node/v22.18.0/bin/dsh --profile web --no-open",
		"node /Users/ingo/.nvm/versions/node/v24.16.0/bin/dsh --profile tui",
		"dsh --profile headless prompt",
	} {
		if !processCmdRegex.MatchString(cmd) {
			t.Errorf("process pattern did not match %q", cmd)
		}
	}
	for _, cmd := range []string{
		"irrlichd --watch /Users/ingo/.dsh/sessions",
		"tail -f /Users/ingo/.dsh/sessions/x/session.v3.jsonl.zstd",
		"node /tmp/dsh-helper/index.js",
	} {
		if processCmdRegex.MatchString(cmd) {
			t.Errorf("process pattern matched unrelated command %q", cmd)
		}
	}
}

func TestSessionLockPath(t *testing.T) {
	path := "/Users/x/.dsh/sessions/--work--/session-600e7941-bf4f-4da4-9ef6-489168e13724/session.v3.jsonl.zstd"
	want := filepath.Join(filepath.Dir(path), sessionLockFilename)
	if got := sessionLockPath(path); got != want {
		t.Errorf("sessionLockPath() = %q, want %q", got, want)
	}
	if got := sessionLockPath("session.v3.jsonl.zstd"); got != "" {
		t.Errorf("parentless sessionLockPath() = %q, want empty", got)
	}
}
