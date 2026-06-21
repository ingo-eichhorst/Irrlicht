package transcript

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// subagentSpawnWindow is how far before a child's transcript mtime a sibling
// must have been active to be a parent candidate. The parent writes its spawn
// line as the child is born, so a generous window reliably includes it while
// excluding stale conversations from the scan.
const subagentSpawnWindow = 2 * time.Minute

// maxSubagentSiblingScan caps how many sibling conversations are inspected when
// resolving a subagent's parent — the parent is necessarily one of the most
// recently active conversations (it wrote the spawn line as the child was
// created), so the newest-by-mtime window comfortably contains it while
// bounding the scan on a long-lived brain store.
const maxSubagentSiblingScan = 40

// subagentParentTailBytes is how much of each sibling transcript's tail to read
// when searching for the child's conversationId. The INVOKE_SUBAGENT line that
// announces a child sits near the parent's tail at spawn time; reading the tail
// keeps the scan cheap without missing it.
const subagentParentTailBytes = 256 * 1024

// AntigravityParentConvID resolves the parent conversation of an Antigravity
// subagent transcript, or "" when the transcript isn't a subagent (or isn't an
// Antigravity transcript at all). Antigravity runs each subagent as its own
// top-level conversation — brain/<child>/… — so the child's path does NOT
// encode its parent (unlike the path-based links of codex/pi/gemini). The link
// lives in the PARENT: a spawning conversation records an INVOKE_SUBAGENT step
// whose content carries `"conversationId": "<child>"`.
//
// So the parent is found by scanning sibling conversations for one whose
// transcript references this child's conversationId. The conversationId is a
// random UUID, so a substring hit is an unambiguous match. The scan is bounded
// to the most-recently-active siblings (the parent is active at spawn time) and
// reads only each transcript's tail.
//
// Called from the session detector's parent-derivation chain at child-session
// creation; by then the daemon's processing latency guarantees the parent's
// INVOKE_SUBAGENT line is already on disk (it is written as the child dir
// appears).
func AntigravityParentConvID(childTranscriptPath string) string {
	childConv, brainParent := antigravityConvAndBrainParent(childTranscriptPath)
	if childConv == "" {
		return ""
	}
	brain := filepath.Join(brainParent, "brain")
	entries, err := os.ReadDir(brain)
	if err != nil {
		return ""
	}

	// The parent wrote its INVOKE_SUBAGENT line as this child was created, so it
	// is necessarily active around the child's birth. Restrict the scan to
	// siblings modified within a window before the child's transcript mtime;
	// this keeps the common case — a top-level (non-subagent) conversation,
	// which has no parent — from tail-reading every sibling for nothing, while
	// always retaining the real parent (its mtime only grows after the spawn).
	childInfo, err := os.Stat(childTranscriptPath)
	if err != nil {
		return ""
	}
	cutoff := childInfo.ModTime().Add(-subagentSpawnWindow).UnixNano()

	type sib struct {
		path  string
		mtime int64
	}
	sibs := make([]sib, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || e.Name() == childConv {
			continue
		}
		p := filepath.Join(brain, e.Name(), ".system_generated", "logs", "transcript.jsonl")
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		mt := info.ModTime().UnixNano()
		if mt < cutoff {
			continue
		}
		sibs = append(sibs, sib{path: p, mtime: mt})
	}
	// Newest first — the parent is among the most recently active.
	sort.Slice(sibs, func(i, j int) bool { return sibs[i].mtime > sibs[j].mtime })
	if len(sibs) > maxSubagentSiblingScan {
		sibs = sibs[:maxSubagentSiblingScan]
	}

	for _, s := range sibs {
		if transcriptTailContains(s.path, childConv) {
			// s.path = …/brain/<parent>/.system_generated/logs/transcript.jsonl
			return filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(s.path))))
		}
	}
	return ""
}

// transcriptTailContains reports whether the tail of the file at path contains
// needle. Reads at most subagentParentTailBytes from the end.
func transcriptTailContains(path, needle string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	start := int64(0)
	if info.Size() > subagentParentTailBytes {
		start = info.Size() - subagentParentTailBytes
	}
	if _, err := f.Seek(start, 0); err != nil {
		return false
	}
	buf := make([]byte, info.Size()-start)
	// io.ReadFull, not a bare Read: a single Read may return fewer bytes than
	// requested for a 256KB buffer, leaving the tail zero-filled and silently
	// dropping a needle that lands in the unread portion.
	if _, err := io.ReadFull(f, buf); err != nil {
		return false
	}
	return strings.Contains(string(buf), needle)
}
