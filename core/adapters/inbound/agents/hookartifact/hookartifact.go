// Package hookartifact installs, verifies, and uninstalls a single
// executable file artifact that an upstream agent auto-discovers from a
// plain directory glob — the shape pi's extensions/irrlicht.js (#1721) and
// opencode's plugin/irrlicht.js (#1719) both turned out to share (issue
// #1764).
//
// Both adapters landed within a day of each other, independently, and
// SonarCloud measured 105 duplicated lines between their two
// hookinstaller.go files. That was not a coincidence of two authors writing
// similar code: both upstream loaders resolve extensions the same way (a
// directory glob accepting a bare .js file, no registry, no package manager,
// no config-file entry), so both installers do the same four things —
// resolve the artifact's path, write one file atomically, decide whether an
// existing file at that path is still ours, and remove it on uninstall. This
// package is that shared logic, extracted once both adapters were merged and
// green rather than riding behind either feature review (see #1764 for why
// that sequencing was deliberate).
//
// # What stays with the adapter
//
// The artifact's CONTENT does not move here. pi's extension.js and
// opencode's plugin.js are different files subscribing to different upstream
// events, each graded by its own adapter-local test suite
// (extension_test.go, plugin_test.go) that has nothing to do with the
// install mechanism — those tests mutate the embedded template directly and
// must keep compiling unchanged. So an Installer takes a Render function
// (the adapter's own renderExtension/renderPlugin, which already owns the
// template, the placeholder token, and the beacon-command substitution) and
// only orchestrates WHEN to call it: on a fresh install, on a rewrite of a
// stale one, and to compute what "current" looks like for Verify.
//
// Likewise, this package never imports core/pkg/hookbeacon itself — every
// other shared installer of this repo's (hookjson/hooktoml/hookyaml, each
// living under core/adapters/inbound/agents/ beside this one) takes
// hookbeacon-derived values as plain data from the declaring adapter rather
// than reaching for hookbeacon on its own; hermes's hookyaml wiring is the
// precedent (`Sentinel: hookbeacon.Sentinel(segment)` computed by the
// adapter, handed in as a field). Installer follows the same shape:
// Sentinel and ResolveCommand are supplied by the adapter's own
// hookinstaller.go, which already imports hookbeacon for other reasons
// (HookEndpointPath, the beacon route). That keeps this package a plain
// consumer of pre-resolved data — consistent with its siblings, and the
// reason it can live beside them rather than needing a placement exception.
//
// # The one property that matters most
//
// IsOurs (the decision inside EnsureInstalledWithCommand, Verify, and
// Uninstall) is the one place this package can get subtly wrong in the
// permissive direction: mistaking a user's own same-named file for one of
// ours would make an install overwrite it, and mistaking one for a stale
// copy of ours would make an uninstall delete it. It is deliberately the
// same test every adapter of this shape already used before this
// extraction — a substring check for the adapter-supplied Sentinel (see
// hookbeacon.Sentinel's own doc for why it excludes the binary path: a copy
// naming an irrlichd that has since moved is still recognized as ours)
// while never matching a file that does not carry it. Getting this right is
// the whole justification for the extraction: two independent copies of
// this check existing side by side is exactly the shape that lets them
// drift apart silently, in code that deletes files from a user's own
// directory.
package hookartifact

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/atomicfile"
)

// Installer owns the install/verify/uninstall lifecycle of one single-file
// artifact. Every field is required; a zero-value Installer will fail loudly
// (a nil Path, Render, or ResolveCommand panics on first call, which is
// preferable to a silent no-op — an adapter that forgets to wire one of
// these has a bug worth surfacing at the earliest possible point).
type Installer struct {
	// AdapterName identifies the upstream agent for error text only (e.g.
	// the "pi: " / "opencode: " prefix on a refusal-to-overwrite message).
	// Ownership recognition itself is Sentinel's job, not this field's.
	AdapterName string

	// Sentinel is the substring that marks a file as this installer's own —
	// e.g. hookbeacon.Sentinel(AdapterName), computed once by the declaring
	// adapter and handed in as plain data (see the package doc for why this
	// package does not compute it itself).
	Sentinel string

	// ResolveCommand resolves the beacon command to install or compare
	// against for the CURRENT irrlichd — e.g.
	// func() (string, error) { return hookbeacon.InstalledCommand(AdapterName) }.
	// Called on every operation rather than cached, so a long-lived process
	// always compares against what it would install right now.
	ResolveCommand func() (string, error)

	// Path resolves the absolute path of the file this installer owns —
	// e.g. pi's ExtensionPath or opencode's PluginPath. Called on every
	// operation rather than cached, so it always reflects the current
	// environment (an env-var override honored at call time, not at
	// Installer-construction time).
	Path func() (string, error)

	// Render produces the exact bytes to install for a given, already-
	// resolved beacon command. Owned by the caller because the artifact's
	// content is adapter-specific; this package only decides when to call
	// it (fresh install, rewrite of a stale copy, or to compute what
	// "current" looks like for Verify) and never inspects what it returns
	// beyond a byte-for-byte comparison.
	Render func(command string) ([]byte, error)

	// Events is every hook event this install represents, reported back
	// verbatim in HookEntryStatus.Missing/Stale — never inspected or
	// filtered by this package, since the artifact carries exactly one file
	// covering all of them at once (unlike a config-entry install, there is
	// no per-event grain to check independently).
	Events []string
}

