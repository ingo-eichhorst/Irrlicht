// unknown.go makes an unrecognized hook event visible (issue #1364).
//
// Before this, both receivers ended their event switch the same way: an Info
// log line, an HTTP 200, and nothing else. That is indistinguishable from
// health. The failure it hides is not exotic — an upstream CLI renames the
// event we key permission detection on, every POST lands in the default branch,
// the permission still reads `granted`, the installed config still contains our
// entries, and state detection just quietly gets worse. Hook schemas are young
// enough that this is a when.
//
// Three properties, and each one is a deliberate choice against an obvious
// alternative:
//
//   - COUNTED PER (adapter, event name), not per adapter and not globally. A
//     rename fires on every single tool call, forever; a stray event from some
//     local process fires once. A single scalar cannot tell those apart, and
//     telling them apart is the entire diagnostic value.
//   - LOGGED ONCE PER DISTINCT NAME, at error level. A rename would otherwise
//     write a log line per tool call — the log stops being readable exactly when
//     someone needs to read it. The counter carries the volume; the log carries
//     the name. Error rather than Info because Info is where this was already
//     hiding, and because outbound.Logger has no level in between.
//   - STILL ANSWERED 2xx. Not a preference — see RejectPath's doc comment in
//     confine.go for the evidence, and the contract assertion that pins it. This
//     function deliberately takes no http.ResponseWriter so it cannot acquire an
//     opinion about the status code later; the caller's existing 200 stands.
//
// The counter shape mirrors PathConfiner's rejection counters (confine.go) and
// the splicer's fallback counters (fallback.go) on purpose — same package, same
// problem, same accessors: a snapshot map that omits zeros, plus a total. The
// one place it cannot mirror them is the storage: those index a fixed array by
// a closed enum of reasons, and an event NAME is caller-supplied on a local,
// unauthenticated endpoint, so the key space is open. That is what the bounded
// table below is for.
package hookjson

import (
	"fmt"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"irrlicht/core/ports/outbound"
)

// Both bounds exist for the threat model in the file header: the name is
// caller-supplied on an unauthenticated endpoint and is retained for the life
// of the process. Real event names are short PascalCase identifiers, so both
// are far past anything an upstream rename produces.
const (
	// maxUnknownEventNames bounds how many distinct (adapter, event) pairs are
	// retained.
	maxUnknownEventNames = 64

	// maxUnknownEventNameLen bounds the retained length of one name, in bytes.
	maxUnknownEventNameLen = 64
)

// UnknownEvent identifies one unrecognized hook event: which adapter's receiver
// saw it, and the name the CLI sent. Both halves are the key — an event named
// "Stop" arriving at an adapter that does not implement Stop is a different fact
// from the same name arriving at one that does.
type UnknownEvent struct {
	Adapter string
	Event   string
}

// unknownOutcome is what observeUnknownEvent decided, so IgnoreUnknownEvent
// reports the transitions worth a log line and stays silent on the two that
// would flood.
type unknownOutcome int

const (
	// unknownRepeat — this name has been seen before in this process. Counted,
	// not logged; this is the case that would flood.
	unknownRepeat unknownOutcome = iota
	// unknownFirstSighting — a name not seen before. Counted and logged.
	unknownFirstSighting
	// unknownTableFullFirst — the bounded table is saturated and this is the
	// FIRST sighting dropped for this adapter. Logged, so saturation is
	// announced once per adapter rather than once per process: a table filled by
	// junk aimed at one adapter must not silence the announcement for a real
	// rename arriving at another.
	unknownTableFullFirst
	// unknownTableFullRepeat — saturated, and this adapter has already
	// announced it. Totalled in its drop counter only.
	unknownTableFullRepeat
)

