package desktopdriver

// Irrlicht daemon observation: what the daemon reports about the owned
// session, and the recording it writes.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"irrlicht/core/domain/session"
)

func (runtime *LiveRuntime) WaitIrrlichtState(
	ctx context.Context,
	owned OwnedSession,
	state string,
) (SessionObservation, error) {
	return runtime.waitIrrlichtStates(ctx, owned, []string{state})
}

func (runtime *LiveRuntime) WaitIrrlichtTurnEnd(
	ctx context.Context,
	owned OwnedSession,
) (SessionObservation, error) {
	return runtime.waitIrrlichtStates(ctx, owned, []string{session.StateReady, session.StateWaiting})
}

func (runtime *LiveRuntime) waitIrrlichtStates(
	ctx context.Context,
	owned OwnedSession,
	states []string,
) (SessionObservation, error) {
	var observation SessionObservation
	err := poll(ctx, "Irrlicht session state "+strings.Join(states, " or "), func() (bool, error) {
		sessions, err := runtime.fetchIrrlichtSessions(ctx)
		if err != nil {
			return false, err
		}
		candidate, found, err := selectIrrlichtSession(sessions, owned.Transcript.SessionID)
		if err != nil || !found {
			return false, err
		}
		if !sameWorkspace(candidate.CWD, owned.Registry.CWD) {
			return false, fmt.Errorf("Irrlicht workspace mismatch: registry %q, Irrlicht %q", owned.Registry.CWD, candidate.CWD)
		}
		if candidate.Launcher.HostBundleID != desktopBundleID {
			return false, fmt.Errorf(
				"Irrlicht host bundle ID is %q, want %q",
				candidate.Launcher.HostBundleID,
				desktopBundleID,
			)
		}
		if err := validateOwnedProcessBaseline(runtime.processBaseline, candidate); err != nil {
			return false, err
		}
		command, err := runtime.observeProcess(ctx, candidate.PID)
		if err != nil {
			return false, err
		}
		process := ProcessEvidence{PID: candidate.PID, Command: command}
		if previous, ok := runtime.processEvidence[owned.Registry.SessionID]; ok && previous != process {
			// Name both halves. The comparison is over the whole evidence
			// struct, so a drifting command line with a stable PID is a real
			// mismatch — and reporting only the PIDs printed the same number
			// twice and sent the operator looking at the wrong field.
			return false, fmt.Errorf(
				"owned Claude process identity changed from PID %d (%s) to PID %d (%s)",
				previous.PID, previous.Command, process.PID, process.Command)
		}
		runtime.processes[owned.Registry.SessionID] = candidate.PID
		runtime.processEvidence[owned.Registry.SessionID] = process
		for _, state := range states {
			stateSeen, err := runtime.stateObserved(owned.Transcript.SessionID, candidate.State, state)
			if err != nil {
				return false, err
			}
			if stateSeen {
				observation = candidate
				return true, nil
			}
		}
		return false, nil
	})
	return observation, err
}

// stateObserved reads the run's own recording rather than the live state.
//
// A Desktop session is created BY its first turn, so its sequence starts at
// working — there is no pre-turn ready, because the registry row and the Claude
// Code session do not exist until the prompt is sent. Demanding a leading ready
// made every Desktop turn unobservable.
//
// The recording is also the only race-free source. Live run 17 went from
// working to ready in 2.7 seconds; a poll for the CURRENT state has no
// guarantee of landing inside a window that short, while the transition it is
// looking for is durably in the recording.
//
// The expectation it hands recordingHasStateSequence is CUMULATIVE across
// turns, not just this turn's own working/ready pair. recordingHasStateSequence
// rescans the whole recording from the start on every call and keeps no
// cursor between calls, so a bare ["working"] expectation would stale-match
// the FIRST turn's own "working" transition on every LATER turn's wait too —
// a multi-turn recipe's second `waitState("working")` would return
// immediately, before Desktop had even started the second turn. Prefixing the
// expectation with one full working->ready cycle per PRIOR turn (runtime.turn,
// incremented by Submit) forces the match to land strictly further into the
// recording each turn, so it can only be satisfied by that turn's own
// transitions.
func (runtime *LiveRuntime) stateObserved(sessionID, currentState, wantedState string) (bool, error) {
	expected := cumulativeExpectedStates(runtime.turn, wantedState)
	recorded, err := recordingHasStateSequence(runtime.options.RecordingDirectory, sessionID, expected)
	if err != nil {
		return false, err
	}
	if isCompletedTurnState(wantedState) {
		return runtime.completedTurnStateObserved(sessionID, currentState, wantedState, recorded)
	}
	return runtime.workingStateObserved(currentState, recorded), nil
}

func isCompletedTurnState(state string) bool {
	return state == session.StateReady || state == session.StateWaiting
}