// IsOurs reports whether a file on disk is an artifact this installer would
// have written — see the package doc for why this is the load-bearing
// decision.
//
// Exported so a caller that already has the bytes in hand (an adapter's own
// test asserting a freshly-written file carries the sentinel) can ask
// without going through inspect/Uninstall's file I/O.
func (in Installer) IsOurs(src []byte) bool {
	return strings.Contains(string(src), in.Sentinel)
}

// installState classifies the file at Path against the bytes Render would
// produce for a given command — the ONE place that classification happens,
// so EnsureInstalledWithCommand and VerifyInstalled can never independently
// drift on what "current" means. Before this was split out, both functions
// re-derived the same read-and-compare logic separately; a change to one
// (e.g. a normalization tweak) could silently stop matching the other,
// which is exactly the kind of divergence issue #1764 extracted this
// package to prevent.
type installState int

const (
	installAbsent  installState = iota // nothing at Path
	installForeign                     // present, but IsOurs is false
	installStale                       // ours, but not byte-identical to want
	installCurrent                     // ours and byte-identical to want
)

// inspect resolves Path, renders the bytes this Installer would write for
// command, and classifies whatever is currently on disk against them. want
// is always returned (even when the file is absent or foreign) so a caller
// that decides to write never re-renders.
func (in Installer) inspect(command string) (path string, state installState, want []byte, err error) {
	path, err = in.Path()
	if err != nil {
		return "", 0, nil, err
	}
	want, err = in.Render(command)
	if err != nil {
		return "", 0, nil, err
	}

	existing, readErr := os.ReadFile(path)
	switch {
	case errors.Is(readErr, os.ErrNotExist):
		return path, installAbsent, want, nil
	case readErr != nil:
		return "", 0, nil, readErr
	case !in.IsOurs(existing):
		return path, installForeign, want, nil
	case string(existing) == string(want):
		return path, installCurrent, want, nil
	default:
		// Ours, but stale — a moved irrlichd, or a template change.
		return path, installStale, want, nil
	}
}

// EnsureInstalled writes the artifact for the irrlichd that is running now,
// creating the parent directory if it does not exist. Returns true if
// anything changed.
//
// Idempotent, and self-healing across a moved binary: an already-installed
// copy whose beacon command names a different irrlichd is rewritten IN
// PLACE (same path, one file), never duplicated.
func (in Installer) EnsureInstalled() (bool, error) {
	command, err := in.ResolveCommand()
	if err != nil {
		return false, err
	}
	return in.EnsureInstalledWithCommand(command)
}

// EnsureInstalledWithCommand is EnsureInstalled with the beacon command
// resolved by the caller — the seam the #1178 contract's ForeignInstall uses
// to seed the install a DIFFERENTLY-situated daemon would have written.
func (in Installer) EnsureInstalledWithCommand(command string) (bool, error) {
	path, state, want, err := in.inspect(command)
	if err != nil {
		return false, err
	}
	switch state {
	case installCurrent:
		return false, nil
	case installForeign:
		// A file we did not write occupies the path we install to.
		// Overwriting it would destroy a user's own file for the sake of
		// monitoring, which is never the right trade; refusing surfaces as
		// "granted but NOT applied" in the wizard (issue #1362) with this
		// message, which is the honest outcome.
		return false, fmt.Errorf("%s: %s exists and was not written by irrlicht; refusing to overwrite it", in.AdapterName, path)
	}
	// installAbsent or installStale: write it.
	if err := atomicfile.WriteFile(path, want); err != nil {
		return false, err
	}
	return true, nil
}

// VerifyInstalled reports whether the installed artifact is still present
// and current, without writing anything (issue #1372). This is what
// services.HookEntryVerifier re-checks a granted install with on a timer.
func (in Installer) VerifyInstalled() (agent.HookEntryStatus, error) {
	command, err := in.ResolveCommand()
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	_, state, _, err := in.inspect(command)
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	switch state {
	case installAbsent, installForeign:
		// A foreign file is reported as Missing rather than Stale: "stale"
		// means EnsureInstalled would rewrite it, and it deliberately will
		// not.
		return agent.HookEntryStatus{Missing: append([]string(nil), in.Events...)}, nil
	case installStale:
		return agent.HookEntryStatus{Stale: append([]string(nil), in.Events...)}, nil
	default: // installCurrent
		return agent.HookEntryStatus{}, nil
	}
}

// Uninstall removes the artifact. Returns true if anything changed.
//
// Removes the FILE, because the file is the entire install — nothing else
// was touched, so there is nothing else to undo. A file at our path that is
// not ours is left alone, the same "leave a foreign entry alone" rule every
// hook uninstall in this codebase follows — see the package doc for why
// this is the direction that matters most to get right.
//
// The parent directory is deliberately NOT removed when it empties out, even
// if this install is what created it: an empty extensions/plugin directory
// is an ordinary state for the upstream agent, and removing a directory a
// user's editor or shell may have open is a worse surprise than leaving one
// behind.
func (in Installer) Uninstall() (bool, error) {
	path, err := in.Path()
	if err != nil {
		return false, err
	}
	existing, readErr := os.ReadFile(path)
	if errors.Is(readErr, os.ErrNotExist) {
		return false, nil
	}
	if readErr != nil {
		return false, readErr
	}
	if !in.IsOurs(existing) {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}
