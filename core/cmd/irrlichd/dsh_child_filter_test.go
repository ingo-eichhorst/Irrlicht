package main

import (
	"testing"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/session"
)

func TestVisibleDSHParentRequiresConsentAndExactLiveTranscript(t *testing.T) {
	repo := filesystem.NewWithDir(t.TempDir())
	for _, state := range []*session.SessionState{
		{SessionID: "dsh", Adapter: "dsh", PID: 10, State: session.StateWorking, TranscriptPath: "/tmp/dsh.zstd"},
		{SessionID: "native", Adapter: "codex", PID: 11, State: session.StateWorking, TranscriptPath: "/tmp/native.jsonl"},
		{SessionID: "old", Adapter: "dsh", PID: 12, State: session.StateReady, TranscriptPath: "/tmp/old.zstd"},
		{SessionID: "proc-13", Adapter: "dsh", PID: 13, State: session.StateWorking},
	} {
		if err := repo.Save(state); err != nil {
			t.Fatal(err)
		}
	}
	granted := func() bool { return true }
	denied := func() bool { return false }
	for _, tc := range []struct {
		name    string
		pid     int
		consent func() bool
		want    bool
	}{
		{"owned parent", 10, granted, true},
		{"revoked DSH", 10, denied, false},
		{"other adapter", 11, granted, false},
		{"old DSH row", 12, granted, false},
		{"no transcript", 13, granted, false},
		{"wrong pid", 14, granted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := visibleDSHParent(repo, tc.consent, tc.pid); got != tc.want {
				t.Fatalf("visible parent = %t, want %t", got, tc.want)
			}
		})
	}
}
