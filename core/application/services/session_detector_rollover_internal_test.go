package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// transcriptRolledOver's contract (#2080): follow a different transcript only
// when it exists and is strictly newer than the current one, or the current
// one is gone. The "older" row is what keeps a late event from a codex
// thread's old segment from moving its session back.
func TestTranscriptRolledOver(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Minute)
	stamp := func(name string, at time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}
	current := stamp("rollout-current.jsonl", base)
	newer := stamp("rollout-newer.jsonl", base.Add(time.Second))
	older := stamp("rollout-older.jsonl", base.Add(-time.Second))
	same := stamp("rollout-same.jsonl", base)
	missing := filepath.Join(dir, "rollout-missing.jsonl")

	for _, tc := range []struct {
		name               string
		current, candidate string
		want               bool
	}{
		{"newer candidate", current, newer, true},
		{"older candidate", current, older, false},
		{"equal mtime", current, same, false},
		{"same path", current, current, false},
		{"current gone", missing, newer, true},
		{"candidate gone", current, missing, false},
		{"no current path", "", newer, false},
		{"no candidate path", current, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := transcriptRolledOver(tc.current, tc.candidate); got != tc.want {
				t.Fatalf("transcriptRolledOver(%s, %s) = %v, want %v",
					filepath.Base(tc.current), filepath.Base(tc.candidate), got, tc.want)
			}
		})
	}
}
