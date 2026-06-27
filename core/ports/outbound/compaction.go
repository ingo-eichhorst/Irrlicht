package outbound

// CompactKind selects the compaction strategy for a piece of session text.
// The two kinds carry different intent, so an adapter can shape the headline
// differently per kind (e.g. a question keeps its trailing "?", an intent reads
// as a statement). See issue #759.
type CompactKind int

const (
	// CompactIntent compacts the session's "what is this about" text (the
	// task-summary marker or the first user prompt) into a one-line headline.
	CompactIntent CompactKind = iota
	// CompactQuestion compacts the agent's pending question (the task-question
	// marker or the raw last-assistant text) into a one-line headline.
	CompactQuestion
)

// TextCompactor turns a possibly-bloated piece of session text into a terse
// one-line headline suitable for the sidebar, with the full text kept elsewhere
// for tooltips. It is a strategy seam (issue #759): the default deterministic
// adapter is a pure heuristic; a future LLM adapter can swap in without touching
// callers. There is deliberately no error return — a compactor that can't
// improve the text (or whose backend fails) returns the input unchanged (and
// logs via Logger), so the headline degrades gracefully rather than vanishing.
type TextCompactor interface {
	Compact(text string, kind CompactKind) string
}
