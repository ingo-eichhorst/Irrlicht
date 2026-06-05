package services_test

import (
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/ports/outbound"
)

// TestPushService_BroadcastStampsMonotonicSeq: every subscriber sees the
// same, gap-free Seq per broadcast (#593).
func TestPushService_BroadcastStampsMonotonicSeq(t *testing.T) {
	p := services.NewPushService()
	a := p.Subscribe()
	b := p.Subscribe()

	for range 3 {
		p.Broadcast(outbound.PushMessage{Type: outbound.PushTypeUpdated})
	}

	for want := uint64(1); want <= 3; want++ {
		ma := <-a
		mb := <-b
		if ma.Seq != want || mb.Seq != want {
			t.Fatalf("broadcast %d: subscriber seqs = %d/%d, want both %d", want, ma.Seq, mb.Seq, want)
		}
	}
}

// TestPushService_SeqSurvivesSlowSubscriberDrop: a full subscriber is
// skipped, but numbering keeps advancing — the fast subscriber receives a
// contiguous sequence while the slow one sees a gap, which is exactly the
// signal a client needs to re-hydrate instead of keeping phantom state
// (#593).
func TestPushService_SeqSurvivesSlowSubscriberDrop(t *testing.T) {
	p := services.NewPushService()
	slow := p.Subscribe()
	// Fill the slow subscriber's 64-slot buffer.
	for range 64 {
		p.Broadcast(outbound.PushMessage{Type: outbound.PushTypeUpdated})
	}
	fast := p.Subscribe()
	// These two broadcasts drop for slow, deliver to fast.
	p.Broadcast(outbound.PushMessage{Type: outbound.PushTypeDeleted})
	p.Broadcast(outbound.PushMessage{Type: outbound.PushTypeDeleted})

	m1 := <-fast
	m2 := <-fast
	if m1.Seq != 65 || m2.Seq != 66 {
		t.Fatalf("fast subscriber seqs = %d,%d; want 65,66", m1.Seq, m2.Seq)
	}

	// The slow subscriber got 1..64 and then nothing — its last received
	// Seq (64) trails the stream, so the gap is client-observable.
	var last uint64
	for range 64 {
		last = (<-slow).Seq
	}
	if last != 64 {
		t.Fatalf("slow subscriber last seq = %d, want 64", last)
	}
	select {
	case m := <-slow:
		t.Fatalf("slow subscriber unexpectedly received seq %d", m.Seq)
	default:
	}
}
