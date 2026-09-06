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
)

func (runtime *LiveRuntime) WaitIrrlichtState(
	ctx context.Context,
	owned OwnedSession,
	state string,
) (SessionObservation, error) {
	var observation SessionObservation
	err := poll(ctx, "Irrlicht session state "+state, func() (bool, error) {
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
		stateSeen, err := runtime.stateObserved(owned.Transcript.SessionID, candidate.State, state)
		if err != nil {
			return false, err
		}
		if !stateSeen {
			return false, nil
		}
		observation = candidate
		return true, nil
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
func (runtime *LiveRuntime) stateObserved(sessionID, currentState, wantedState string) (bool, error) {
	expected := []string{"working"}
	if wantedState == "ready" {
		expected = append(expected, "ready")
	}
	recorded, err := recordingHasStateSequence(runtime.options.RecordingDirectory, sessionID, expected)
	if wantedState == "ready" {
		return currentState == "ready" && recorded, err
	}
	return recorded, err
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
