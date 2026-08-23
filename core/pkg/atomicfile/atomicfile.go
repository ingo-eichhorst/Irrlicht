// Package atomicfile writes a shared, user-owned config or hook-install
// file the way every install path in this repo needs to: build the new
// content in a temp file beside the target, then atomically rename it into
// place, so a reader — the agent CLI itself, mid-launch, or a concurrent
// irrlicht process re-checking install state — never observes a
// half-written file.
//
// Before this package existed, eleven call sites hand-rolled the same
// temp-file-plus-rename shape independently: nine private `atomicWriteFile`
// functions (one per hook-installing adapter) plus two exported
// `AtomicWriteFile` functions inside the shared hooktoml and hookyaml
// packages (used by the vibe and hermes adapters respectively). Comparing
// all eleven (issue #1761) found them equivalent in intent but not in
// behavior:
//
//   - Nine of eleven created the temp file with os.CreateTemp, colocated in
//     the target's own directory (so the final os.Rename can never cross a
//     filesystem, which is what makes it atomic) and under a randomized
//     name. Two — claudecode's and processlifecycle's — used a fixed name
//     (path+".tmp") and os.WriteFile directly. A fixed temp name is a
//     symlink-race / concurrent-installer hazard os.CreateTemp's O_EXCL
//     avoids, so this package always uses os.CreateTemp.
//   - Nine of eleven finished at file mode 0o600 (owner read/write only);
//     claudecode's and processlifecycle's os.WriteFile call passed 0o644
//     (also group/other readable). These are single-user config and hook
//     files with no legitimate second reader, so 0o600 — the majority and
//     the stricter of the two — is what this package writes.
//   - Ten of eleven created a missing parent directory at 0o755; hookyaml's
//     (hermes') used 0o700. Nothing anywhere depends on these per-user
//     config directories being group- or other-accessible, so 0o700 — the
//     stricter of the two — is what this package creates them at. Because
//     os.MkdirAll is a no-op whenever the directory already exists (the
//     overwhelmingly common case: the agent CLI itself created it), this
//     only changes behavior the rare first time irrlicht is what creates a
//     given config directory.
//   - None of the eleven called Sync before the rename. This package
//     doesn't either — adding it would be new durability behavior no copy
//     ever had, which is out of scope for a refactor that preserves what
//     each copy already did.
//
// Every other difference among the eleven (the temp file's glob pattern,
// whether Chmod ran before or after Close, the exact shape of the deferred
// cleanup) was cosmetic: none of it is externally observable, so this
// package picks one form for all of it rather than parameterizing on it.
package atomicfile

import (
	"os"
	"path/filepath"
)

// WriteFile atomically writes data to path: it creates the parent
// directory if missing (0o700), writes data to a temp file created
// alongside path (so the rename below is guaranteed to stay on one
// filesystem), sets that temp file's permissions to 0o600, and renames it
// over path. A partial failure never leaves a half-written file at path —
// at worst it leaves an orphaned, already-0600 temp file behind, which a
// later call in the same directory harmlessly ignores.
//
// The signature matches every one of the eleven functions this package
// replaces (`func(path string, data []byte) error`), so it drops into any
// existing `WriteFile` field or callback of that shape without a wrapper.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, tempPattern(path))
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// tempPattern derives a CreateTemp glob pattern from the target's own
// basename (e.g. "hooks.json" -> ".hooks.json.*.tmp") so a temp file that
// survives a crash between Write and the final Rename still names the file
// it was destined to become — the same debuggability each of the eleven
// prior copies got by hand-writing its own adapter-specific pattern
// (".irrlicht-hooks-*.json.tmp", ".irrlicht-extension-*.js.tmp", …), derived
// here instead of hardcoded per call site.
func tempPattern(path string) string {
	return "." + filepath.Base(path) + ".*.tmp"
}
