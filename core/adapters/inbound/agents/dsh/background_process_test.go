package dsh

import (
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

// TestParserMapsMeasuredBackgroundJobSignals is red against the pre-fix parser:
// it ignored the measured DSH run_in_background launch, result, and tool-jobs
// terminal notice in the 3.3 recording.
func TestParserMapsMeasuredBackgroundJobSignals(t *testing.T) {
	events := parseFixture(t, "testdata/background-process.jsonl")
	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3", len(events))
	}
	if got := events[1].BackgroundSpawns; len(got) != 1 || got[0].BashID != "bash-1" || !got[0].NoProbeHold {
		t.Fatalf("background spawns = %+v, want bash-1 with a no-probe hold", got)
	}
	if !events[2].Skip {
		t.Fatalf("terminal plugin notice must be bookkeeping, got %+v", events[2])
	}
	if got := events[2].TerminatedBackgroundTaskIDs; !reflect.DeepEqual(got, []string{"bash-1"}) {
		t.Fatalf("terminal background jobs = %v, want [bash-1]", got)
	}
}

// TestParserRejectsUncorrelatedBackgroundStart is the committed mutation
// fixture: changing the result's callId prevents the matching background call
// from proving that bash-1 was launched.
func TestParserRejectsUncorrelatedBackgroundStart(t *testing.T) {
	events := parseFixture(t, "testdata/background-process-uncorrelated-result.jsonl")
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	if got := events[1].BackgroundSpawns; len(got) != 0 {
		t.Fatalf("uncorrelated result created background spawns: %+v", got)
	}
}

func TestBackgroundJobHoldSurvivesTurnEndUntilTerminalNotice(t *testing.T) {
	path := fmt.Sprintf("%s/session.v3.jsonl.zstd", t.TempDir())
	now := time.Now().UnixMilli()
	writeZstdFrame(t, path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fmt.Sprintf(`{"type":"session","version":3,"createdAt":%d}`+"\n"+
		`{"type":"tool/call","time":%d,"data":{"callId":"call-1","name":"bash","arguments":"{\"run_in_background\":true}"}}`+"\n"+
		`{"type":"tool/result","time":%d,"data":{"message":{"source":{"callId":"call-1"},"content":[{"type":"tool-result","content":[{"type":"text","text":"started background job bash-1"}],"isError":false}]}}}`+"\n"+
		`{"type":"turn/end","time":%d,"data":{"reason":{"kind":"completed"}}}`+"\n", now, now+1, now+2, now+3))

	transcriptTailer := newTestTranscriptTailer(path)
	metrics := tailTranscript(t, transcriptTailer)
	if metrics.BackgroundProcessCount != 1 || !metrics.BackgroundProcessClockBound {
		t.Fatalf("background metrics after turn end = %+v, want one clock-bound hold", metrics)
	}
	domainMetrics := &session.SessionMetrics{
		LastEventType:               metrics.LastEventType,
		BackgroundProcessCount:      metrics.BackgroundProcessCount,
		BackgroundProcessClockBound: metrics.BackgroundProcessClockBound,
		HasLiveBackgroundProcess:    metrics.BackgroundProcessClockBound,
	}
	if state, _ := services.ClassifyState(session.StateWorking, domainMetrics); state != session.StateWorking {
		t.Fatalf("turn_done with a live background hold = %q, want working", state)
	}

	// The same tailer reads the next independently-compressed DSH frame.
	// This is the daemon path: the terminal notice releases the existing hold.
	writeZstdFrame(t, path, os.O_WRONLY|os.O_APPEND, fmt.Sprintf(`{"type":"user/message","time":%d,"data":{"content":[{"type":"text","text":"background job bash-1 (bash: sleep 120 && echo BG_DONE) finished [status: completed, exit code: 0]."}],"source":{"kind":"plugin","plugin":"tool-jobs","form":"notice"}}}`+"\n", now+4))
	metrics = tailTranscript(t, transcriptTailer)
	if metrics.BackgroundProcessCount != 0 || metrics.BackgroundProcessClockBound {
		t.Fatalf("background metrics after terminal notice = %+v, want released hold", metrics)
	}
}

func TestBackgroundJobNoProbeHoldExpires(t *testing.T) {
	path := fmt.Sprintf("%s/session.v3.jsonl.zstd", t.TempDir())
	past := time.Now().Add(-13 * time.Hour).UnixMilli()
	writeZstdFrame(t, path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fmt.Sprintf(`{"type":"session","version":3,"createdAt":%d}`+"\n"+
		`{"type":"tool/call","time":%d,"data":{"callId":"call-1","name":"bash","arguments":"{\"run_in_background\":true}"}}`+"\n"+
		`{"type":"tool/result","time":%d,"data":{"message":{"source":{"callId":"call-1"},"content":[{"type":"tool-result","content":[{"type":"text","text":"started background job bash-1"}],"isError":false}]}}}`+"\n", past, past+1, past+2))

	metrics := tailTranscript(t, newTestTranscriptTailer(path))
	if metrics.BackgroundProcessCount != 0 || metrics.BackgroundProcessClockBound {
		t.Fatalf("expired no-probe hold = %+v, want no live background hold", metrics)
	}
}
