package session

// SessionError.Class is free-form by #1798's design — the agents' failure
// vocabularies genuinely differ, and flattening them into one enum would mean
// inventing mappings the payloads do not support. This file therefore names
// exactly ONE class, and only because that class has a cross-layer contract
// that a string literal cannot carry.
//
// Adapter-side classes (provider, rate_limit, auth, query, quota, aborted, and
// whatever the next adapter needs) stay literals in the adapter that emits
// them, next to the payload evidence for the mapping. They are display values
// with no other reader.
const (
	// ErrorClassProcessDeath marks a session that failed because the agent
	// PROCESS went away mid-turn (#1800), rather than because anything was
	// written into a transcript.
	//
	// RESERVED FOR THE DAEMON'S PROCESS-EXIT PRODUCER. No adapter parser sets
	// it, and none should: an adapter reads a transcript, so a failure it
	// reports is transcript-tier evidence by construction, and labelling it
	// process death would attach a claim about the OS view of the process to
	// something that never looked at the process.
	//
	// The classifier does NOT key on this value — it reads
	// SessionMetrics.ProcessDeath, which only the SignalProcessDeath hold can
	// set. That separation is deliberate: the evidence TIER must not be
	// derivable from a string an adapter could write. This constant is what
	// the producer puts on the user-visible error line, and what a test can
	// assert against without re-typing the literal.
	ErrorClassProcessDeath = "process_death"
)
