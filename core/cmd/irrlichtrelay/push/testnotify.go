package push

// Rate limiting for the "send a test notification" diagnostic
// (docs/mobile-notifications-arc42.md §8.3). The state lives here rather
// than in the HTTP layer for the same reason the pairing-code window does:
// this package owns push state and the injected clock, so a test can drive
// the window without sleeping.

import "time"

// TestNotificationInterval is the minimum gap between two test
// notifications for one device token.
//
// The route is authenticated and reaches only the caller's OWN registered
// address, so it is neither an amplifier nor a way to buzz somebody else's
// phone — the case for limiting it at all is narrower than the pairing
// window's, and worth stating rather than assuming. What it is is a button
// wired to a POST from the relay's IP to Apple or Google, offered to
// somebody in exactly the state that produces repeated taps: nothing
// arrived, so try again. A held button is then a burst the push service
// attributes to this relay, and the relay's standing with it is shared by
// every phone paired to it. Ten seconds is below what a human tapping "did
// it arrive?" would notice and far above what a loop needs to be harmless.
const TestNotificationInterval = 10 * time.Second

// ClaimTestNotification reserves tokenID's slot for one test notification,
// reporting how long is left when it is refused. The stamp is taken before
// the send rather than after it: a send that takes the sender's full 10 s
// client timeout is exactly when a waiting user taps again, and stamping
// afterwards would let those taps queue up behind it.
//
// The map holds only the current window — each call sweeps what has drained
// — so it is bounded by the devices that pressed the button in the last
// TestNotificationInterval, and needs no coupling to the token prune.
func (s *Service) ClaimTestNotification(tokenID string) (retryAfter time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, at := range s.testSends {
		if now.Sub(at) >= TestNotificationInterval {
			delete(s.testSends, id)
		}
	}
	if at, seen := s.testSends[tokenID]; seen {
		return TestNotificationInterval - now.Sub(at), false
	}
	s.testSends[tokenID] = now
	return 0, true
}
