package opencode

import (
	"testing"
)

func TestParseTranscriptPath_RawDBPath(t *testing.T) {
	dbPath, sid := parseTranscriptPath("/home/user/.local/share/opencode/opencode.db", "ses_abc")
	if dbPath != "/home/user/.local/share/opencode/opencode.db" {
		t.Errorf("dbPath = %q", dbPath)
	}
	if sid != "ses_abc" {
		t.Errorf("sid = %q, want ses_abc", sid)
	}
}

func TestParseTranscriptPath_WALSuffix(t *testing.T) {
	dbPath, sid := parseTranscriptPath("/home/.local/share/opencode/opencode.db-wal", "ses_xyz")
	if dbPath != "/home/.local/share/opencode/opencode.db" {
		t.Errorf("dbPath = %q, want .../opencode.db", dbPath)
	}
	if sid != "ses_xyz" {
		t.Errorf("sid = %q, want ses_xyz", sid)
	}
}

func TestParseTranscriptPath_WALWithSession(t *testing.T) {
	dbPath, sid := parseTranscriptPath("/home/.local/share/opencode/opencode.db-wal?session=ses_123", "")
	if dbPath != "/home/.local/share/opencode/opencode.db" {
		t.Errorf("dbPath = %q, want .../opencode.db", dbPath)
	}
	if sid != "ses_123" {
		t.Errorf("sid = %q, want ses_123", sid)
	}
}

func TestParseTranscriptPath_DBWithSession(t *testing.T) {
	dbPath, sid := parseTranscriptPath("/home/.local/share/opencode/opencode.db?session=ses_456", "")
	if dbPath != "/home/.local/share/opencode/opencode.db" {
		t.Errorf("dbPath = %q", dbPath)
	}
	if sid != "ses_456" {
		t.Errorf("sid = %q, want ses_456", sid)
	}
}

func TestParseTranscriptPath_EmptyPath(t *testing.T) {
	dbPath, sid := parseTranscriptPath("", "")
	if dbPath != "" {
		t.Errorf("dbPath = %q, want empty", dbPath)
	}
	if sid != "" {
		t.Errorf("sid = %q, want empty", sid)
	}
}

func TestParseTranscriptPath_EmptyPathWithSession(t *testing.T) {
	dbPath, sid := parseTranscriptPath("", "fallback-id")
	if dbPath != "" {
		t.Errorf("dbPath = %q, want empty", dbPath)
	}
	if sid != "fallback-id" {
		t.Errorf("sid = %q, want fallback-id", sid)
	}
}

func TestParseTranscriptPath_DBPathWithoutWAL_WithSession(t *testing.T) {
	dbPath, sid := parseTranscriptPath("/tmp/test.db?session=hello", "")
	if dbPath != "/tmp/test.db" {
		t.Errorf("dbPath = %q, want /tmp/test.db", dbPath)
	}
	if sid != "hello" {
		t.Errorf("sid = %q, want hello", sid)
	}
}

func TestParseTranscriptPath_NoSessionQuery(t *testing.T) {
	dbPath, sid := parseTranscriptPath("/tmp/test.db-wal", "provided-id")
	if dbPath != "/tmp/test.db" {
		t.Errorf("dbPath = %q, want /tmp/test.db", dbPath)
	}
	if sid != "provided-id" {
		t.Errorf("sid = %q, want provided-id", sid)
	}
}
