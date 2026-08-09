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
	"sort"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"irrlicht/core/ports/outbound"
)

const (
	// MaxUnknownEventNames bounds how many distinct (adapter, event) pairs are
	// retained. The hook endpoints are local and unauthenticated — confine.go
	// says so in its first paragraph, and it is the reason that file exists —
	// so any process on the machine can POST an event name of its choosing. An
	// unbounded map keyed by that string is a memory-growth primitive handed to
	// an unauthenticated caller. Real adapters recognize a handful of events
	// each, so a table this size is far past anything an upstream rename can
	// produce and far short of anything that matters for memory.
	MaxUnknownEventNames = 64

	// maxUnknownEventNameLen bounds the retained length of a single name, for
	// the same reason: the pair is held for the life of the process, so a
	// multi-megabyte "event name" must not be what is held. Every real event
	// name is a short PascalCase identifier.
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

// unknownOutcome is what observeUnknownEvent decided, so IgnoreUnknownEvent can
// log the two noteworthy transitions and stay silent on the common one.
type unknownOutcome int

const (
	// unknownRepeat — this name has been seen before in this process. Counted,
	// not logged; this is the case that would flood.
	unknownRepeat unknownOutcome = iota
	// unknownFirstSighting — a name not seen before. Counted and logged.
	unknownFirstSighting
	// unknownTableFull — the bounded table is saturated, so this name is not
	// retained. Counted only in unknownNamesDropped.
	unknownTableFull
)

var (
	// unknownMu guards insertion into unknownCounts only. Once a pair has a
	// slot, further sightings go through its atomic without taking the write
	// lock — which matters because the thing these counters exist to observe is
	// a BURST, and a burst is by definition the same name arriving repeatedly.
	// Serializing exactly then would blunt what is being measured, the same
	// argument confine.go makes for its fixed array of atomics.
	unknownMu     sync.RWMutex
	unknownCounts = make(map[UnknownEvent]*atomic.Uint64)

	// unknownNamesDropped counts sightings refused a slot because the table was
	// full. Surfaced beside the table so a saturated table is never mistaken for
	// a complete one.
	unknownNamesDropped atomic.Uint64

	// unknownSaturationOnce makes the table-full report a single log line rather
	// than one per event, for the same reason first-sighting logging exists.
	unknownSaturationOnce sync.Once
)

// IgnoreUnknownEvent is the uniform handling every hook receiver owes an event
// name it does not recognize. It is the ONE place that behaviour is decided —
// the sibling of RejectPath, and for the same reason: two receivers had already
// written the identical default branch by hand, and the third would have had to
// remember to.
//
// It counts the sighting, logs the first occurrence of each distinct name, and
// returns. It writes NO response: the caller has already committed to answering
// 2xx on this path, and that rule is asserted by
// contracttesting.AssertUnknownHookEventObserved rather than left to each
// receiver.
//
// component is the caller's Logger component tag; adapter is the adapter name
// the count is keyed by; sessionID is the resolved session the event arrived
// for, purely so the log line joins up with the rest of that session's trail.
func IgnoreUnknownEvent(log outbound.Logger, component, adapter, sessionID, event string) {
	switch observeUnknownEvent(adapter, event) {
	case unknownFirstSighting:
		log.LogError(component, sessionID, fmt.Sprintf(
			"unrecognized hook event %q from %s: ignored, answered 2xx. Further occurrences of this name are counted, not logged — an upstream event rename looks exactly like this, so check the count in the diagnostics bundle's hooks.json before dismissing it.",
			truncateEventName(event), adapter))
	case unknownTableFull:
		unknownSaturationOnce.Do(func() {
			log.LogError(component, sessionID, fmt.Sprintf(
				"unrecognized hook event %q from %s: ignored, and NOT counted — the distinct-name table is full at %d entries. Later names are totalled in unknown_event_names_dropped only.",
				truncateEventName(event), adapter, MaxUnknownEventNames))
		})
	case unknownRepeat:
		// Counted. Deliberately silent: this is the flooding case.
	}
}

// observeUnknownEvent records one sighting and reports what kind it was.
//
// The double-checked shape is not premature optimization: the fast path is the
// repeat, and the write lock is taken only on the transition that also produces
// the log line — so "first sighting" is decided by exactly one goroutine even
// when two arrive together, which is what makes "logged once" true rather than
// approximately true.
func observeUnknownEvent(adapter, event string) unknownOutcome {
	key := UnknownEvent{Adapter: adapter, Event: truncateEventName(event)}

	unknownMu.RLock()
	c := unknownCounts[key]
	unknownMu.RUnlock()
	if c != nil {
		c.Add(1)
		return unknownRepeat
	}

	unknownMu.Lock()
	defer unknownMu.Unlock()
	if c := unknownCounts[key]; c != nil {
		// Lost the race to insert. The winner logs; this one only counts.
		c.Add(1)
		return unknownRepeat
	}
	if len(unknownCounts) >= MaxUnknownEventNames {
		unknownNamesDropped.Add(1)
		return unknownTableFull
	}
	counter := &atomic.Uint64{}
	counter.Add(1)
	unknownCounts[key] = counter
	return unknownFirstSighting
}

// UnknownEvents returns a snapshot of the per-(adapter, name) sighting counts,
// sorted by adapter then name so a bundle diffs cleanly between two captures.
//
// No entry is ever zero — a pair only gets a slot when it is first counted — so
// unlike Fallbacks and Rejections there is nothing to omit; the property those
// promise ("zeros are absent") holds here by construction.
func UnknownEvents() []UnknownEventCount {
	unknownMu.RLock()
	out := make([]UnknownEventCount, 0, len(unknownCounts))
	for key, counter := range unknownCounts {
		out = append(out, UnknownEventCount{UnknownEvent: key, Count: counter.Load()})
	}
	unknownMu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Adapter != out[j].Adapter {
			return out[i].Adapter < out[j].Adapter
		}
		return out[i].Event < out[j].Event
	})
	return out
}

// UnknownEventCount is one row of the UnknownEvents snapshot.
type UnknownEventCount struct {
	UnknownEvent
	Count uint64
}

// UnknownEventTotal is the total number of unrecognized events seen, across
// every name — including the ones the table had no room to name.
func UnknownEventTotal() uint64 {
	total := UnknownEventNamesDropped()
	unknownMu.RLock()
	defer unknownMu.RUnlock()
	for _, counter := range unknownCounts {
		total += counter.Load()
	}
	return total
}

// UnknownEventNamesDropped is how many sightings arrived after the distinct-name
// table saturated. Non-zero means UnknownEvents is incomplete, which is exactly
// the thing a reader must not have to infer.
func UnknownEventNamesDropped() uint64 { return unknownNamesDropped.Load() }

// resetUnknownEvents clears the table. Test-only, and unexported so it cannot
// become a production reset: one test in this package deliberately saturates the
// table, and a saturated table makes every test after it fail for a reason that
// has nothing to do with that test. Nothing in the daemon resets these — a
// counter an operator can zero is a counter that can hide the thing it was added
// to reveal.
func resetUnknownEvents() {
	unknownMu.Lock()
	defer unknownMu.Unlock()
	unknownCounts = make(map[UnknownEvent]*atomic.Uint64)
	unknownNamesDropped.Store(0)
	unknownSaturationOnce = sync.Once{}
}

// truncateEventName bounds a retained name at a rune boundary. Cutting mid-rune
// would store invalid UTF-8, which then has to survive JSON encoding into a
// diagnostics bundle.
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
