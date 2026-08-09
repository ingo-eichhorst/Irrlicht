package main

import (
	"bytes"
	"errors"
	"sort"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	gastownadapter "irrlicht/core/adapters/inbound/orchestrators/gastown"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// TestPrintHookConfigsListsEveryDeclaredConfigFile pins the second half of
// #1357's coverage requirement: the recording rig reads its protected file set
// from `irrlichd --print-hook-configs`, so anything the registry declares and
// this output omits is a file a grant-all recording daemon would rewrite in the
// user's real $HOME and never hand back.
//
// Contract test — it passes by construction; it was seen red by making
// printHookConfigs emit only the first config (see the PR body).
func TestPrintHookConfigsListsEveryDeclaredConfigFile(t *testing.T) {
	configs, err := agents.HookConfigs(agents.All())
	if err != nil {
		t.Fatalf("agents.HookConfigs(agents.All()): %v", err)
	}
	if len(configs) == 0 {
		t.Fatal("no adapter declares a hooks permission — this check would pass vacuously")
	}

	var buf bytes.Buffer
	if err := printHookConfigs(&buf); err != nil {
		t.Fatalf("printHookConfigs: %v", err)
	}

	printed := linesOf(t, buf.String())

	want := map[string]bool{}
	for _, c := range configs {
		want[c.Path] = true
		if !printed[c.Path] {
			t.Errorf("%s/%s declares %q, which --print-hook-configs does not list: "+
				"a recording would repoint that file and never restore it (#1357)", c.Adapter, c.Key, c.Path)
		}
	}
	for p := range printed {
		if !want[p] {
			t.Errorf("--print-hook-configs listed %q, which no adapter declares", p)
		}
	}
}

// linesOf splits the flag's output into a set, failing on a repeated line: the
// rig keys its backups by line index, so one file listed twice would be backed
// up under two indices and restored twice.
func linesOf(t *testing.T, out string) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		if got[line] {
			t.Errorf("printed %q twice", line)
		}
		got[line] = true
	}
	return got
}

// fakePermissionStore is an in-memory consent store, so the coverage check
// never reads or writes the developer's real permissions.json.
type fakePermissionStore struct {
	set   permission.Set
	saved permission.Set
}

func (f *fakePermissionStore) Load() (permission.Set, error) { return f.set, nil }
func (f *fakePermissionStore) Save(s permission.Set) error   { f.saved = s.Clone(); return nil }

// TestUninstallHookConfigsCoversEveryConfig pins the first half: --uninstall-hooks
// must run EVERY declared uninstaller and record EVERY matching permission as
// denied. Missing one leaves that agent's hook entries in the user's real config
// permanently, firing on every turn at a dead port; and a hooks permission left
// "granted" reinstalls them on the next daemon start via the Apply closure,
// silently reverting the uninstall (#570).
//
// Contract test — driven by synthetic configs so it stays true of an adapter set
// that has not been written yet. Seen red by truncating the loop to the first
// config (see the PR body).
func TestUninstallHookConfigsCoversEveryConfig(t *testing.T) {
	var called []string
	configs := []agents.HookConfig{
		{Adapter: "alpha", Key: "hooks", Path: "/tmp/alpha/settings.json",
			Uninstall: func() (bool, error) { called = append(called, "alpha"); return true, nil }},
		{Adapter: "beta", Key: "hooks", Path: "/tmp/beta/hooks.json",
			Uninstall: func() (bool, error) { called = append(called, "beta"); return false, nil }},
		{Adapter: "gamma", Key: "hooks", Path: "/tmp/gamma/settings.json",
			Uninstall: func() (bool, error) { called = append(called, "gamma"); return true, nil }},
	}

	store := &fakePermissionStore{set: permission.Set{}}
	for _, c := range configs {
		store.set.Put(c.Adapter, c.Key, permission.StateGranted)
	}

	var out bytes.Buffer
	uninstallHookConfigs(&out, configs, store)

	sort.Strings(called)
	if got, want := strings.Join(called, ","), "alpha,beta,gamma"; got != want {
		t.Errorf("uninstalled %q, want %q — an adapter whose uninstaller never runs keeps its hook entries forever (#1357)", got, want)
	}

	if store.saved == nil {
		t.Fatal("granted hooks permissions were never persisted as denied")
	}
	for _, c := range configs {
		if st := store.saved.Get(c.Adapter, c.Key); st != permission.StateDenied {
			t.Errorf("%s/%s is %q after uninstall, want %q — a still-granted hooks permission reinstalls on the next start", c.Adapter, c.Key, st, permission.StateDenied)
		}
	}

	// Each file is named in the report, so a CODEX_HOME user is told which
	// file was actually cleaned rather than the default one.
	for _, c := range configs {
		if !strings.Contains(out.String(), c.Path) {
			t.Errorf("the uninstall report never mentions %q:\n%s", c.Path, out.String())
		}
	}
}

