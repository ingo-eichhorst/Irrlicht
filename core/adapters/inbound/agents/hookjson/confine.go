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
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

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

	// One counter per reason, indexed by rejectReasons order. A fixed array of
	// atomics rather than a mutex-guarded map: the counters exist to observe a
	// rejection BURST, so instrumentation that serializes exactly when
	// rejections arrive together would blunt the thing it measures.
	counts [len(rejectReasons)]atomic.Uint64
}

// rejectReasons is every reason in a fixed order, so each has a stable counter
// slot. RejectNone is deliberately absent — it is not a rejection.
var rejectReasons = [...]RejectReason{
	RejectEmptyPath,
	RejectRelativePath,
	RejectWrongSuffix,
	RejectNoRoots,
	RejectUnresolvable,
	RejectEscapesRoot,
}

// NewPathConfiner builds a confiner over roots, accepting only files whose
// extension is suffix (e.g. ".jsonl"; empty means any).
//
// roots is a func, not a slice, because a root is env-dependent
// (CLAUDE_CONFIG_DIR, CODEX_HOME) and the declaration that produced it is
// evaluated lazily. Each returned element is either absolute or $HOME-relative,
// exactly as agent.FilesUnderRoot documents; agentpaths.AbsRoot closes that gap.
func NewPathConfiner(roots func() []string, suffix string) *PathConfiner {
	return &PathConfiner{roots: roots, suffix: suffix}
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
		// Drop empty entries rather than resolving them. AllRootsFor always
		// returns at least one element — RootDirFor is prepended
		// unconditionally — so an adapter whose DirFunc failed and returned ""
		// would otherwise present a root that resolves to $HOME, turning the
		// fail-closed branch below into confinement over the entire home
		// directory. An empty root is the absence of a root.
		roots := files.AllRootsFor(goos)
		kept := make([]string, 0, len(roots))
		for _, root := range roots {
			if strings.TrimSpace(root) != "" {
				kept = append(kept, root)
			}
		}
		return kept
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
// DECLARED root rather than the symlink-resolved one so the ROOT PREFIX stays
// in the namespace the fswatcher emits paths in — that side is not
// symlink-resolved, downstream state is keyed by transcript path, and two
// spellings of one file are two sessions. On a machine whose $HOME or temp dir
// sits behind a symlink (/var → /private/var on macOS) this is what keeps the
// hook's key and the watcher's key equal.
//
// The relative component is a resolved one, so the guarantee stops at the
// prefix: a symlink INSIDE the tree is collapsed here while the watcher reports
// the link name. No shipped adapter layout has one, and the failure is quiet
// and partial rather than wrong — hook-delivered rate limits, task estimates
// and summaries miss their tailer and are dropped, while state classification,
// which is keyed by session id, is unaffected.
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
	// Re-assert the extension on the RESOLVED path. The pre-check above reads
	// the caller's string, which an in-tree symlink can spell ".jsonl" while
	// pointing at something else entirely — the daemon would then tail a
	// non-transcript file and derive a session id from its name.
	if c.suffix != "" && filepath.Ext(resolved) != c.suffix {
		return "", c.count(RejectWrongSuffix)
	}
	for _, declared := range roots {
		if confined, ok := containedIn(declared, resolved); ok {
			return confined, RejectNone
		}
	}
	return "", c.count(RejectEscapesRoot)
}

