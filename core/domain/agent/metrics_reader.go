package agent

import "irrlicht/core/domain/session"

// MetricsReader bypasses the JSONL-tailer path entirely. Adapters whose
// session data lives in a structured store (OpenCode SQLite; in Phase C
// goose v1.10+, crush, cursor, zed) implement this and the runtime calls
// it instead of running a JSONLineParser.
//
// storePath is whatever ProcessOwnedStore.PathForPID returned for the
// session's PID; sessionID is the session UUID within the store.
// Returns (nil, nil) when the session has no data yet.
type MetricsReader interface {
	ComputeMetrics(storePath, sessionID string) (*session.SessionMetrics, error)
}
