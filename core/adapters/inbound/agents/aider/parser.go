package aider

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"irrlicht/core/pkg/tailer"
)

// Parser maps the markdown lines of `.aider.chat.history.md` to
// tailer.ParsedEvent values. Aider does not write JSONL, so the parser
// implements tailer.RawLineParser; the JSONL ParseLine entry point is a
// no-op kept only to satisfy the TranscriptParser interface.
//
// State machine: a turn begins on a `#### …` line (user prompt). Plain
// prose lines accumulate as assistant text until the next `> Tokens: …`
// line, which marks end-of-one-model-call (NOT end-of-turn) and emits an
// assistant_message event carrying that call's PerTurnContribution. The
// turn stays open across multiple model calls — under `--yes-always`,
// aider auto-accepts file-add prompts and re-prompts the model multiple
// times within a single user turn.
//
// End-of-turn is signaled out-of-band: aider's idle TUI prompt never
// lands in the markdown chat history, so the parser implements
// idleFlusher and synthesizes a turn_done event when the file has been
// quiet for aiderIdleTurnDoneAfter (1500ms). The tailer drives this via
// IdleFlush after each TailAndProcess pass.
type Parser struct {
	model           string
	assistantBuffer strings.Builder
	turnOpen        bool
	toolSeq         int
}

// aiderIdleTurnDoneAfter is the wall-clock idle window the parser waits
// after the last transcript line before declaring the turn over. Tuned
// to balance two failure modes: too short flips the session to ready
// mid-turn during a slow remote model call; too long delays the
// working→ready transition the user perceives as "aider is finished".
//
// This is a floor, not the actual user-visible latency. The daemon's
// SessionDetector polls working sessions every staleWorkingRefreshInterval
// (5s in core/application/services/session_detector.go), so the
// effective working→ready delay after the last transcript line is
// roughly max(this constant, the next refresh tick) — about 5s in
// practice when no other fswatcher event arrives.
const aiderIdleTurnDoneAfter = 1500 * time.Millisecond

var (
	// `> Tokens: 771 sent, 1 received.` (with optional cost suffixes)
	tokensRE = regexp.MustCompile(`^>\s*Tokens:\s*([\d.]+\s*[kKmM]?)\s*sent,\s*([\d.]+\s*[kKmM]?)\s*received(.*)$`)
	// `, $0.0123 message` / `, $0.0123 message, $0.456 session.`
	costMessageRE = regexp.MustCompile(`\$([\d.]+)\s*message`)
	// `> Model: openai/gemma-4-e2b-it-uncensored with whole edit format`.
	// Also matches `> Main model: …` which aider emits after a `/model`
	// switch — both are authoritative for the next turn's Contribution.Model.
	// `> Weak model: …` is intentionally not matched (aider's internal
	// quick-summary model, not the main turn).
	modelRE = regexp.MustCompile(`^>\s*(?:Main\s+)?[Mm]odel:\s*(\S+)`)
	// `> Applied edit to <file>` — file edit tool call
	appliedEditRE = regexp.MustCompile(`^>\s*Applied edit to\s+`)
	// `> Running <cmd>` or `> Running shell command:` — shell tool call
	runningRE = regexp.MustCompile(`^>\s*Running\s+`)
)

// ParseLine satisfies tailer.TranscriptParser but is unused: aider transcripts
// are markdown, so the tailer routes lines through ParseLineRaw instead.
func (p *Parser) ParseLine(_ map[string]interface{}) *tailer.ParsedEvent {
	return nil
}

// ParseLineRaw maps a single trimmed line of `.aider.chat.history.md` to a
// ParsedEvent. Returns nil for lines that contribute only to internal
// buffering (assistant prose accumulates between `####` and `> Tokens:`).
func (p *Parser) ParseLineRaw(line string) *tailer.ParsedEvent {
	if line == "" || strings.HasPrefix(line, "# aider chat started") {
		// Session header carries no event for the daemon — it already
		// emits transcript_new lifecycle events.
		return nil
	}

	if strings.HasPrefix(line, "#### ") {
		// User prompt. A new turn opens; reset the assistant buffer in case
		// the previous turn was interrupted before its `> Tokens:` line, or
		// the previous turn closed via IdleFlush but more prose arrived
		// after IdleFlush ran.
		p.assistantBuffer.Reset()
		p.turnOpen = true
		text := strings.TrimPrefix(line, "#### ")
		return &tailer.ParsedEvent{
			EventType:      "user_message",
			AssistantText:  truncate(text),
			ContentChars:   int64(len(text)),
			ClearToolNames: true,
		}
	}

	if m := modelRE.FindStringSubmatch(line); m != nil {
		p.model = tailer.NormalizeModelName(m[1])
		return &tailer.ParsedEvent{Skip: true, ModelName: p.model}
	}

	if m := tokensRE.FindStringSubmatch(line); m != nil {
		return p.closeModelCall(m)
	}

	if appliedEditRE.MatchString(line) {
		return p.toolCall("Edit")
	}
	if runningRE.MatchString(line) {
		return p.toolCall("Bash")
	}

	if strings.HasPrefix(line, ">") {
		// Other blockquote lines are aider status output (warnings,
		// confirmation prompts, invocation echo). Ignore.
		return nil
	}

	// Plain prose: assistant response. Buffer it for the next model-call
	// flush. ContentChars updates run on flush, not per line.
	if p.turnOpen {
		if p.assistantBuffer.Len() > 0 {
			p.assistantBuffer.WriteString(" ")
		}
		p.assistantBuffer.WriteString(line)
	}
	return nil
}

