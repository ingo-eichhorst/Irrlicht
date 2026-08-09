package hookjson

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// countingLogger records the lines IgnoreUnknownEvent emits.
type countingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *countingLogger) LogInfo(_, _, msg string)  { l.add(msg) }
func (l *countingLogger) LogError(_, _, msg string) { l.add(msg) }
func (l *countingLogger) LogProcessingTime(string, string, int64, int, string) {
}
func (l *countingLogger) Close() error { return nil }

func (l *countingLogger) add(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, msg)
}

func (l *countingLogger) mentioning(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	var n int
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

// countFor reads one (adapter, event) counter out of the snapshot.
func countFor(adapter, event string) uint64 {
	for _, row := range UnknownEvents() {
		if row.Adapter == adapter && row.Event == event {
			return row.Count
		}
	}
	return 0
}

// TestUnknownEventsAreKeyedPerAdapter pins the half of the key that is easiest
// to drop: the same event name arriving at two adapters must not merge into one
// row. A merged count attributes a rename to a CLI that never sent it.
func TestUnknownEventsAreKeyedPerAdapter(t *testing.T) {
	resetUnknownEvents()
	log := &countingLogger{}
	const event = "SharedNameKeyedPerAdapter"

	IgnoreUnknownEvent(log, "comp", "adapter-a", "sess", event)
	IgnoreUnknownEvent(log, "comp", "adapter-a", "sess", event)
	IgnoreUnknownEvent(log, "comp", "adapter-b", "sess", event)

	if got := countFor("adapter-a", event); got != 2 {
		t.Errorf("adapter-a count = %d, want 2", got)
	}
	if got := countFor("adapter-b", event); got != 1 {
		t.Errorf("adapter-b count = %d, want 1", got)
	}
	// One line per (adapter, name), not one per name: two adapters seeing the
	// same rename are two facts, and an operator has to see both.
	if n := log.mentioning(event); n != 2 {
		t.Errorf("logged %d line(s) for %q across two adapters, want 2", n, event)
	}
}

// TestUnknownEventTotalIncludesDropped is the arithmetic a reader relies on:
// the total is the number of unrecognized events that ARRIVED, not the number
// the name table had room for.
func TestUnknownEventTotalIncludesDropped(t *testing.T) {
	resetUnknownEvents()
	log := &countingLogger{}
	before, beforeDropped := UnknownEventTotal(), UnknownEventNamesDropped()

	IgnoreUnknownEvent(log, "comp", "adapter-total", "sess", "TotalledEvent")
	IgnoreUnknownEvent(log, "comp", "adapter-total", "sess", "TotalledEvent")

	if got := UnknownEventTotal() - before; got != 2 {
		t.Errorf("total delta = %d, want 2", got)
	}
	if got := UnknownEventNamesDropped() - beforeDropped; got != 0 {
		t.Errorf("dropped delta = %d, want 0 — the table was nowhere near full", got)
	}
}

// TestUnknownEventNameIsTruncatedAtRuneBoundary covers the retention bound. The
// endpoint is unauthenticated, so the name is caller-supplied and held for the
// life of the process; what must never happen is that the retained bytes are
// invalid UTF-8 and blow up JSON encoding of the diagnostics bundle.
func TestUnknownEventNameIsTruncatedAtRuneBoundary(t *testing.T) {
	resetUnknownEvents()
	log := &countingLogger{}
	// Multi-byte runes, so a naive byte cut lands mid-rune.
	long := strings.Repeat("ü", maxUnknownEventNameLen)

	IgnoreUnknownEvent(log, "comp", "adapter-trunc", "sess", long)

	var retained string
	for _, row := range UnknownEvents() {
		if row.Adapter == "adapter-trunc" {
			retained = row.Event
		}
	}
	if retained == "" {
		t.Fatal("no row retained for adapter-trunc")
	}
	if retained == long {
		t.Fatalf("name of %d bytes was retained whole; want it bounded", len(long))
	}
	if !utf8.ValidString(retained) {
		t.Errorf("retained name is not valid UTF-8: %q", retained)
	}
	if !strings.HasSuffix(retained, "…") {
		t.Errorf("retained name %q does not mark that it was truncated", retained)
	}
}

// TestUnknownEventTableSaturates pins the bound itself: past the cap, sightings
// are still totalled and the incompleteness is reported, rather than the table
// growing without limit on an unauthenticated endpoint.
func TestUnknownEventTableSaturates(t *testing.T) {
	resetUnknownEvents()
	log := &countingLogger{}
	beforeDropped := UnknownEventNamesDropped()

	// Overshoot the cap outright — other tests in this package share the table,
	// so filling it exactly is not something a single test can arrange.
	for i := 0; i < MaxUnknownEventNames+8; i++ {
		IgnoreUnknownEvent(log, "comp", "adapter-sat", "sess", fmt.Sprintf("Sat%03d", i))
	}

	if got := UnknownEventNamesDropped() - beforeDropped; got == 0 {
		t.Fatalf("posting %d distinct names past a cap of %d dropped none — the table is unbounded",
			MaxUnknownEventNames+8, MaxUnknownEventNames)
	}
	if n := len(UnknownEvents()); n > MaxUnknownEventNames {
		t.Errorf("retained %d distinct names, want at most %d", n, MaxUnknownEventNames)
	}
	if n := log.mentioning("distinct-name table is full"); n != 1 {
		t.Errorf("table saturation was reported %d time(s), want exactly 1 — the report must not itself become the flood", n)
	}
}

// TestUnknownEventFirstSightingIsLoggedOnceUnderConcurrency is the property the
// double-checked insert exists for: two goroutines racing on a name neither has
// seen must produce exactly one line, not two. Run with -race.
func TestUnknownEventFirstSightingIsLoggedOnceUnderConcurrency(t *testing.T) {
	resetUnknownEvents()
	log := &countingLogger{}
	const event = "ConcurrentFirstSighting"
	const goroutines = 32

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			IgnoreUnknownEvent(log, "comp", "adapter-race", "sess", event)
		}()
	}
	close(start)
	wg.Wait()

	if got := countFor("adapter-race", event); got != goroutines {
		t.Errorf("count = %d, want %d — a sighting was lost", got, goroutines)
	}
	if n := log.mentioning(event); n != 1 {
		t.Errorf("logged %d line(s), want exactly 1", n)
	}
}
