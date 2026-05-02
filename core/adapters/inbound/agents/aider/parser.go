package aider

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"irrlicht/core/pkg/tailer"
)

// Parser maps the markdown lines of `.aider.chat.history.md` to
// tailer.ParsedEvent values. Aider does not write JSONL, so the parser
// implements tailer.RawLineParser; the JSONL ParseLine entry point is a
// no-op kept only to satisfy the TranscriptParser interface.
//
// State machine: a turn begins on a `#### …` line (user prompt). Plain
// prose lines accumulate as assistant text until either:
//   - a `> Tokens: …` line closes the turn cleanly with usage, or
//   - an LLM-layer error blockquote (e.g. `> litellm.BadRequestError: …`)
//     aborts the turn before tokens are reported.
// Both paths emit a turn_done event so the state classifier returns the
// session to ready; only the clean path carries a PerTurnContribution.
type Parser struct {
	model           string
	assistantBuffer strings.Builder
	turnOpen        bool
	toolSeq         int
}

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
	// `> litellm.BadRequestError: …`, `> openai.RateLimitError: …`,
	// `> OpenAIException - …`, `> LookupError` — LLM-layer failures that
	// abort a turn without ever printing `> Tokens: …`. The trailing
	// alternation `(?:[: ]|$)` covers both delimited forms and bare
	// error tokens at end-of-line. Matched only inside an open turn so
	// we don't fabricate a turn for startup-banner noise.
	errorRE = regexp.MustCompile(`^>\s*\S*(?:Error|Exception)(?:[: ]|$)`)
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
		// the previous turn was interrupted before its `> Tokens:` line.
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
		return p.flushAssistantTurn(m)
	}

	if appliedEditRE.MatchString(line) {
		return p.toolCall("Edit")
	}
	if runningRE.MatchString(line) {
		return p.toolCall("Bash")
	}

	if p.turnOpen && errorRE.MatchString(line) {
		return p.flushErrorTurn(line)
	}

	if strings.HasPrefix(line, ">") {
		// Other blockquote lines are aider status output (warnings,
		// confirmation prompts, invocation echo). Ignore.
		return nil
	}

	// Plain prose: assistant response. Buffer it for the eventual
	// turn-flush. ContentChars updates run on flush, not per line.
	if p.turnOpen {
		if p.assistantBuffer.Len() > 0 {
			p.assistantBuffer.WriteString(" ")
		}
		p.assistantBuffer.WriteString(line)
	}
	return nil
}

func (p *Parser) flushAssistantTurn(m []string) *tailer.ParsedEvent {
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
	p.turnOpen = false

	// Emit turn_done so the state classifier returns the session to ready.
	// Aider's markdown has no separate "agent finished" marker — the
	// `> Tokens:` line both reports usage and signals turn completion.
	return &tailer.ParsedEvent{
		EventType:     "turn_done",
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

// flushErrorTurn closes the current turn after aider prints an LLM-layer
// error blockquote. Aider does not emit a `> Tokens: …` line in this case,
// so without this synthetic turn_done the session would stay stuck in
// `working`. The error text is surfaced as AssistantText so the dashboard
// shows what happened. Tokens/Contribution are intentionally nil because
// no usage was reported.
func (p *Parser) flushErrorTurn(line string) *tailer.ParsedEvent {
	errText := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), ">"))
	p.assistantBuffer.Reset()
	p.turnOpen = false
	return &tailer.ParsedEvent{
		EventType:     "turn_done",
		ModelName:     p.model,
		AssistantText: truncate(errText),
		ContentChars:  int64(len(errText)),
	}
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
