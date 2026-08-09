// confine.go holds the receiver-side path guard every JSON-hook adapter shares
// (issue #1361).
//
// The daemon's hook endpoints are local and unauthenticated: any process on the
// machine can POST to them, and the transcript_path in that body is opened by
// whatever the receiver hands it to. Confinement therefore is not an
// adapter-specific nicety but a property of the receiver path itself, and it
// lives here — beside ConsentGranter, for the same reason — so a new
// hook-receiving adapter inherits it instead of remembering it. Codex had this
// logic inline and Claude Code did not, which is exactly the drift a shared
// helper removes.
//
// The roots come from the adapter's own agent.Source declaration rather than a
// second list: a confinement root that can disagree with the watched root is a
// guard on the wrong directory.
package hookjson

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/outbound"
)

// RejectReason names why a caller-supplied path was refused. It is a stable,
// low-cardinality token so a rejection can be logged and counted rather than
// dropped — the endpoint is unauthenticated, so a burst of rejections is the
// signal that something local is probing it.
type RejectReason string

const (
	// RejectNone is the zero value: the path was accepted.
	RejectNone RejectReason = ""
	// RejectEmptyPath — no transcript_path in the payload.
	RejectEmptyPath RejectReason = "empty_path"
	// RejectRelativePath — a relative path, which would resolve against the
	// daemon's working directory rather than the agent's transcript tree.
	RejectRelativePath RejectReason = "relative_path"
	// RejectWrongSuffix — not the file extension the adapter's transcripts use.
	RejectWrongSuffix RejectReason = "wrong_suffix"
	// RejectNoRoots — the adapter declared no transcript root, so nothing can
	// be confined. Fail closed: a missing root is not an empty guard.
	RejectNoRoots RejectReason = "no_roots"
	// RejectUnresolvable — the path (or its parent directory) could not be
	// resolved on disk, so its real identity is unknown.
	RejectUnresolvable RejectReason = "unresolvable"
	// RejectEscapesRoot — the resolved path lies outside every declared root.
	// Covers plain out-of-tree paths, parent traversal, and symlinks planted
	// inside the tree that point out of it.
	RejectEscapesRoot RejectReason = "escapes_root"
)

// PathConfiner confines a caller-supplied path to an adapter's declared
// transcript roots, and counts what it refuses.
//
// Safe for concurrent use: hook receivers are HTTP handlers, so Confine runs on
// many goroutines at once.
type PathConfiner struct {
	roots  func() []string
	suffix string

	mu     sync.Mutex
	counts map[RejectReason]uint64
}

// NewPathConfiner builds a confiner over roots, accepting only files whose
// extension is suffix (e.g. ".jsonl"; empty means any).
//
// roots is a func, not a slice, because a root is env-dependent
// (CLAUDE_CONFIG_DIR, CODEX_HOME) and the declaration that produced it is
// evaluated lazily. Each returned element is either absolute or $HOME-relative,
// exactly as agent.FilesUnderRoot documents; agentpaths.AbsRoot closes that gap.
func NewPathConfiner(roots func() []string, suffix string) *PathConfiner {
	return &PathConfiner{
		roots:  roots,
		suffix: suffix,
		counts: make(map[RejectReason]uint64),
	}
}

// ConfinerForSource builds a confiner from an adapter's own agent.Source
// declaration — the same roots the daemon builds its fswatchers from
// (wiring.go's AllRootsFor loop), so the guarded tree and the watched tree
// cannot drift apart.
//
// src is re-read on every Confine rather than captured once. A root is
// env-dependent (CLAUDE_CONFIG_DIR, CODEX_HOME) and an adapter's Dir is
// evaluated when it declares itself, so a confiner built at daemon start and
// frozen would guard wherever the environment pointed at that instant. Nothing
// relocates a root mid-run in production, but a handler constructed before its
// root is known guards the wrong tree — and that is the ordinary shape of a
// test, which makes it the shape a bug hides in.
//
// A Source that is not FilesUnderRoot declares no directory tree to confine to
// (FilesUnderCWD is per-process, ProcessOwnedStore is a database), so the
// resulting confiner refuses everything with RejectNoRoots rather than waving
// paths through. If such an adapter ever grows a hook receiver, that failure is
// the prompt to give it a real root — not something to be discovered later by
// an unauthenticated caller.
func ConfinerForSource(src func() agent.Source, goos, suffix string) *PathConfiner {
	return NewPathConfiner(func() []string {
		files, ok := src().(agent.FilesUnderRoot)
		if !ok {
			return nil
		}
		return files.AllRootsFor(goos)
	}, suffix)
}

