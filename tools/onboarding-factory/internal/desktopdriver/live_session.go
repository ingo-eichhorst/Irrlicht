package desktopdriver

// Session identity: which Desktop registry row, transcript and Claude Code
// session this run owns, and how the run recovers that binding.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (runtime *LiveRuntime) WaitOwnedSession(
	ctx context.Context,
	baseline Baseline,
	workspace string,
) (OwnedSession, error) {
	if !runtime.deepLinkOpened {
		return OwnedSession{}, nil
	}
	var owned OwnedSession
	err := poll(ctx, "registry and transcript identity", func() (bool, error) {
		sessions, _, err := runtime.readRegistry()
		if err != nil {
			return false, err
		}
		transcripts, err := runtime.readTranscriptIdentities(sessions)
		if err != nil {
			return false, err
		}
		owned, err = SelectOwnedSession(baseline.SessionIDs, sessions, transcripts, workspace)
		if err != nil {
			if identityMayStillAppear(err) {
				return false, nil
			}
			return false, err
		}
		runtime.registryByID[owned.Registry.SessionID] = owned.Registry
		return true, nil
	})
	return owned, err
}

func (runtime *LiveRuntime) RecoverOwnedSession(
	ctx context.Context,
	baseline Baseline,
	workspace string,
) (OwnedSession, error) {
	if !runtime.deepLinkOpened {
		return OwnedSession{}, nil
	}
	var owned OwnedSession
	err := poll(ctx, "provisional Desktop ownership", func() (bool, error) {
		sessions, _, err := runtime.readRegistry()
		if err != nil {
			return false, err
		}
		owned, err = SelectProvisionalSession(baseline.SessionIDs, sessions, workspace)
		if err != nil {
			if identityMayStillAppear(err) {
				return false, nil
			}
			return false, err
		}
		runtime.registryByID[owned.Registry.SessionID] = owned.Registry
		return true, nil
	})
	return owned, err
}

func identityMayStillAppear(err error) bool {
	message := err.Error()
	return strings.Contains(message, "found 0 matches") ||
		strings.Contains(message, "transcript ID") ||
		strings.Contains(message, "is missing")
}

func (runtime *LiveRuntime) registryRoot() string {
	return filepath.Join(runtime.options.DesktopSupportRoot, "claude-code-sessions")
}

func (runtime *LiveRuntime) readRegistry() ([]RegistrySession, map[string][]byte, error) {
	paths, err := filepath.Glob(filepath.Join(runtime.registryRoot(), "*", "*", "local_*.json"))
	if err != nil {
		return nil, nil, err
	}
	if len(paths) > 10_000 {
		return nil, nil, fmt.Errorf("Desktop registry exceeds 10000 session files")
	}
	sort.Strings(paths)
	sessions := make([]RegistrySession, 0, len(paths))
	files := make(map[string][]byte, len(paths))
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("Desktop registry entry is not a regular file: %q", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		var session RegistrySession
		if err := json.Unmarshal(data, &session); err != nil {
			return nil, nil, fmt.Errorf("decode Desktop registry entry %q: %w", path, err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, nil, fmt.Errorf("decode Desktop registry shape %q: %w", path, err)
		}
		_, session.EnvScopePresent = raw["envScopeId"]
		session.Raw = make(map[string]any, len(raw))
		if err := json.Unmarshal(data, &session.Raw); err != nil {
			return nil, nil, fmt.Errorf("decode Desktop registry raw fields %q: %w", path, err)
		}
		if session.SessionID == "" {
			return nil, nil, fmt.Errorf("Desktop registry entry %q has no sessionId", path)
		}
		session.Path = path
		sessions = append(sessions, session)
		files[path] = data
	}
	return sessions, files, nil
}

func (runtime *LiveRuntime) registrySession(sessionID string) (RegistrySession, error) {
	sessions, _, err := runtime.readRegistry()
	if err != nil {
		return RegistrySession{}, err
	}
	for _, session := range sessions {
		if session.SessionID == sessionID {
			return session, nil
		}
	}
	return RegistrySession{}, fmt.Errorf("Desktop registry session %q is missing", sessionID)
}

func (runtime *LiveRuntime) readTranscriptIdentities(sessions []RegistrySession) (map[string]TranscriptIdentity, error) {
	identities := map[string]TranscriptIdentity{}
	for _, session := range sessions {
		if session.CLISessionID == "" {
			continue
		}
		path, err := runtime.transcriptPath(session.CLISessionID)
		if err != nil {
			if errors.Is(err, errTranscriptPending) {
				continue
			}
			return nil, err
		}
		identity, err := readTranscriptIdentity(path)
		if err != nil {
			return nil, err
		}
		identities[session.CLISessionID] = identity
	}
	return identities, nil
}

func (runtime *LiveRuntime) transcriptPath(sessionID string) (string, error) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\\`) {
		return "", fmt.Errorf("invalid transcript session ID %q", sessionID)
	}
	paths, err := filepath.Glob(filepath.Join(runtime.options.ClaudeProjectsRoot, "*", sessionID+".jsonl"))
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("%w for session %q", errTranscriptPending, sessionID)
	}
	if len(paths) != 1 {
		return "", fmt.Errorf("Claude transcript %q requires one regular file; found %d", sessionID, len(paths))
	}
	info, err := os.Lstat(paths[0])
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("Claude transcript is not a regular file: %q", paths[0])
	}
	return paths[0], nil
}

func readTranscriptIdentity(path string) (TranscriptIdentity, error) {
	file, err := os.Open(path)
	if err != nil {
		return TranscriptIdentity{}, err
	}
	defer file.Close()
	var identity TranscriptIdentity
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var candidate TranscriptIdentity
		if err := json.Unmarshal(scanner.Bytes(), &candidate); err != nil {
			return TranscriptIdentity{}, fmt.Errorf("decode transcript %q: %w", path, err)
		}
		if err := mergeTranscriptIdentity(&identity, candidate); err != nil {
			return TranscriptIdentity{}, fmt.Errorf("transcript %q: %w", path, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return TranscriptIdentity{}, err
	}
	if identity.SessionID == "" || identity.CWD == "" || identity.Entrypoint == "" {
		return TranscriptIdentity{}, fmt.Errorf("transcript %q lacks sessionId, cwd, or entrypoint", path)
	}
	return identity, nil
}

func mergeTranscriptIdentity(identity *TranscriptIdentity, candidate TranscriptIdentity) error {
	fields := []struct {
		name      string
		current   *string
		candidate string
	}{
		{"sessionId", &identity.SessionID, candidate.SessionID},
		{"cwd", &identity.CWD, candidate.CWD},
		{"entrypoint", &identity.Entrypoint, candidate.Entrypoint},
	}
	for _, field := range fields {
		if field.candidate == "" {
			continue
		}
		if *field.current != "" && *field.current != field.candidate {
			return fmt.Errorf("inconsistent %s values %q and %q", field.name, *field.current, field.candidate)
		}
		*field.current = field.candidate
	}
	return nil
}
