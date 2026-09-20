package session

// ClientCopy returns a session copy for a client response. Only working
// sessions expose task-progress metrics. The source session remains unchanged
// so a subsequent working response can use its current estimate.
func (s *SessionState) ClientCopy() *SessionState {
	if s == nil {
		return nil
	}
	clientState := *s
	if clientState.State != StateWorking && clientState.Metrics != nil {
		metrics := *clientState.Metrics
		metrics.TaskEstimate = nil
		metrics.TaskCompletionEta = nil
		clientState.Metrics = &metrics
	}
	return &clientState
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