var (
	// unknownMu guards the two maps below. Once a pair has a slot, further
	// sightings go through its atomic without taking any write lock — which
	// matters because the thing these counters exist to observe is a BURST, and
	// a burst is by definition the same name arriving repeatedly. Serializing
	// exactly then would blunt what is being measured, the same argument
	// confine.go makes for its fixed array of atomics.
	unknownMu     sync.RWMutex
	unknownCounts = make(map[UnknownEvent]*atomic.Uint64)

	// unknownDropped counts, PER ADAPTER, the sightings refused a slot because
	// the table was full.
	//
	// Per adapter rather than one global scalar, because the table is shared and
	// its cap is a process-wide resource: a local process can fill all 64 slots
	// with junk — the endpoint is unauthenticated — and every genuine rename
	// after that is refused a row. A single scalar climbing into the thousands
	// says only "something", which is the exact "a single scalar cannot tell
	// those apart" failure this file exists to prevent. Keyed by adapter, it
	// still says WHICH CLI is flooding. The name is not recoverable once the
	// table is full; that residual is the price of the bound, and the
	// first-drop log line at least names the first one.
	unknownDropped = make(map[string]*atomic.Uint64)
)

// IgnoreUnknownEvent is the uniform handling every hook receiver owes an event
// name it does not recognize. It is the ONE place that behaviour is decided —
// the sibling of RejectPath, and for the same reason: two receivers had already
// written the identical default branch by hand, and the third would have had to
// remember to.
//
// It counts the sighting, reports the first occurrence of each distinct name,
// and returns. It writes NO response: the caller has already committed to
// answering 2xx on this path, and that rule is asserted by
// contracttesting.AssertUnknownHookEventObserved rather than left to each
// receiver.
//
// component is the caller's Logger component tag; adapter is the adapter name
// the count is keyed by; sessionID is the resolved session the event arrived
// for, purely so the log line joins up with the rest of that session's trail.
func IgnoreUnknownEvent(log outbound.Logger, component, adapter, sessionID, event string) {
	outcome, name := observeUnknownEvent(adapter, event)
	switch outcome {
	case unknownFirstSighting:
		log.LogError(component, sessionID, fmt.Sprintf(
			"unrecognized hook event %q from %s: ignored, answered 2xx. Further occurrences of this name are counted, not logged — an upstream event rename looks exactly like this, so check the count in the diagnostics bundle's hooks.json before dismissing it.",
			name, adapter))
	case unknownTableFullFirst:
		log.LogError(component, sessionID, fmt.Sprintf(
			"unrecognized hook event %q from %s: ignored, and NOT counted by name — the distinct-name table is full at %d entries, so no further name from this adapter can be attributed. Later sightings are totalled per adapter in hooks.json's unknown_event_names_dropped_by_adapter.",
			name, adapter, maxUnknownEventNames))
	case unknownRepeat, unknownTableFullRepeat:
		// Counted. Deliberately silent: these are the flooding cases.
	}
}

// observeUnknownEvent records one sighting and reports what kind it was.
//
// The double-checked shape is not premature optimization: the fast path is the
// repeat, and the write lock is taken only on a transition that also produces a
// log line — so "first sighting" and "first drop for this adapter" are each
// decided by exactly one goroutine even when several arrive together, which is
// what makes "reported once" true rather than approximately true. Both
// decisions are made under the same lock as the state they consult, so there is
// no piece of this state living outside the mutex's protection domain.
func observeUnknownEvent(adapter, event string) (unknownOutcome, string) {
	key := UnknownEvent{Adapter: adapter, Event: truncateEventName(event)}

	unknownMu.RLock()
	counter := unknownCounts[key]
	var dropped *atomic.Uint64
	if counter == nil && len(unknownCounts) >= maxUnknownEventNames {
		// Post-saturation fast path: read the adapter's drop counter under the
		// SAME read lock the map lookup above already needs, so a flood of
		// unretainable names never reaches the exclusive lock.
		dropped = unknownDropped[adapter]
	}
	unknownMu.RUnlock()
	if counter != nil {
		counter.Add(1)
		return unknownRepeat, key.Event
	}
	if dropped != nil {
		dropped.Add(1)
		return unknownTableFullRepeat, key.Event
	}

	unknownMu.Lock()
	defer unknownMu.Unlock()
	if counter := unknownCounts[key]; counter != nil {
		// Lost the race to insert. The winner reports; this one only counts.
		counter.Add(1)
		return unknownRepeat, key.Event
	}
	if len(unknownCounts) >= maxUnknownEventNames {
		if dropped := unknownDropped[adapter]; dropped != nil {
			dropped.Add(1)
			return unknownTableFullRepeat, key.Event
		}
		dropped := &atomic.Uint64{}
		dropped.Add(1)
		unknownDropped[adapter] = dropped
		return unknownTableFullFirst, key.Event
	}
	fresh := &atomic.Uint64{}
	fresh.Add(1)
	unknownCounts[key] = fresh
	return unknownFirstSighting, key.Event
}

