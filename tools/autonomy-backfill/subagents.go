package main

import (
	"regexp"
	"strings"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

// Classifying a reconstructed run as a TOP-LEVEL run or a SUBAGENT run
// (#1905 subagents).
//
// The live daemon reads this straight off the session state's ParentSessionID.
// A back-fill has no session states — they are deleted when a session ends —
// so it reads the daemon's own event log, which describes the same two facts in
// words:
//
//	"new session detected in subagents (adapter=claude-code)"
//	   the session's transcript appeared under a subagent directory, so it is
//	   a child. This is the ONLY evidence for the many children that finish
//	   without ever emitting a parent-naming line.
//	"finished orphaned subagent (working → ready) — parent <id>"
//	"subagent completed via parent task-notification (working → ready, parent <id>)"
//	   the same fact PLUS the parent's session id.
//
// Every pattern below is DERIVED from the exported format string the daemon
// logs with, never retyped: services.NewSessionInfoFormat,
// services.SubagentOrphanedInfoFormat, services.SubagentCompletedInfoFormat and
// services.SubagentDirName. A reworded message therefore changes the pattern
// this tool matches with, instead of leaving it silently classifying children
// as top-level — which is the failure mode a hand-typed copy has, and the one
// that would look exactly like a machine that ran fewer subagents.
//
// WHAT THIS CANNOT SEE, stated because the third state exists to carry it:
//
//   - a session whose birth line has rotated out of the retained log. Its kind
//     is UNKNOWN, not top-level. Every cost-era span is in this bucket, since
//     the cost log reaches back months further than the event log does.
//   - a child of an adapter whose child layout the daemon's log never names.
//     The two parent-naming messages are written for every adapter, so such a
//     child is caught the moment it finishes through either path; one that
//     never does would read as top-level. On the machine this was measured
//     against, every one of the 386 children the log describes is a Claude Code
//     subagent caught by the directory rule (`of`-style census in the PR body).

// subagentIndex is what the event log knows about parentage.
type subagentIndex struct {
	// children are the sessions the log describes as a child, by any marker.
	children map[string]struct{}

	// parents maps a child session to its parent, for the children whose
	// completion message named one. A child can be in `children` and absent
	// here: knowing a run was a subagent's is what the default view needs, and
	// naming the parent is an extra the log supplies only sometimes.
	parents map[string]string

	// born are the sessions whose BIRTH the retained log recorded. Membership
	// is what separates "the log watched this session start, outside a subagent
	// directory" (top-level) from "the log never saw this session at all"
	// (unknown) — the distinction the whole third state exists for.
	born map[string]struct{}
}

func newSubagentIndex() *subagentIndex {
	return &subagentIndex{
		children: map[string]struct{}{},
		parents:  map[string]string{},
		born:     map[string]struct{}{},
	}
}

// sessionIDPattern is what a session id looks like in a message. Deliberately
// loose — ids are adapter-shaped (uuids, `agent-<hex>`, filename stems) and the
// only thing they share is that they run to the next space.
const sessionIDPattern = `(\S+)`

// anyTextPattern matches a message's free-text placeholder (a project
// directory, a state name) without assuming its shape.
const anyTextPattern = `(.*)`

var (
	// newSessionPattern captures the project directory and the adapter from a
	// birth line.
	newSessionPattern = patternFromFormat(services.NewSessionInfoFormat, anyTextPattern, `([^)]*)`)

	// orphanedPattern and completedPattern each capture the state the child
	// left and, second, the PARENT's session id.
	orphanedPattern  = patternFromFormat(services.SubagentOrphanedInfoFormat, anyTextPattern, sessionIDPattern)
	completedPattern = patternFromFormat(services.SubagentCompletedInfoFormat, anyTextPattern, sessionIDPattern)
)

// patternFromFormat turns one of the daemon's `%s` log formats into a regexp,
// escaping every literal part and substituting the given sub-pattern for each
// placeholder in order.
//
// This is what "derived, never retyped" costs and buys: the tool's matcher is a
// function of the daemon's own wording, so the two cannot drift apart in
// silence. A format with a different number of `%s` than sub-patterns given
// yields a pattern that matches nothing, which
// TestSubagentPatternsMatchTheDaemonsOwnMessages catches by running the real
// format strings through fmt.Sprintf and requiring a match.
func patternFromFormat(format string, groups ...string) *regexp.Regexp {
	parts := strings.Split(format, "%s")
	var b strings.Builder
	for i, part := range parts {
		b.WriteString(regexp.QuoteMeta(part))
		if i < len(parts)-1 {
			if i < len(groups) {
				b.WriteString(groups[i])
			} else {
				// More placeholders than sub-patterns: match nothing rather
				// than match loosely. A tool that silently matched half a
				// message would classify from a coincidence.
				b.WriteString(`(?!)`)
			}
		}
	}
	return regexp.MustCompile(b.String())
}

// observe folds one session-detector log message into the index, and reports
// whether the line said anything about parentage — so the parse census counts a
// line this tool actually used rather than silently reading more than it
// admits to.
func (ix *subagentIndex) observe(sessionID, message string) bool {
	if sessionID == "" {
		return false
	}
	if m := newSessionPattern.FindStringSubmatch(message); m != nil {
		ix.born[sessionID] = struct{}{}
		if isSubagentProjectDir(m[1]) {
			ix.children[sessionID] = struct{}{}
		}
		return true
	}
	for _, re := range []*regexp.Regexp{orphanedPattern, completedPattern} {
		m := re.FindStringSubmatch(message)
		if m == nil {
			continue
		}
		ix.children[sessionID] = struct{}{}
		if parent := m[2]; parent != "" {
			// First naming wins, so a session named twice resolves the same way
			// on every run — a back-fill whose output depends on which line it
			// happened to read last cannot be checked against its own dry run.
			if _, seen := ix.parents[sessionID]; !seen {
				ix.parents[sessionID] = parent
			}
		}
		return true
	}
	return false
}

// isSubagentProjectDir reports whether a birth line's project directory is the
// one Claude Code writes subagent transcripts under.
//
// A Workflow-tool agent sits one level deeper
// (.../subagents/workflows/<run-id>/agent-<id>.jsonl) and so reports the run id
// as its directory, which this cannot recognise. Such a session lands in
// `unknown` rather than in `top` unless one of the two completion messages
// names it — which is the safe direction, and the reason the third state is not
// optional.
func isSubagentProjectDir(dir string) bool {
	return dir == services.SubagentDirName
}

// classify returns the run kind and parent id for one session.
//
// THREE OUTCOMES, and the third is the one that must not collapse into the
// first:
//
//	subagent — a marker in the log said so.
//	top      — the log recorded this session's birth and never called it a
//	           child. That is a positive observation, not an absence.
//	unknown  — the log never saw this session start. Nothing here establishes
//	           either kind, and guessing "top" would put every cost-era run into
//	           the default view under a claim nobody measured.
func (ix *subagentIndex) classify(sessionID string) (kind, parent string) {
	if _, ok := ix.children[sessionID]; ok {
		return session.AutonomyKindSubagent, ix.parents[sessionID]
	}
	if _, ok := ix.born[sessionID]; ok {
		return session.AutonomyKindTopLevel, ""
	}
	return session.AutonomyKindUnknown, ""
}

// kindCensus counts a span list by kind, for the report.
type kindCensus struct {
	TopLevel int
	Subagent int
	Unknown  int
	// ParentNamed is the subset of Subagent whose parent the log named. The
	// rest are known-child runs with an unnamed parent — excluded from the
	// default view just the same, since the exclusion turns on being a child,
	// not on which parent.
	ParentNamed int
}

func censusOf(spans []spanWithKind) kindCensus {
	var c kindCensus
	for _, s := range spans {
		switch s.Kind {
		case session.AutonomyKindSubagent:
			c.Subagent++
			if s.Parent != "" {
				c.ParentNamed++
			}
		case session.AutonomyKindTopLevel:
			c.TopLevel++
		default:
			c.Unknown++
		}
	}
	return c
}

// spanWithKind is the subset of outbound.AutonomySpan the census reads, so
// censusOf can be exercised without building whole spans.
type spanWithKind struct {
	Kind   string
	Parent string
}