// Confine resolves raw and returns the path the daemon may open, or the reason
// it was refused. A non-empty reason means the caller must reject the request;
// the returned path is empty in that case.
//
// Symlinks are resolved BEFORE the containment check. That ordering is the
// whole guard: a link planted inside the declared tree that points out of it is
// lexically contained and really is not, so a check that runs first passes a
// naive traversal test and confines nothing.
//
// The accepted path is rebuilt as declared-root + the resolved relative
// component, never returned as the caller's own string, so no caller-controlled
// path reaches os.Open (SonarQube gosecurity:S2083). It is rebuilt on the
// DECLARED root rather than the symlink-resolved one so the result stays in the
// same namespace as the fswatcher's paths, which are not symlink-resolved —
// downstream state is keyed by transcript path, and two spellings of one file
// are two sessions.
func (c *PathConfiner) Confine(raw string) (string, RejectReason) {
	if raw == "" {
		return "", c.count(RejectEmptyPath)
	}
	if !filepath.IsAbs(raw) {
		return "", c.count(RejectRelativePath)
	}
	if c.suffix != "" && filepath.Ext(raw) != c.suffix {
		return "", c.count(RejectWrongSuffix)
	}
	roots := c.roots()
	if len(roots) == 0 {
		return "", c.count(RejectNoRoots)
	}
	resolved, err := resolvePath(raw)
	if err != nil {
		return "", c.count(RejectUnresolvable)
	}
	for _, declared := range roots {
		if confined, ok := containedIn(declared, resolved); ok {
			return confined, RejectNone
		}
	}
	return "", c.count(RejectEscapesRoot)
}

// RejectPath writes the uniform refusal every hook receiver owes a confinement
// failure: a 400 naming the reason, and an error-level log line carrying the
// offending path. Never a silent 200 — the endpoint is unauthenticated, so a
// refused path is either a misconfigured agent or a local process probing the
// daemon, and both deserve to be visible. The path is logged with %q so an
// embedded newline cannot forge a log record.
//
// component is the caller's Logger component tag; sessionID is empty because
// confinement runs before an id can be derived from the path.
func RejectPath(w http.ResponseWriter, log outbound.Logger, component, raw string, reason RejectReason) {
	log.LogError(component, "", fmt.Sprintf("rejected hook transcript_path %q: %s", raw, reason))
	http.Error(w, "bad request: transcript_path "+string(reason), http.StatusBadRequest)
}

// Rejections returns a snapshot of the per-reason rejection counts. Nothing
// surfaces these to a UI yet — there is no daemon-wide counter registry to
// publish into — but they make "rejected and counted" an observable fact
// rather than a claim, which is what the contract assertion checks.
func (c *PathConfiner) Rejections() map[RejectReason]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[RejectReason]uint64, len(c.counts))
	for reason, n := range c.counts {
		out[reason] = n
	}
	return out
}

// RejectionCount returns the total number of paths this confiner has refused.
func (c *PathConfiner) RejectionCount() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var total uint64
	for _, n := range c.counts {
		total += n
	}
	return total
}

// count records a rejection and returns the reason, so call sites read as
// `return "", c.count(reason)`.
func (c *PathConfiner) count(reason RejectReason) RejectReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[reason]++
	return reason
}

// containedIn reports whether resolved (already symlink-resolved and absolute)
// lies strictly inside the declared root, and if so returns the path rebuilt on
// that declared root.
func containedIn(declared, resolved string) (string, bool) {
	absRoot, err := agentpaths.AbsRoot(declared)
	if err != nil {
		return "", false
	}
	// The root is resolved too: on macOS a temp or home directory routinely
	// sits behind a symlink (/var → /private/var), and comparing a resolved
	// file against an unresolved root reports every real transcript as an
	// escape.
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		// The declared root is not on disk. Nothing can be inside it.
		return "", false
	}
	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(absRoot, rel), true
}

// resolvePath resolves every symlink in path.
//
// A transcript that is not on disk yet is resolved one level up instead: the
// hook fires around the write, so refusing an unflushed file would turn a
// benign race into a 400 on a legitimate hook. Only the leaf may be missing,
// and only when the path carries no ".." — a parent reference cannot be
// resolved without the filesystem, and resolving it lexically is precisely the
// bypass this function exists to prevent.
func resolvePath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) || hasParentRef(path) {
		return "", err
	}
	dir, base := filepath.Split(path)
	if dir == "" || base == "" || base == "." || base == ".." {
		return "", err
	}
	resolvedDir, dirErr := filepath.EvalSymlinks(filepath.Clean(dir))
	if dirErr != nil {
		return "", dirErr
	}
	return filepath.Join(resolvedDir, base), nil
}

// hasParentRef reports whether path contains a ".." component.
func hasParentRef(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}
