package processlifecycle

import (
	"testing"

	"irrlicht/core/domain/agent"
)

func TestPIDFilterRetiresAndRestoresNativeRow(t *testing.T) {
	const pid = 1988
	prev := osProc
	osProc = fakeObserver{pids: []int{pid}, cwd: map[int]string{pid: "/work"}, argv: map[int][]string{pid: {"codex", "app-server", "--stdio"}}}
	t.Cleanup(func() { osProc = prev })

	owned := false
	s := NewScanner("codex", "codex", 0).WithPIDFilter(func(candidate int) bool { return candidate == pid && owned })
	ch := s.Subscribe()
	s.poll()
	assertScannerEvent(t, ch, agent.EventNewSession)
	owned = true
	s.poll()
	assertScannerEvent(t, ch, agent.EventRemoved)
	s.poll()
	assertNoScannerEvent(t, ch)
	owned = false // DSH consent was revoked, so the native row must return.
	s.poll()
	assertScannerEvent(t, ch, agent.EventNewSession)
}

func TestPIDFilterRetriesRemovalAfterSubscriberAppears(t *testing.T) {
	const pid = 1989
	prev := osProc
	osProc = fakeObserver{pids: []int{pid}, cwd: map[int]string{pid: "/work"}}
	t.Cleanup(func() { osProc = prev })
	s := NewScanner("codex", "codex", 0).WithPIDFilter(func(int) bool { return true })
	s.poll()
	ch := s.Subscribe()
	s.poll()
	assertScannerEvent(t, ch, agent.EventRemoved)
	s.poll()
	assertNoScannerEvent(t, ch)
}

func TestPIDFilterRetriesDroppedRemoval(t *testing.T) {
	const pid = 1990
	prev := osProc
	osProc = fakeObserver{pids: []int{pid}, cwd: map[int]string{pid: "/work"}}
	t.Cleanup(func() { osProc = prev })
	s := NewScanner("codex", "codex", 0).WithPIDFilter(func(int) bool { return true })
	ch := s.Subscribe()
	for i := 0; i < cap(ch); i++ {
		s.broadcast(agent.Event{Type: agent.EventActivity})
	}
	s.poll() // Removal cannot enter the full channel.
	for i := 0; i < cap(ch); i++ {
		<-ch
	}
	s.poll() // The scanner must retry after capacity returns.
	assertScannerEvent(t, ch, agent.EventRemoved)
}

func assertScannerEvent(t *testing.T, ch <-chan agent.Event, want agent.EventType) {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Type != want {
			t.Fatalf("event = %v, want %v", ev.Type, want)
		}
	default:
		t.Fatalf("missing %v event", want)
	}
}

func assertNoScannerEvent(t *testing.T, ch <-chan agent.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event: %+v", ev)
	default:
	}
}