// RejectPath writes the uniform refusal every hook receiver owes a confinement
// failure. It is the ONE place the wire behaviour of a refusal is decided, so
// every receiver — present and future — inherits it instead of choosing.
//
// The refusal is loud in the daemon and silent on the wire: an error-level log
// line naming the reason and the offending path, the confiner's counter
// incremented, and a 200 with an empty body. The path is never forwarded and
// nothing is opened, so the security property does not depend on the status
// code at all.
//
// Why 2xx rather than 400, recorded because the next reader will otherwise flip
// it on aesthetics (issue #1361 item 4 asked for a 400; issue #1364 asks for
// 200, and this is the reconciliation):
//
//   - OBSERVED, from the Claude Code hooks guide: "HTTP status codes alone
//     can't block actions." A non-2xx from a `type: http` hook is documented as
//     a non-blocking error; to block or deny you must return 2xx with a JSON
//     decision body. So a 400 here cannot deny a tool call or stall a turn.
//   - OBSERVED, from the claude 2.1.226 binary: the HTTP hook client sets
//     `validateStatus: () => true` and returns `{ok: status>=200 && status<300,
//     statusCode, body}` — a 400 is simply `ok:false`, not an exception.
//   - NOT OBSERVED: whether `ok:false` surfaces an error to the user, is
//     retried, or is merely logged. The docs do not say and I did not run a
//     live probe. That unknown sits on the user's critical path.
//   - NOT OBSERVED for the other CLIs at all. Codex delivers via `curl ... ||
//     true`, so its exit code is already swallowed — but gemini-cli's pre-tool
//     hooks fail CLOSED (a non-zero result yields decision "deny" and the tool
//     is skipped) and Copilot's preToolUse denies on error. A receiver added
//     for one of those adapters would inherit whatever this function does, and
//     for them a non-2xx is known to be dangerous.
//
// An empty-bodied 200 is the response this handler already returns on its
// success path, so it is the one interaction with the CLI that is continuously
// exercised. Choosing it trades nothing: "never silently drop" is satisfied by
// the log and the counter, which is where an operator looks, and not by a
// status code the agent may or may not show anyone.
//
// The one exception is a MISSING transcript_path, which stays a 400. That is a
// malformed body rather than a verdict about a path — the field the receiver
// keys everything on is simply absent — and both receivers already answered 400
// for it before this change, so it is the status the CLIs have been given all
// along. Keeping it preserves that, and confines this function's new opinion to
// the cases that are genuinely new.
//
// component is the caller's Logger component tag; sessionID is empty because
// confinement runs before an id can be derived from the path.
func RejectPath(w http.ResponseWriter, log outbound.Logger, component, raw string, reason RejectReason) {
	log.LogError(component, "", fmt.Sprintf("rejected hook transcript_path %q: %s", raw, reason))
	if reason == RejectEmptyPath {
		http.Error(w, "bad request: missing transcript_path", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Rejections returns a snapshot of the per-reason rejection counts. Nothing
// surfaces these to a UI yet — there is no daemon-wide counter registry to
// publish into — but they make "rejected and counted" an observable fact
// rather than a claim, which is what the contract assertion checks.
func (c *PathConfiner) Rejections() map[RejectReason]uint64 {
	out := make(map[RejectReason]uint64, len(rejectReasons))
	for i, reason := range rejectReasons {
		if n := c.counts[i].Load(); n > 0 {
			out[reason] = n
		}
	}
	return out
}

// RejectionCount returns the total number of paths this confiner has refused.
func (c *PathConfiner) RejectionCount() uint64 {
	var total uint64
	for i := range c.counts {
		total += c.counts[i].Load()
	}
	return total
}

// count records a rejection and returns the reason, so call sites read as
// `return "", c.count(reason)`.
func (c *PathConfiner) count(reason RejectReason) RejectReason {
	for i, known := range rejectReasons {
		if known == reason {
			c.counts[i].Add(1)
			break
		}
	}
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
//
// "Missing" has to mean absent from the directory, not merely unresolvable.
// EvalSymlinks reports fs.ErrNotExist for a DANGLING symlink exactly as it does
// for a file that was never created, and the two are not equivalent here: an
// attacker who plants a broken link inside the tree, has it accepted, and only
// then creates the target has escaped the root without ever needing a race.
// Lstat is what tells them apart — it succeeds on a symlink whatever its target
// — so an existing leaf is a resolution failure, never an unflushed write.
func resolvePath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	// The ".." test is written inline, as a plain strings.Contains, rather than
	// through a helper: it is a real guard (a parent reference cannot be
	// resolved without the filesystem, and resolving it lexically is the very
	// bypass this function exists to prevent) AND it is the form CodeQL's
	// go/path-injection query recognizes as a sanitizer for the Lstat below —
	// an interprocedural predicate is not (same lesson as session_meta.go's
	// guard and store.go's underRoot in #910). Contains is stricter than a
	// component-wise check, which is the right direction here: it also refuses
	// a not-yet-written leaf whose own name embeds "..", and no agent names a
	// transcript that way.
	if !errors.Is(err, fs.ErrNotExist) || strings.Contains(path, "..") {
		return "", err
	}
	if _, lerr := os.Lstat(path); !errors.Is(lerr, fs.ErrNotExist) {
		// The leaf is there (a dangling symlink, most likely) or its status
		// cannot be established. Either way this is not the write race.
		return "", err
	}
	// path is absolute (Confine checked) and free of "..", so Split always
	// yields a non-empty dir and base is neither "" nor "..".
	dir, base := filepath.Split(path)
	if base == "." {
		return "", err
	}
	resolvedDir, dirErr := filepath.EvalSymlinks(filepath.Clean(dir))
	if dirErr != nil {
		return "", dirErr
	}
	return filepath.Join(resolvedDir, base), nil
}
