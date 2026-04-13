package services

import (
	"sync"

	"irrlicht/core/ports/outbound"
)

// PushService fans out session state changes to all subscribers.
// It implements ports/outbound.PushBroadcaster.
type PushService struct {
	mu   sync.Mutex
	subs []chan outbound.PushMessage
}

// NewPushService creates a new PushService.
func NewPushService() *PushService {
	return &PushService{}
}

// Subscribe returns a new buffered channel that will receive state updates.
// Buffer capacity matches the fswatcher subscriber channel (64) to avoid
// silently dropping state transition broadcasts during bursts with concurrent
// sessions and subagent transcripts. See issue #143 for the fswatcher fix.
func (p *PushService) Subscribe() chan outbound.PushMessage {
	ch := make(chan outbound.PushMessage, 64)
	p.mu.Lock()
	p.subs = append(p.subs, ch)
	p.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel and closes it.
func (p *PushService) Unsubscribe(ch chan outbound.PushMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, sub := range p.subs {
		if sub == ch {
			p.subs = append(p.subs[:i], p.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

// Broadcast sends the message to all subscribers. Slow subscribers are skipped.
func (p *PushService) Broadcast(msg outbound.PushMessage) {
	p.mu.Lock()
	subs := make([]chan outbound.PushMessage, len(p.subs))
	copy(subs, p.subs)
	p.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
			// skip slow subscriber
		}
	}
}
