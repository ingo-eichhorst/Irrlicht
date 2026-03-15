package services

import (
	"sync"

	"irrlicht/core/domain/session"
)

// PushService fans out session state changes to all subscribers.
// It implements ports/outbound.PushBroadcaster.
type PushService struct {
	mu   sync.Mutex
	subs []chan *session.SessionState
}

// NewPushService creates a new PushService.
func NewPushService() *PushService {
	return &PushService{}
}

// Subscribe returns a new buffered channel that will receive state updates.
func (p *PushService) Subscribe() chan *session.SessionState {
	ch := make(chan *session.SessionState, 16)
	p.mu.Lock()
	p.subs = append(p.subs, ch)
	p.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel and closes it.
func (p *PushService) Unsubscribe(ch chan *session.SessionState) {
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

// Broadcast sends the state to all subscribers. Slow subscribers are skipped.
func (p *PushService) Broadcast(state *session.SessionState) {
	p.mu.Lock()
	subs := make([]chan *session.SessionState, len(p.subs))
	copy(subs, p.subs)
	p.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- state:
		default:
			// skip slow subscriber
		}
	}
}
