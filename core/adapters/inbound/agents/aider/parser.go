package aider

import "irrlicht/core/pkg/tailer"

// NoOpParser is a stub TranscriptParser that returns nil for every line,
// signaling "skip" to the tailer. Aider's transcript is markdown
// (`.aider.chat.history.md`), so the tailer's JSON-line gate filters lines
// out before they reach this parser anyway — this stub exists so the
// adapter satisfies agents.Config without committing to a parser shape.
type NoOpParser struct{}

// ParseLine matches the tailer.TranscriptParser interface. Returning nil
// signals the tailer to skip the line without applying metadata or events.
func (p *NoOpParser) ParseLine(_ map[string]interface{}) *tailer.ParsedEvent {
	return nil
}