// UnknownEvents returns a snapshot of the per-(adapter, name) sighting counts,
// in unspecified order — ordering is a presentation guarantee and belongs to
// the one place that owes it, the diagnostics bundle's hooksView. Sorting here
// too would mean the same comparator in two files, agreeing forever, with no
// test able to see the other go stale.
//
// No entry is ever zero — a pair only gets a slot when it is first counted — so
// unlike Fallbacks and Rejections there is nothing to omit; the property those
// promise ("zeros are absent") holds here by construction.
func UnknownEvents() []UnknownEventCount {
	unknownMu.RLock()
	defer unknownMu.RUnlock()
	out := make([]UnknownEventCount, 0, len(unknownCounts))
	for key, counter := range unknownCounts {
		out = append(out, UnknownEventCount{UnknownEvent: key, Count: counter.Load()})
	}
	return out
}

// UnknownEventCount is one row of the UnknownEvents snapshot.
type UnknownEventCount struct {
	UnknownEvent
	Count uint64
}

// UnknownEventTotal is the total number of unrecognized events seen, across
// every name — including the ones the table had no room to name. It is
// published in the diagnostics bundle rather than left for a reader to
// reconstruct by summing rows and remembering to add the drops.
func UnknownEventTotal() uint64 {
	unknownMu.RLock()
	defer unknownMu.RUnlock()
	var total uint64
	for _, counter := range unknownCounts {
		total += counter.Load()
	}
	for _, dropped := range unknownDropped {
		total += dropped.Load()
	}
	return total
}

// UnknownEventNamesDropped is how many sightings arrived, per adapter, after
// the distinct-name table saturated. A non-empty result means UnknownEvents is
// incomplete FOR THOSE ADAPTERS, which is exactly the thing a reader must not
// have to infer — and keyed by adapter it still says which CLI is producing
// them once the names themselves are no longer recoverable.
func UnknownEventNamesDropped() map[string]uint64 {
	unknownMu.RLock()
	defer unknownMu.RUnlock()
	out := make(map[string]uint64, len(unknownDropped))
	for adapter, dropped := range unknownDropped {
		if n := dropped.Load(); n > 0 {
			out[adapter] = n
		}
	}
	return out
}

// truncateEventName bounds a retained name at a rune boundary. Cutting mid-rune
// would store invalid UTF-8, which then has to survive JSON encoding into a
// diagnostics bundle.
//
// The bound means "once per distinct name" is really "once per distinct
// RETAINED name": two names sharing a 64-byte prefix collapse to one key. No
// real event name comes close, and the alternative is retaining whatever a local
// caller sends.
//
// Not session.CapRunes, which is the repo's canonical headline truncator: it
// bounds by RUNE count, and to do that it runs utf8.RuneCountInString over the
// whole string and then materializes []rune(s). On the input this bound exists
// for — a multi-megabyte name from a local caller — that is a full scan plus a
// 4x allocation of the very string we are refusing to keep. A byte bound reads
// at most 64 bytes.
func truncateEventName(name string) string {
	if len(name) <= maxUnknownEventNameLen {
		return name
	}
	cut := maxUnknownEventNameLen
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut] + "…"
}
