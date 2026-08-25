// vocab-source.go — a stand-in for core/domain/session/session.go, so the
// tests drive the vocabulary parser without depending on the real file's
// current contents.
package session

const (
	StateWorking = "working"
	StateWaiting = "waiting"
	StateReady   = "ready"
	StateError   = "error"

	// Deliberately present: `State[A-Z]` also matches the tail of this
	// identifier, which is why the parser resolves names taken from the
	// slice literal instead of pattern-matching constants directly.
	CompactionStateNotCompacting = "not_compacting"
)

var canonicalStates = []string{StateWorking, StateWaiting, StateReady, StateError}