// closeModelCall handles a `> Tokens:` line: aider has finished one model
// call. Emits assistant_message — NOT turn_done — carrying the per-call
// Contribution and the buffered assistant prose. The turn stays open
// because under `--yes-always` aider may auto-accept file-add prompts and
// re-prompt the model; multiple `> Tokens:` lines within one `####`
// user turn are normal. End-of-turn is synthesized later via IdleFlush
// when the transcript file has been quiet long enough.
func (p *Parser) closeModelCall(m []string) *tailer.ParsedEvent {
	sent := parseTokenCount(m[1])
	received := parseTokenCount(m[2])
	rest := m[3]

	contribution := &tailer.PerTurnContribution{
		Model: p.model,
		Usage: tailer.UsageBreakdown{
			Input:  sent,
			Output: received,
		},
	}
	if costMatch := costMessageRE.FindStringSubmatch(rest); costMatch != nil {
		if cost, err := strconv.ParseFloat(costMatch[1], 64); err == nil {
			contribution.ProviderCostUSD = &cost
		}
	}

	text := strings.TrimSpace(p.assistantBuffer.String())
	contentChars := int64(len(text))
	p.assistantBuffer.Reset()
	// turnOpen intentionally stays true: another model call may follow.

	return &tailer.ParsedEvent{
		EventType:     "assistant_message",
		ModelName:     p.model,
		AssistantText: truncate(text),
		ContentChars:  contentChars,
		Contribution:  contribution,
		Tokens: &tailer.TokenSnapshot{
			Input:  sent,
			Output: received,
			Total:  sent + received,
		},
	}
}

// IdleFlush synthesizes a turn_done event when aider has been quiet for
// at least aiderIdleTurnDoneAfter. Returns nil when no turn is open or
// the threshold hasn't been reached. The tailer's idleFlusher hook calls
// this once per TailAndProcess pass; per-call tokens and cost are already
// accumulated via the assistant_message events emitted at each
// `> Tokens:` line, so the synthesized turn_done carries no payload —
// its only job is to flip LastEventType so the state classifier
// transitions working → ready.
func (p *Parser) IdleFlush(idleFor time.Duration) *tailer.ParsedEvent {
	if !p.turnOpen {
		return nil
	}
	if idleFor < aiderIdleTurnDoneAfter {
		return nil
	}
	p.turnOpen = false
	p.assistantBuffer.Reset()
	// Stamp the synthesized event with wall-clock time so the tailer's
	// sliding-window message metrics (MessagesPerMinute, etc.) don't bucket
	// it at the zero time.
	return &tailer.ParsedEvent{EventType: "turn_done", Timestamp: time.Now()}
}

func (p *Parser) toolCall(name string) *tailer.ParsedEvent {
	p.toolSeq++
	id := fmt.Sprintf("aider-tool-%d", p.toolSeq)
	// Aider runs with --yes-always; tools execute immediately, so we emit
	// the open and close in one event. The state classifier sees a
	// completed tool call without a permission window.
	return &tailer.ParsedEvent{
		EventType:     "tool_result",
		ToolUses:      []tailer.ToolUse{{ID: id, Name: name}},
		ToolResultIDs: []string{id},
	}
}

// parseTokenCount turns aider's compact display ("1.2k", "543", "2M") into a raw integer.
func parseTokenCount(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := int64(1)
	last := s[len(s)-1]
	if last == 'k' || last == 'K' {
		mult = 1000
		s = s[:len(s)-1]
	} else if last == 'm' || last == 'M' {
		mult = 1_000_000
		s = s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int64(v * float64(mult))
}

func truncate(s string) string {
	const max = 200
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return "…" + string(runes[len(runes)-max:])
}
