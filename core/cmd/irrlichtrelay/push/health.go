package push

// DeliveryStatus is the outcome of the most recent push send to one device
// token — the §8.3 "delivery attempt outcome" surface. RAM only, on purpose
// (docs/mobile-notifications-arc42.md §8.6): after a relay restart the honest
// answer is "unknown since restart", which absence expresses and a persisted
// stale verdict would not.
type DeliveryStatus struct {
	At     int64  `json:"at"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// SetDeliveryStatus records the latest send outcome for a device token. The
// key deliberately outlives the subscription entry: a pruned-on-410 token
// keeps its last failure visible, which is exactly the state the health
// panel needs to explain.
func (s *Service) SetDeliveryStatus(tokenID string, st DeliveryStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.health[tokenID] = st
}

// DeliveryStatus returns the last recorded send outcome for a device token,
// ok=false when no send has been attempted since the relay started.
func (s *Service) DeliveryStatus(tokenID string) (DeliveryStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.health[tokenID]
	return st, ok
}
