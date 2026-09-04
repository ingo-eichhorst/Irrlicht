package desktopdriver

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RegistrySession is the identity record written by Claude Desktop for one
// local Claude Code session.
type RegistrySession struct {
	SessionID    string  `json:"sessionId"`
	CLISessionID string  `json:"cliSessionId"`
	CWD          string  `json:"cwd"`
	EnvScopeID   *string `json:"envScopeId"`
	// EnvScopePresent distinguishes the omitted Local-project field from an
	// explicit JSON null when preserving the allowlisted identity fields.
	EnvScopePresent bool           `json:"-"`
	Archived        bool           `json:"isArchived"`
	Title           string         `json:"title"`
	Path            string         `json:"-"`
	Raw             map[string]any `json:"-"`
}

// TranscriptIdentity is the identity carried by the raw Claude Code JSONL.
type TranscriptIdentity struct {
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	Entrypoint string `json:"entrypoint"`
}

// OwnedSession joins the one post-baseline Desktop registry entry to its raw
// Claude Code transcript.
type OwnedSession struct {
	Registry   RegistrySession
	Transcript TranscriptIdentity
}

// SelectOwnedSession selects one session that did not exist in the baseline.
// A new session is not owned until its Desktop and transcript identities both
// name the requested workspace. The caller must refuse concurrent session
// creation because it cannot determine which actor created each new session.
func SelectOwnedSession(
	baseline map[string]struct{},
	current []RegistrySession,
	transcripts map[string]TranscriptIdentity,
	requestedWorkspace string,
) (OwnedSession, error) {
	provisional, err := SelectProvisionalSession(baseline, current, requestedWorkspace)
	if err != nil {
		return OwnedSession{}, err
	}
	registry := provisional.Registry
	if registry.CLISessionID == "" {
		return OwnedSession{}, fmt.Errorf("desktop session %q has no Claude Code transcript ID", registry.SessionID)
	}
	transcript, ok := transcripts[registry.CLISessionID]
	if !ok {
		return OwnedSession{}, fmt.Errorf("desktop transcript %q is missing", registry.CLISessionID)
	}
	if transcript.SessionID != registry.CLISessionID {
		return OwnedSession{}, fmt.Errorf(
			"desktop transcript identity mismatch: registry maps to %q, transcript reports %q",
			registry.CLISessionID,
			transcript.SessionID,
		)
	}
	if !sameWorkspace(transcript.CWD, requestedWorkspace) {
		return OwnedSession{}, fmt.Errorf(
			"desktop transcript workspace mismatch: requested %q, observed %q",
			requestedWorkspace,
			transcript.CWD,
		)
	}
	if transcript.Entrypoint != "claude-desktop" {
		return OwnedSession{}, fmt.Errorf(
			"desktop transcript %q has entrypoint %q, want %q",
			registry.CLISessionID,
			transcript.Entrypoint,
			"claude-desktop",
		)
	}
	return OwnedSession{Registry: registry, Transcript: transcript}, nil
}

// SelectProvisionalSession identifies the minimum safe cleanup identity. A
// transcript can appear after the registry entry, so cleanup may use this
// narrower proof. Submission and evidence still require SelectOwnedSession.
func SelectProvisionalSession(
	baseline map[string]struct{},
	current []RegistrySession,
	requestedWorkspace string,
) (OwnedSession, error) {
	var postBaseline []RegistrySession
	var candidates []RegistrySession
	for _, session := range current {
		if _, existed := baseline[session.SessionID]; existed {
			continue
		}
		postBaseline = append(postBaseline, session)
		if strings.HasPrefix(session.SessionID, "local_") &&
			session.EnvScopeID == nil &&
			sameWorkspace(session.CWD, requestedWorkspace) {
			candidates = append(candidates, session)
		}
	}
	if len(postBaseline) > 1 {
		return OwnedSession{}, fmt.Errorf(
			"desktop provisional ownership refuses %d concurrent post-baseline sessions",
			len(postBaseline),
		)
	}
	if len(candidates) == 1 {
		return OwnedSession{Registry: candidates[0]}, nil
	}
	if len(postBaseline) == 1 {
		observed := postBaseline[0]
		if !strings.HasPrefix(observed.SessionID, "local_") {
			return OwnedSession{}, fmt.Errorf("desktop session %q is not a local session", observed.SessionID)
		}
		if observed.EnvScopeID != nil {
			return OwnedSession{}, fmt.Errorf("desktop session %q did not use the Local environment", observed.SessionID)
		}
		return OwnedSession{}, fmt.Errorf(
			"desktop registry workspace mismatch: requested %q, observed %q",
			requestedWorkspace,
			observed.CWD,
		)
	}
	return OwnedSession{}, fmt.Errorf(
		"desktop provisional ownership requires exactly one post-baseline Local session for workspace %q; found %d matches among %d new sessions",
		requestedWorkspace,
		len(candidates),
		len(postBaseline),
	)
}

func sameWorkspace(observed, requested string) bool {
	return filepath.IsAbs(observed) && filepath.IsAbs(requested) &&
		filepath.Clean(observed) == filepath.Clean(requested)
}