func (runtime *LiveRuntime) completedTurnStateObserved(
	sessionID string,
	currentState string,
	wantedState string,
	recorded bool,
) (bool, error) {
	if currentState != wantedState {
		return false, nil
	}
	if recorded {
		return true, nil
	}
	// Waiting must be durable before the driver continues. Unlike ready, it has
	// no safe first-turn fallback. A live waiting state alone does not prove that
	// this run recorded the working -> waiting transition.
	if wantedState == session.StateWaiting {
		return false, nil
	}
	// The recorded `ready` is the LAST event a turn writes and the one most
	// likely still unflushed. On the FIRST turn, the recorded `working` plus a
	// live idle state prove that the turn ran and ended. Later turns cannot use
	// this fallback because their `working` event appears when they start.
	if runtime.turn > 1 {
		return false, nil
	}
	return recordingHasStateSequence(
		runtime.options.RecordingDirectory,
		sessionID,
		cumulativeExpectedStates(runtime.turn, session.StateWorking),
	)
}

func (runtime *LiveRuntime) workingStateObserved(currentState string, recorded bool) bool {
	// The recording is written by the daemon and can lag its own HTTP API. Cell
	// 1-1 timed out after 1m30s waiting for a `working` that the recording, read
	// moments later, already held. On the FIRST turn the live state settles it:
	// if the daemon says this session is working, it is.
	//
	// A later turn cannot use that shortcut. A live "working" does not say which
	// turn it belongs to, and the whole point of the cumulative sequence is that
	// turn two must not be satisfied by turn one's transition.
	if runtime.turn <= 1 && (currentState == session.StateWorking || currentState == session.StateReady) {
		// "ready" counts here too, and deliberately. A short turn reaches ready
		// before any poll can catch it working, and the recording that proves it
		// worked has not been flushed yet — so neither source can show working,
		// for a turn that ran perfectly. Cell 2-13 failed exactly that way.
		//
		// Nothing is given up by moving on. waitForCompletion still demands the
		// recorded sequence working→ready for this session, by which time the
		// daemon has flushed it. That is the evidence; this wait is only a gate
		// on the way to it.
		return true
	}
	return recorded
}

// cumulativeExpectedStates returns the full state sequence a recording must
// carry to prove `wantedState` has been reached on the CURRENT turn. Turn
// numbers below 2 need no prefix — there is no prior turn to guard against —
// so a caller that never increments `turn` (every existing single-turn path)
// sees exactly the sequence it saw before this existed.
func cumulativeExpectedStates(turn int, wantedState string) []string {
	expected := make([]string, 0, 2*turn)
	for i := 1; i < turn; i++ {
		expected = append(expected, session.StateWorking, session.StateReady)
	}
	expected = append(expected, session.StateWorking)
	if wantedState == session.StateReady || wantedState == session.StateWaiting {
		expected = append(expected, wantedState)
	}
	return expected
}

func selectIrrlichtSession(sessions []SessionObservation, sessionID string) (SessionObservation, bool, error) {
	var matches []SessionObservation
	for _, session := range sessions {
		if session.SessionID == sessionID {
			matches = append(matches, session)
		}
	}
	if len(matches) > 1 {
		return SessionObservation{}, false, fmt.Errorf("Irrlicht session %q is ambiguous: found %d rows", sessionID, len(matches))
	}
	if len(matches) == 0 {
		return SessionObservation{}, false, nil
	}
	return matches[0], true, nil
}

func (runtime *LiveRuntime) WaitHook(ctx context.Context, owned OwnedSession) error {
	return poll(ctx, "hook_received event", func() (bool, error) {
		files, err := filepath.Glob(filepath.Join(runtime.options.RecordingDirectory, "*.jsonl"))
		if err != nil {
			return false, err
		}
		for _, file := range files {
			found, err := jsonlContains(file, func(value map[string]any) bool {
				return value["kind"] == "hook_received" && value["session_id"] == owned.Transcript.SessionID
			})
			if err != nil {
				return false, err
			}
			if found {
				return true, nil
			}
		}
		return false, nil
	})
}

func (runtime *LiveRuntime) WaitIrrlichtRemoved(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	return poll(ctx, "Irrlicht session removal", func() (bool, error) {
		sessions, err := runtime.fetchIrrlichtSessions(ctx)
		if err != nil {
			return false, err
		}
		for _, session := range sessions {
			if session.SessionID == sessionID {
				return false, nil
			}
		}
		return true, nil
	})
}

func (runtime *LiveRuntime) fetchIrrlichtSessions(ctx context.Context) ([]SessionObservation, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+runtime.options.DaemonAddress+"/api/v1/sessions", nil)
	if err != nil {
		return nil, err
	}
	response, err := runtime.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Irrlicht sessions endpoint returned %s", response.Status)
	}
	var root any
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16*1024*1024))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	var sessions []SessionObservation
	if err := collectSessionObjects(root, &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

func collectSessionObjects(value any, sessions *[]SessionObservation) error {
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			if err := collectSessionObjects(child, sessions); err != nil {
				return err
			}
		}
	case map[string]any:
		if id, ok := typed["session_id"].(string); ok && id != "" {
			data, err := json.Marshal(typed)
			if err != nil {
				return fmt.Errorf("encode Irrlicht session %q: %w", id, err)
			}
			var session SessionObservation
			if err := json.Unmarshal(data, &session); err != nil {
				return fmt.Errorf("decode Irrlicht session %q: %w", id, err)
			}
			session.Raw = typed
			*sessions = append(*sessions, session)
		}
		for _, child := range typed {
			if err := collectSessionObjects(child, sessions); err != nil {
				return err
			}
		}
	}
	return nil
}
