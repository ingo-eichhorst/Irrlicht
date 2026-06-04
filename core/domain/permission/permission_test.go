package permission

import "testing"

func TestGetDefaultsToPending(t *testing.T) {
	s := Set{}
	if got := s.Get("claude-code", "hooks"); got != StatePending {
		t.Fatalf("Get on empty set = %q, want %q", got, StatePending)
	}
}

func TestPutThenGet(t *testing.T) {
	s := Set{}
	s.Put("claude-code", "hooks", StateGranted)
	s.Put("claude-code", "statusline", StateDenied)

	if got := s.Get("claude-code", "hooks"); got != StateGranted {
		t.Fatalf("hooks = %q, want granted", got)
	}
	if got := s.Get("claude-code", "statusline"); got != StateDenied {
		t.Fatalf("statusline = %q, want denied", got)
	}
	// A key never answered stays pending — the upgrade path for
	// permissions declared by a later daemon version.
	if got := s.Get("claude-code", "transcripts"); got != StatePending {
		t.Fatalf("transcripts = %q, want pending", got)
	}
	if got := s.Get("codex", "transcripts"); got != StatePending {
		t.Fatalf("codex/transcripts = %q, want pending", got)
	}
}