// TestUninstallHookConfigsLeavesUngrantedPermissionsAlone is a lock: uninstall
// records an opt-out only where consent had actually been given, so a pending
// permission is not silently turned into a denial the wizard will stop asking
// about.
func TestUninstallHookConfigsLeavesUngrantedPermissionsAlone(t *testing.T) {
	configs := []agents.HookConfig{
		{Adapter: "alpha", Key: "hooks", Path: "/tmp/alpha/settings.json",
			Uninstall: func() (bool, error) { return false, nil }},
	}
	store := &fakePermissionStore{set: permission.Set{}}

	uninstallHookConfigs(&bytes.Buffer{}, configs, store)

	if store.saved != nil {
		t.Errorf("persisted %v for a permission that was never granted", store.saved)
	}
}

// TestWriteHookConfigPathsDeduplicates pins the dedup the rig depends on: it
// keys its backups by line index, so one file listed twice would be backed up
// under two indices and restored twice. Two adapters sharing a config file is
// the case that produces it (claudecode's hooks and statusline already share
// ~/.claude/settings.json), so this cannot be exercised through the shipped
// registry — it is driven directly.
func TestWriteHookConfigPathsDeduplicates(t *testing.T) {
	var buf bytes.Buffer
	writeHookConfigPaths(&buf, []agents.HookConfig{
		{Adapter: "alpha", Key: "hooks", Path: "/tmp/shared/settings.json"},
		{Adapter: "beta", Key: "hooks", Path: "/tmp/shared/settings.json"},
		{Adapter: "gamma", Key: "hooks", Path: "/tmp/gamma/hooks.json"},
	})
	got := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{"/tmp/shared/settings.json", "/tmp/gamma/hooks.json"}
	if len(got) != len(want) {
		t.Fatalf("printed %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestUninstallHookConfigsSurvivesOneFailingAdapter pins the fix for a failure
// that used to end the whole command: hookjson refuses to rewrite a config it
// cannot parse, so a single hand-edited ~/.claude/settings.json aborted the
// process before any later adapter was cleaned and before ANY hooks permission
// was recorded denied — leaving them granted, which re-installs on the next
// daemon start via the Apply closure (#570).
func TestUninstallHookConfigsSurvivesOneFailingAdapter(t *testing.T) {
	var called []string
	configs := []agents.HookConfig{
		{Adapter: "alpha", Key: "hooks", Path: "/tmp/alpha/settings.json",
			Uninstall: func() (bool, error) {
				called = append(called, "alpha")
				return false, errors.New("invalid character '}' looking for beginning of object key string")
			}},
		{Adapter: "beta", Key: "hooks", Path: "/tmp/beta/hooks.json",
			Uninstall: func() (bool, error) { called = append(called, "beta"); return true, nil }},
	}
	store := &fakePermissionStore{set: permission.Set{}}
	for _, c := range configs {
		store.set.Put(c.Adapter, c.Key, permission.StateGranted)
	}

	var out bytes.Buffer
	failed := uninstallHookConfigs(&out, configs, store)

	if failed != 1 {
		t.Errorf("failed = %d, want 1 — the caller needs a non-zero exit", failed)
	}
	if strings.Join(called, ",") != "alpha,beta" {
		t.Errorf("uninstalled %q, want %q — a failing adapter must not stop the ones after it", called, "alpha,beta")
	}
	if store.saved == nil {
		t.Fatal("no permission was recorded denied — the still-granted hooks reinstall on the next start")
	}
	if st := store.saved.Get("beta", "hooks"); st != permission.StateDenied {
		t.Errorf("beta/hooks = %q, want %q", st, permission.StateDenied)
	}
	if !strings.Contains(out.String(), "warning: failed to uninstall hooks from /tmp/alpha/settings.json") {
		t.Errorf("the failure is not reported to the user:\n%s", out.String())
	}
}

// TestNonAgentPermissionDeclarationsDeclareNoHooks closes the one gap in the
// registry projection. The daemon's consent catalog is agents.All() PLUS three
// daemon-wide declarations appended in startup.go — gastown, launcher identity,
// and the kitty remote-control config patch. agents.HookConfigs projects only
// the adapter slice, so a hooks declaration landing on one of those would be
// invisible to `--uninstall-hooks`, invisible to `--print-hook-configs`, and
// invisible to the agents-package contract test as well — the exact shape of
// #1357, one declaration later.
//
// This is where the tripwire has to live: package agents cannot import
// processlifecycle (which imports it back), while package main already wires
// all four. If this ever fires, route the declaration through
// agents.HookConfigs rather than deleting the assertion.
func TestNonAgentPermissionDeclarationsDeclareNoHooks(t *testing.T) {
	noop := func() error { return nil }
	catalog := []agent.Agent{
		gastownadapter.PermissionDeclaration(noop, noop),
		processlifecycle.LauncherPermissionDeclaration(),
		processlifecycle.KittyPermissionDeclaration(),
	}
	for _, a := range catalog {
		for _, p := range a.Permissions {
			if p.Hooks != nil {
				t.Errorf("%s/%s declares agent.HookInstall, but non-agent declarations are not projected by agents.HookConfigs — both hook config lists would miss it (#1357)", a.Identity.Name, p.Key)
			}
		}
	}
}
