package agent

import (
	"time"

	"irrlicht/core/pkg/tailer"
)

// FileParser is the sub-sum carried by FilesUnderRoot. It describes how
// to interpret transcript content: JSONL lines, raw text lines (rare), or
// (Phase C) whole-document re-parse on every change.
//
// FilesUnderCWD pairs with RawLineParser directly (no sub-sum) because
// every cwd-resident transcript format we currently support is non-JSONL.
type FileParser interface {
	isFileParser()
}

// JSONLineParser is the default file-parser variant — append-only JSONL.
// NewParser returns a fresh stateful LineParser per file; the runtime
// invokes ParseLine for each transcript line and accumulates state inside
// the LineParser instance.
type JSONLineParser struct {
	NewParser func() LineParser
}

func (JSONLineParser) isFileParser() {}

// RawLineParser is used by adapters whose source format is not JSONL
// (aider's .aider.chat.history.md is markdown). ParseLineRaw is called for
// each raw line; IdleFlush synthesizes a turn_done event after the
// transcript has been quiet for `idleFor` because raw formats typically
// don't write an explicit end-of-turn marker.
//
// FilesUnderCWD always pairs with this type. FilesUnderRoot could in
// principle carry one, though no currently-supported adapter does so.
type RawLineParser struct {
	ParseLineRaw func(line string) *tailer.ParsedEvent
	IdleFlush    func(idleFor time.Duration) *tailer.ParsedEvent
}

func (RawLineParser) isFileParser() {}

// LineParser parses a single JSONL line into a normalized ParsedEvent.
// Adapter packages provide stateful implementations (Claude Code tracks
// pending turns; Codex tracks a cumulative usage cursor). Returns nil for
// lines that should be silently ignored.
type LineParser interface {
	ParseLine(raw map[string]interface{}) *tailer.ParsedEvent
}

// PendingContributor is an optional refinement on LineParser. Stateful
// parsers (currently Claude Code) implement this to expose the in-progress
// turn's cost contribution. The metrics collector queries via type
// assertion: `if p, ok := lineParser.(PendingContributor); ok { … }`.
//
// Mirrors tailer.pendingContributor (unexported) — the public name lives
// here so consumers can take a dependency on the contract without
// importing the implementation package.
type PendingContributor interface {
	PendingContribution() *tailer.PerTurnContribution
}

// SubagentCounter is an optional refinement on LineParser. Adapters that
// model subagents inline in the parent transcript (Claude Code) implement
// this to report open child agents. Adapters that model subagents as
// separate transcript files (codex, pi) do not implement it; the daemon's
// subagent-summary walks file-based children instead.
//
// Replaces the legacy free-function `agents.SubagentCounter` typedef.
type SubagentCounter interface {
	OpenSubagents(m *tailer.SessionMetrics) int
}
