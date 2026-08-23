package services

import (
	"fmt"
	"path/filepath"
	"strings"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
)

// Issue #1449's guard, in its own file rather than inside permission_service.go
// beside #1365's version gate. Both refuse from the same call site
// (runClosureEffect), but they answer unrelated questions — one about the
// upstream CLI, one about which home a file lives in — and permission_service.go
// is already the package's largest unit. Its tests are the sibling
// permission_shared_config_gate_test.go.

// sharedConfigRefusal returns a non-nil error when a grant-all daemon must not
// run this permission's Apply, because the file that Apply writes is the user's
// real, shared agent config rather than something inside the home this daemon
// was given (issue #1449).
//
// Why grant-all specifically. In ask mode a human answered this exact
// permission in the wizard, which is the whole #570 contract; the guard would
// be wrong there and is inert. grant-all answers on the user's behalf for
// EVERY declared permission, so it runs installers nobody chose to run —
// including, in the reported incident, hook installs that repointed
// ~/.claude/settings.json and ~/.codex/hooks.json at a dev daemon's port and
// left them pointing there after it died. The permissions still read "granted",
// the entries were still present, and nothing arrived.
//
// What "isolated" means, and what it does NOT mean. The obvious reading —
// "IRRLICHT_HOME is set and differs from $HOME" — is VACUOUS against this
// defect, and checking it would have shipped a guard that never fires: the
// daemon that caused the incident had IRRLICHT_HOME set (that is what
// ir:test-mac's separate mode and every dev daemon do), and so does the
// recording rig. IRRLICHT_HOME isolates the daemon's OWN state — socket, addr
// file, stores — and a ManagedUserFile is by definition the other thing: a file
// that follows $HOME and outlives the daemon (agent.Permission.Writes). So
// isolation is asked of the FILE, not of the daemon: a managed file is inside
// an isolated home only when its resolved path is inside IRRLICHT_HOME, which
// happens only when $HOME — or a per-agent override the adapter honors, like
// CODEX_HOME, COPILOT_HOME or XDG_CONFIG_HOME — points in there too. That is a
// real configuration (a fully sandboxed integration daemon) and it is allowed;
// everything else is the user's own config and is refused.
//
// Containment is deliberately LEXICAL, unlike hookjson's confiner. That one
// resolves symlinks before comparing because its input is caller-supplied on an
// unauthenticated endpoint and an unresolved compare is a security hole. Here
// the path is our own adapter's resolver output, and the two error directions
// are not symmetric: a false "outside" refuses an install (safe, and says why),
// while a false "inside" would permit one. Lexical comparison can only err
// toward refusing, so it is the fail-closed choice rather than a shortcut.
// Deliberately does not check p.Advisory (issue #1748). That field names a
// path an Apply is KNOWN to write but that ManagedUserFile cannot represent —
// kiro-cli's own agent-identity sqlite store is the one declared instance —
// and it would seem to belong here on the same logic as Path/Also: the
// hazard this guard exists for (a grant-all daemon writing into the
// operator's real $HOME) is identical in shape. It is left out because
// extending the refusal is a live-behavior change nothing has measured:
// whether kiro-cli's data store actually escapes the isolated home under
// today's recording setups depends on whether $HOME itself is redirected
// there, which no test here exercises. Wiring this in wrong, in the refusing
// direction, would silently break every grant-all recording of kiro-cli's
// hook install; wrong in the permitting direction adds nothing #1748 asked
// for (Advisory already gets its own visibility path — see agent.AdvisoryWrite,
// agents.AdvisoryFiles, --print-advisory-files). Left as a documented option
// for whoever next measures that configuration, not a guess shipped here.
func (s *PermissionService) sharedConfigRefusal(p agent.Permission) error {
	if s.mode != config.PermissionModeGrantAll || p.Writes == nil {
		return nil
	}
	if s.allowSharedConfigWrites {
		return nil
	}
	// Path plus every Also entry (#1718): a single Apply closure that writes
	// two files is only safe under grant-all when BOTH resolve inside the
	// isolated home. Checking Path alone would wave an Apply through whose
	// SECOND file lands in the user's real config — invisible to this guard in
	// exactly the way an undeclared write always is, which is the incident
	// #1449 exists to prevent. Path is checked first so its error message (the
	// one every existing test pins) is unchanged when Also is empty.
	if err := s.refuseIfOutsideIsolatedHome(p.Key, "Path", p.Writes.Path); err != nil {
		return err
	}
	for i, also := range p.Writes.Also {
		if err := s.refuseIfOutsideIsolatedHome(p.Key, fmt.Sprintf("Also[%d]", i), also); err != nil {
			return err
		}
	}
	return nil
}

// refuseIfOutsideIsolatedHome resolves one file a permission's Apply writes
// and refuses if it is unresolvable or lands outside the daemon's isolated
// home. label names which resolver failed (e.g. "Path", "Also[0]") so a
// multi-file permission's refusal says which of its files is the problem
// rather than leaving the reader to guess.
func (s *PermissionService) refuseIfOutsideIsolatedHome(key, label string, resolve func() (string, error)) error {
	// A nil resolver is a malformed declaration, not a permission that writes
	// nothing — that shape is Writes == nil in the caller. Same direction as an
	// erroring resolver: a file we cannot name is not one we can call safe.
	// Unreachable in a CI-green tree (agents.ManagedUserFiles rejects it), but
	// the guard's correctness must not rest on a test in another package.
	if resolve == nil {
		return fmt.Errorf("refusing to run %s's install in IRRLICHT_PERMISSION_MODE=grant-all: "+
			"it declares a managed user file with no %s resolver", key, label)
	}
	path, err := resolve()
	if err != nil {
		// Fail closed. An unresolvable path is not a licence to write: we cannot
		// name the file, so we certainly cannot say it is safe to modify.
		return fmt.Errorf("refusing to write %s's shared config in IRRLICHT_PERMISSION_MODE=grant-all: "+
			"cannot resolve which file it writes (%s: %v)", key, label, err)
	}
	if s.isolatedHome != "" && pathInsideRoot(s.isolatedHome, path) {
		return nil
	}
	// Kept short on purpose: this string is rendered verbatim as the
	// permission's EffectError in both wizards, beside #1365's one-line
	// refusals. It has to name the file and the way forward, and stop there.
	return fmt.Errorf("refusing to modify %s: a shared config file outside this daemon's "+
		"isolated home (IRRLICHT_HOME=%s), and grant-all answered for you. Back it up, "+
		"then set IRRLICHT_ALLOW_SHARED_CONFIG_WRITES=1",
		path, isolatedHomeForMessage(s.isolatedHome))
}

// isolatedHomeForMessage renders IRRLICHT_HOME for the refusal text, naming the
// unset case rather than printing an empty string the reader has to interpret.
func isolatedHomeForMessage(root string) string {
	if root == "" {
		return "<unset>"
	}
	return root
}

// pathInsideRoot reports whether path is root itself or lies beneath it,
// comparing cleaned absolute paths. A relative or unresolvable pair reports
// false, which refuses — see sharedConfigRefusal for why that direction is the
// safe one here.
func pathInsideRoot(root, path string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
