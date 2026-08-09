package hookjson

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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

// resetUnknownEvents clears the table. It lives in the test file, not beside
// the counters, so the production binary carries no way to zero them: a counter
// an operator can reset is a counter that can hide the thing it was added to
// reveal. Tests need it because one of them deliberately saturates the shared
// table, which would otherwise make every test after it fail for a reason that
// has nothing to do with that test.
func resetUnknownEvents() {
	unknownMu.Lock()
	defer unknownMu.Unlock()
	unknownCounts = make(map[UnknownEvent]*atomic.Uint64)
	unknownDropped = make(map[string]*atomic.Uint64)
}

// droppedTotal sums the per-adapter drop counters.
func droppedTotal() uint64 {
	var total uint64
	for _, n := range UnknownEventNamesDropped() {
		total += n
	}
	return total
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
	before, beforeDropped := UnknownEventTotal(), droppedTotal()

	IgnoreUnknownEvent(log, "comp", "adapter-total", "sess", "TotalledEvent")
	IgnoreUnknownEvent(log, "comp", "adapter-total", "sess", "TotalledEvent")

	if got := UnknownEventTotal() - before; got != 2 {
		t.Errorf("total delta = %d, want 2", got)
	}
	if got := droppedTotal() - beforeDropped; got != 0 {
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
	// 3-byte runes, chosen so byte index maxUnknownEventNameLen (64) is a
	// CONTINUATION byte: 64 % 3 == 1. A 2-byte rune would leave index 64 on a
	// rune start, and a naive `name[:64] + "…"` would satisfy every assertion
	// below — the fixture has to be able to fail before it is evidence.
	long := strings.Repeat("日", maxUnknownEventNameLen)

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

// TestUnknownEventTableSaturates pins the bound and, more importantly, what
// survives it. The table is process-global and shared by every adapter, so a
// local process can fill all of it — the endpoint is unauthenticated. What must
// NOT happen is that a genuine rename arriving at some other adapter afterwards
// becomes invisible: the name is unrecoverable, but the adapter and the volume
// are not, and saturation is announced once per adapter rather than once per
// process.
func TestUnknownEventTableSaturates(t *testing.T) {
	resetUnknownEvents()
	log := &countingLogger{}

	// Fill the table with junk aimed at one adapter.
	for i := 0; i < maxUnknownEventNames+8; i++ {
		IgnoreUnknownEvent(log, "comp", "adapter-junk", "sess", fmt.Sprintf("Sat%03d", i))
	}
	if n := len(UnknownEvents()); n > maxUnknownEventNames {
		t.Errorf("retained %d distinct names, want at most %d", n, maxUnknownEventNames)
	}
	if droppedTotal() == 0 {
		t.Fatalf("posting %d distinct names past a cap of %d dropped none — the table is unbounded",
			maxUnknownEventNames+8, maxUnknownEventNames)
	}

	// Now a real rename at a DIFFERENT adapter, arriving on every tool call.
	for i := 0; i < 500; i++ {
		IgnoreUnknownEvent(log, "comp", "adapter-victim", "sess", "RenamedAssert")
	}

	dropped := UnknownEventNamesDropped()
	if dropped["adapter-victim"] != 500 {
		t.Errorf("victim adapter dropped %d, want 500 — a table filled by one adapter must not make another adapter's flood unattributable",
			dropped["adapter-victim"])
	}
	if dropped["adapter-junk"] == 0 {
		t.Error("junk adapter's drops were not attributed to it")
	}
	if n := log.mentioning("distinct-name table is full"); n != 2 {
		t.Errorf("saturation was announced %d time(s), want exactly 2 — once per adapter, so a second adapter's rename is not silenced by the first adapter's junk, and not once per event either", n)
	}
	// The total still accounts for every event that arrived, named or not.
	if total := UnknownEventTotal(); total < 500+uint64(maxUnknownEventNames) {
		t.Errorf("total = %d, want at least %d — dropped sightings must still be totalled", total, 500+maxUnknownEventNames)
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
