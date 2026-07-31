package session

// SignalTier ranks the channels a session-state signal can arrive on, from the
// most authoritative to the least.
//
// irrlicht has always had this ordering — "the agent's own hook outranks a
// regex over its prose" is the premise the whole state layer rests on — but
// until #1288 it existed only as the comment-numbered order of the if-statements
// inside ClassifyState. Nothing in a type said a hook outranks a transcript
// guess, so the invariant survived only as long as nobody reordered the
// function. This type is that invariant made explicit and testable, and it is
// what the remaining phases of #1129 plug into: each adds a signal *source*,
// and without a declared ladder each would also have to re-litigate where its
// signal sits relative to every other.
//
// Numerically lower means more authoritative, so the tiers can be compared
// directly — but use Outranks rather than `<`, which gets the zero value
// backwards (see TierNone).
type SignalTier uint8

const (
	// TierNone is the zero value: no tiered signal is attached. It is not a
	// tier — it outranks nothing and everything outranks it — so a struct
	// that never sets a tier degrades to "unknown provenance" rather than
	// silently claiming the top of the ladder, which a bare `<` comparison
	// would give it.
	TierNone SignalTier = 0

	// TierHook is an out-of-band push from the agent's own hook system:
	// Claude Code's PermissionRequest/Stop/Notification (#108, #1161, #1173)
	// and Codex's equivalents (#1171). The most authoritative tier available
	// — the agent reporting its own state rather than irrlicht guessing at
	// it — though not the fastest: only the blocking PermissionRequest hook
	// is instant, while Notification/idle_prompt was measured at ~6s (#1141).
	// Authoritative and late is exactly the combination SignalHolds exists to
	// reconcile.
	TierHook SignalTier = 1

	// TierStructured is a structured machine-readable stream that is not a
	// hook: OTel spans (Phase 4) and ACP session/update (Phase 7). Reserved —
	// nothing emits it yet. Below hooks because the OTel spike (#1141) found
	// its state-bearing span retrospective: it exports at the *end* of a
	// block, so it can corroborate a state but never open one live.
	TierStructured SignalTier = 2

	// TierProcess is derived from the OS view of the agent process: an
	// in-flight model API request seen as per-process network activity
	// (Phase 5). Reserved — nothing emits it yet.
	TierProcess SignalTier = 3

	// TierScreen is derived from the agent's rendered terminal output —
	// screen-stability or manifest matching against the live buffer
	// (Phase 6). Reserved — nothing emits it yet.
	TierScreen SignalTier = 4

	// TierTranscript is inference from the transcript's own content: the
	// turn-done heuristics, the open-tool rules, and waiting_cue's prose
	// regex. The least authoritative tier and the only one that answers a UI
	// question ("is a human blocked right now?") from a content log that was
	// never asked it — which is the inversion #1129 exists to undo.
	//
	// It is nonetheless load-bearing and stays that way: it is the instant
	// first guess for the two adapters that have hooks, and the *only* signal
	// for the seven that don't. Bottom of the ladder is not deprecated.
	TierTranscript SignalTier = 5
)

// Known reports whether t is an actual tier rather than the TierNone zero
// value.
func (t SignalTier) Known() bool {
	return t >= TierHook && t <= TierTranscript
}

// Outranks reports whether t is strictly more authoritative than other.
//
// Always prefer this to comparing tiers with `<`: TierNone is numerically
// lowest but is the *absence* of a tier, so a raw comparison would have it
// outrank every real signal — the one mistake this ladder cannot afford,
// since an untagged signal is exactly the case where nothing is known.
func (t SignalTier) Outranks(other SignalTier) bool {
	if !t.Known() {
		return false
	}
	if !other.Known() {
		return true
	}
	return t < other
}

// String renders the tier for logs and recorded lifecycle traces. The values
// are stable identifiers, not display prose: they are written into
// lifecycle.ClassifierInputs and read back by offline replay tooling, so
// renaming one silently reinterprets every trace already on disk.
func (t SignalTier) String() string {
	switch t {
	case TierHook:
		return "hook"
	case TierStructured:
		return "structured"
	case TierProcess:
		return "process"
	case TierScreen:
		return "screen"
	case TierTranscript:
		return "transcript"
	default:
		return ""
	}
}
