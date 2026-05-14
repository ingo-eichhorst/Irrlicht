// Package preflight gates the recorder behind a per-agent prerequisites
// manifest. Some scenarios need maintainer-only setup (auth tokens, paid
// subscriptions, signing certs, MCP keys) that the autonomous agent
// running Phase 1 cannot perform.
//
// The manifest lives at `replaydata/agents/<agent>/prerequisites.md` and
// is maintainer-authored. The maintainer signals completion by touching
// `.agent-onboarding/prereqs-<agent>.ok` (relative to the repo root).
//
// The recorder refuses to start if:
//   - the manifest exists and the .ok file is missing, OR
//   - the manifest exists and was modified more recently than the .ok file
//     (i.e. the prereqs list changed after the maintainer last confirmed).
//
// A missing manifest is treated as "no prereqs declared, OK to record" so
// brand-new agents work without ceremony.
package preflight

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrPrereqsNotMet is returned when the .ok file is missing or stale.
var ErrPrereqsNotMet = errors.New("prerequisites not met")

// Result reports what the preflight check found, in human-readable form
// the recorder can quote back to the user before exiting.
type Result struct {
	ManifestPath string // path checked (whether or not it existed)
	OKPath       string // path checked (whether or not it existed)
	Status       string // "ok", "no-manifest", "missing-ok", "stale-ok"
	Detail       string // one-line explanation suitable for CLI output
}

// Check validates the prereqs state for one agent.
//
//	repoRoot: absolute or relative path to the repo root (where .agent-onboarding/ lives)
//	agent:    adapter slug (e.g. "claudecode")
//
// Returns Result with Status="ok" and a nil error when the recorder may
// proceed. Returns ErrPrereqsNotMet wrapped with a clear message on
// missing or stale .ok.
func Check(repoRoot, agent string) (Result, error) {
	manifest := filepath.Join(repoRoot, "replaydata", "agents", agent, "prerequisites.md")
	okPath := filepath.Join(repoRoot, ".agent-onboarding", fmt.Sprintf("prereqs-%s.ok", agent))

	r := Result{ManifestPath: manifest, OKPath: okPath}

	manSt, err := os.Stat(manifest)
	if err != nil {
		if os.IsNotExist(err) {
			r.Status = "no-manifest"
			r.Detail = fmt.Sprintf("no prerequisites.md for %q — proceeding without prereq gate", agent)
			return r, nil
		}
		return r, fmt.Errorf("stat manifest: %w", err)
	}

	okSt, err := os.Stat(okPath)
	if err != nil {
		if os.IsNotExist(err) {
			r.Status = "missing-ok"
			r.Detail = fmt.Sprintf("prerequisites.md exists but %s is missing.\n"+
				"  Read the manifest, complete each item, then `touch %s` to acknowledge.",
				okPath, okPath)
			return r, fmt.Errorf("%w: %s", ErrPrereqsNotMet, r.Detail)
		}
		return r, fmt.Errorf("stat ok file: %w", err)
	}

	if manSt.ModTime().After(okSt.ModTime()) {
		r.Status = "stale-ok"
		r.Detail = fmt.Sprintf("prerequisites.md was modified after %s.\n"+
			"  Re-read the manifest (it may list new items), then `touch %s` to re-acknowledge.",
			okPath, okPath)
		return r, fmt.Errorf("%w: %s", ErrPrereqsNotMet, r.Detail)
	}

	r.Status = "ok"
	r.Detail = fmt.Sprintf("prereqs OK for %q (last acknowledged %s)", agent, okSt.ModTime().UTC().Format("2006-01-02 15:04:05Z"))
	return r, nil
}
