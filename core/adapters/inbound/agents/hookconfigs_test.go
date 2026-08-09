package agents

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// TestEveryHooksPermissionIsCoveredByHookConfigs is the coverage check #1357
// asks for: an adapter that declares a hooks permission must also declare WHERE
// those hooks go, so both of the places that have to enumerate agent config
// files — `irrlichd --uninstall-hooks` and the onboarding recorder's snapshot —
// see it without anyone remembering to add it twice.
//
// Contract test, not a defect test: it passes by construction against a correct
// adapter set, and its whole value is that it CAN fail. It was seen red by
// dropping codex's Hooks declaration (see the PR body).
//
// The key match is a substring rather than an equality on "hooks" so an adapter
// that spells its permission key differently is still held to the contract.
func TestEveryHooksPermissionIsCoveredByHookConfigs(t *testing.T) {
	configs, err := HookConfigs(All())
	if err != nil {
		t.Fatalf("HookConfigs(): %v", err)
	}

	covered := make(map[string]bool, len(configs))
	for _, c := range configs {
		id := c.Adapter + "/" + c.Key
		if covered[id] {
			t.Errorf("%s is listed twice by HookConfigs()", id)
		}
		covered[id] = true
		if !filepath.IsAbs(c.Path) {
			t.Errorf("%s: hook config path %q is not absolute — the recorder backs up and restores exactly this path", id, c.Path)
		}
		if c.Uninstall == nil {
			t.Errorf("%s: HookConfigs() returned a nil Uninstall", id)
		}
	}

	declared := 0
	for _, a := range All() {
		for _, p := range a.Permissions {
			if !strings.Contains(strings.ToLower(p.Key), "hook") {
				continue
			}
			declared++
			id := a.Identity.Name + "/" + p.Key
			if p.Hooks == nil {
				t.Errorf("%s declares a hooks permission but no agent.HookInstall: "+
					"`irrlichd --uninstall-hooks` would leave its entries in the user's real "+
					"config permanently, and a recording would repoint that file at a dead "+
					"recorder port and never hand it back (#1357)", id)
				continue
			}
			if !covered[id] {
				t.Errorf("%s declares an agent.HookInstall that HookConfigs() does not list", id)
			}
		}
	}

	if declared == 0 {
		t.Fatal("no adapter declares a hooks permission — this check would pass vacuously")
	}
	if len(configs) != declared {
		t.Errorf("HookConfigs() returned %d entries for %d declared hooks permissions", len(configs), declared)
	}
}

// hookAdapter builds a one-permission adapter carrying the given HookInstall.
func hookAdapter(name string, h *agent.HookInstall) agent.Agent {
	return agent.Agent{
		Identity:    agent.Identity{Name: name},
		Permissions: []agent.Permission{{Key: "hooks", Kind: permission.KindModify, Hooks: h}},
	}
}

// TestHookConfigsRejectsUnusableDeclarations covers the branches that become
// log.Fatalf in `irrlichd --uninstall-hooks`. A half-declared HookInstall must
// be an error rather than a silent skip: skipping would reproduce exactly the
// failure HookConfigs exists to remove — a hook-installing adapter absent from
// both lists — only harder to notice, because nothing would say so.
func TestHookConfigsRejectsUnusableDeclarations(t *testing.T) {
	ok := func() (string, error) { return "/tmp/x/settings.json", nil }
	uninstall := func() (bool, error) { return false, nil }

	cases := []struct {
		name    string
		install *agent.HookInstall
		want    string
	}{
		{"nil ConfigPath", &agent.HookInstall{Uninstall: uninstall}, "ConfigPath"},
		{"nil Uninstall", &agent.HookInstall{ConfigPath: ok}, "Uninstall"},
		{"resolve fails", &agent.HookInstall{
			ConfigPath: func() (string, error) { return "", errors.New("no home dir") },
			Uninstall:  uninstall,
		}, "no home dir"},
		{"relative path", &agent.HookInstall{
			ConfigPath: func() (string, error) { return "settings.json", nil },
			Uninstall:  uninstall,
		}, "not absolute"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HookConfigs([]agent.Agent{hookAdapter("alpha", tc.install)})
			if err == nil {
				t.Fatalf("HookConfigs() = %v, want an error naming %q", got, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "alpha") {
				t.Errorf("error %q does not name the offending adapter", err)
			}
		})
	}
}

// TestHookConfigsIgnoresPermissionsWithoutHooks is a lock: only the permission
// that actually installs hooks contributes a config file. A transcripts or
// statusline permission must not be swept in — the recorder would back up a
// file nobody writes hooks into, and uninstall would report on it.
func TestHookConfigsIgnoresPermissionsWithoutHooks(t *testing.T) {
	a := agent.Agent{
		Identity: agent.Identity{Name: "alpha"},
		Permissions: []agent.Permission{
			{Key: "transcripts", Kind: permission.KindObserve},
			{Key: "statusline", Kind: permission.KindModify, Apply: func() error { return nil }},
		},
	}
	got, err := HookConfigs([]agent.Agent{a})
	if err != nil {
		t.Fatalf("HookConfigs(): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("HookConfigs() = %v, want none", got)
	}
}
