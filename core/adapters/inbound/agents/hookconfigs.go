package agents

import (
	"fmt"
	"path/filepath"

	"irrlicht/core/domain/agent"
)

// HookConfig is one adapter's declared hook config file, resolved.
type HookConfig struct {
	// Adapter is the adapter label (agent.Identity.Name) — the key the
	// consent store records permissions under.
	Adapter string
	// Key is the permission key the hooks are declared under.
	Key string
	// Path is the resolved, absolute config file the hooks live in.
	Path string
	// Uninstall removes irrlicht's entries from Path, reporting whether it
	// modified anything.
	Uninstall func() (bool, error)
}

// HookConfigs projects the adapter registry onto the set of agent config files
// irrlicht installs hooks into — the single source both `irrlichd
// --uninstall-hooks` and the onboarding recorder's config snapshot read (#1357).
//
// Before this existed each of them carried its own two-element literal list.
// Both were correct, and both would have been silently wrong the first time a
// third hook adapter landed: uninstall would leave that agent's entries in the
// user's real config pointing at a dead port forever, and a recording would
// repoint the same file at a dead recorder port and never hand it back.
//
// A declaration that is present but unusable is an error rather than a skip.
// Dropping it would reproduce exactly the failure this function exists to
// remove — a hook-installing adapter absent from both lists — only harder to
// notice, because nothing would say so.
//
// It takes the adapter slice rather than calling All() itself, matching the
// other registry projections in maps.go. Note the daemon's consent catalog is
// All() PLUS three non-agent declarations appended in startup.go (gastown,
// launcher, kitty) — those are not projected here, and the tripwire holding
// them to that is TestNonAgentPermissionDeclarationsDeclareNoHooks in
// core/cmd/irrlichd.
func HookConfigs(all []agent.Agent) ([]HookConfig, error) {
	var out []HookConfig
	for _, a := range all {
		for _, p := range a.Permissions {
			if p.Hooks == nil {
				continue
			}
			if p.Hooks.ConfigPath == nil || p.Hooks.Uninstall == nil {
				return nil, fmt.Errorf("adapter %q permission %q: HookInstall needs both ConfigPath and Uninstall", a.Identity.Name, p.Key)
			}
			path, err := p.Hooks.ConfigPath()
			if err != nil {
				return nil, fmt.Errorf("adapter %q permission %q: resolve hook config path: %w", a.Identity.Name, p.Key, err)
			}
			if !filepath.IsAbs(path) {
				return nil, fmt.Errorf("adapter %q permission %q: hook config path %q is not absolute", a.Identity.Name, p.Key, path)
			}
			out = append(out, HookConfig{
				Adapter:   a.Identity.Name,
				Key:       p.Key,
				Path:      path,
				Uninstall: p.Hooks.Uninstall,
			})
		}
	}
	return out, nil
}
