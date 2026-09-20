package session

// ClientCopy returns a session copy for a client response. Ready sessions do
// not expose task-progress metrics. The source session remains unchanged so a
// subsequent working response can use its current estimate.
func (s *SessionState) ClientCopy() *SessionState {
	if s == nil {
		return nil
	}
	copy := *s
	if copy.State == StateReady && copy.Metrics != nil {
		metrics := *copy.Metrics
		metrics.TaskEstimate = nil
		metrics.TaskCompletionEta = nil
		copy.Metrics = &metrics
	}
	return &copy
}

// ClientCopies returns client-response copies of states.
func ClientCopies(states []*SessionState) []*SessionState {
	if states == nil {
		return nil
	}
	copies := make([]*SessionState, len(states))
	for i, state := range states {
		copies[i] = state.ClientCopy()
	}
	return copies
}
