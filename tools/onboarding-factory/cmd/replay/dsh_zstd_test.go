package main

import (
	"strings"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/dsh"
	"irrlicht/core/domain/lifecycle"
)

func TestSidecarReplayerPreservesZstdScratchSuffix(t *testing.T) {
	watch := lifecycle.Event{Timestamp: time.UnixMilli(1000).UTC()}
	r, cleanup, err := newSidecarReplayer(
		"/tmp/transcript.jsonl.zstd",
		[]byte("compressed bytes are appended later"),
		reportSettings{Adapter: dsh.AdapterName},
		[]lifecycle.Event{watch},
		map[string]*childInfo{},
	)
	if err != nil {
		t.Fatalf("newSidecarReplayer: %v", err)
	}
	defer cleanup()
	if !strings.HasSuffix(r.tmp.Name(), ".jsonl.zstd") {
		t.Fatalf("scratch path = %q; zstd input must keep a .zstd suffix", r.tmp.Name())
	}
}
