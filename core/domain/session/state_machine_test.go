package session_test

import (
	"testing"

	"irrlicht/core/domain/session"
)

func TestSmartStateTransition_SessionStart(t *testing.T) {
	tests := []struct {
		name               string
		matcher            string
		source             string
		wantState          string
		wantCompaction     string
		wantReasonContains string
	}{
		{"startup", "startup", "", StateReady, CompactionNotCompacting, "startup"},
		{"compact resume", "compact", "", StateWorking, CompactionPostCompact, "compaction"},
		{"resume", "resume", "", StateWorking, CompactionNotCompacting, "resumed"},
		{"clear source", "", "clear", StateReady, CompactionNotCompacting, "clear"},
		{"unknown matcher", "other", "", StateReady, CompactionNotCompacting, "new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := session.SmartStateTransition("SessionStart", tt.matcher, tt.source, "", nil, false)
			if r.NewState != tt.wantState {
				t.Errorf("state: got %q, want %q", r.NewState, tt.wantState)
			}
			if r.NewCompactionState != tt.wantCompaction {
				t.Errorf("compaction: got %q, want %q", r.NewCompactionState, tt.wantCompaction)
			}
			if tt.wantReasonContains != "" && !containsStr(r.Reason, tt.wantReasonContains) {
				t.Errorf("reason %q does not contain %q", r.Reason, tt.wantReasonContains)
			}
		})
	}
}

func TestSmartStateTransition_Notification(t *testing.T) {
	r := session.SmartStateTransition("Notification", "", "", "", nil, false)
	if r.NewState != StateWaiting {
		t.Errorf("got %q, want waiting", r.NewState)
	}
}

func TestSmartStateTransition_PreToolUse_FromWaiting(t *testing.T) {
	prev := &session.SessionState{State: StateWaiting}
	r := session.SmartStateTransition("PreToolUse", "", "", "", prev, false)
	if r.NewState != StateWorking {
		t.Errorf("got %q, want working", r.NewState)
	}
	if !containsStr(r.Reason, "notification") {
		t.Errorf("reason %q should mention notification", r.Reason)
	}
}

func TestSmartStateTransition_PreToolUse_NotFromWaiting(t *testing.T) {
	// No previous state → stays working (simple mapping)
	r := session.SmartStateTransition("PreToolUse", "", "", "", nil, false)
	if r.NewState != StateWorking {
		t.Errorf("got %q, want working", r.NewState)
	}
}

func TestSmartStateTransition_SessionEnd(t *testing.T) {
	tests := []struct {
		reason    string
		wantState string
	}{
		{"prompt_input_exit", StateCancelledByUser},
		{"clear", StateDeleteSession},
		{"logout", StateDeleteSession},
		{"", StateDeleteSession},
		{"other", StateDeleteSession},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			r := session.SmartStateTransition("SessionEnd", "", "", tt.reason, nil, false)
			if r.NewState != tt.wantState {
				t.Errorf("reason=%q: got %q, want %q", tt.reason, r.NewState, tt.wantState)
			}
		})
	}
}

func TestSmartStateTransition_TranscriptActivity_Overrides(t *testing.T) {
	// Even a Notification event should resolve to working when transcript grew
	prev := &session.SessionState{
		State:            StateWaiting,
		TranscriptPath:   "/some/path",
		LastTranscriptSize: 100,
	}
	now := int64(1)
	prev.WaitingStartTime = &now
	r := session.SmartStateTransition("Notification", "", "", "", prev, true)
	if r.NewState != StateWorking {
		t.Errorf("got %q, want working (transcript activity)", r.NewState)
	}
	if r.Reason != "transcript_activity_detected" {
		t.Errorf("reason %q", r.Reason)
	}
}

func TestSmartStateTransition_UserPromptSubmit_AfterNotification(t *testing.T) {
	prev := &session.SessionState{State: StateWaiting}
	r := session.SmartStateTransition("UserPromptSubmit", "", "", "", prev, false)
	if r.NewState != StateWorking {
		t.Errorf("got %q, want working", r.NewState)
	}
}

func TestSmartStateTransition_UserPromptSubmit_ClearsPostCompact(t *testing.T) {
	prev := &session.SessionState{
		State:           StateWorking,
		CompactionState: CompactionPostCompact,
	}
	r := session.SmartStateTransition("UserPromptSubmit", "", "", "", prev, false)
	if r.NewCompactionState != CompactionNotCompacting {
		t.Errorf("compaction: got %q, want not_compacting", r.NewCompactionState)
	}
}

func TestSmartStateTransition_PreCompact(t *testing.T) {
	for _, matcher := range []string{"auto", "manual", ""} {
		r := session.SmartStateTransition("PreCompact", matcher, "", "", nil, false)
		if r.NewState != StateWorking {
			t.Errorf("matcher=%q: got %q, want working", matcher, r.NewState)
		}
		if r.NewCompactionState != CompactionCompacting {
			t.Errorf("matcher=%q: compaction got %q, want compacting", matcher, r.NewCompactionState)
		}
	}
}

func TestSmartStateTransition_Stop(t *testing.T) {
	r := session.SmartStateTransition("Stop", "", "", "", nil, false)
	if r.NewState != StateReady {
		t.Errorf("got %q, want ready", r.NewState)
	}
}

func TestMergeMetrics(t *testing.T) {
	old := &session.SessionMetrics{
		ElapsedSeconds:     100,
		TotalTokens:        500,
		ModelName:          "claude-3",
		ContextUtilization: 0.75,
		PressureLevel:      "low",
	}
	// New metrics with some zero/empty values
	newM := &session.SessionMetrics{
		ElapsedSeconds: 0,
		TotalTokens:    600,
		ModelName:      "",
		PressureLevel:  "unknown",
	}
	merged := session.MergeMetrics(newM, old)
	if merged.ElapsedSeconds != 100 {
		t.Errorf("elapsed: got %d, want 100 (preserved from old)", merged.ElapsedSeconds)
	}
	if merged.TotalTokens != 600 {
		t.Errorf("tokens: got %d, want 600 (new value)", merged.TotalTokens)
	}
	if merged.ModelName != "claude-3" {
		t.Errorf("model: got %q, want claude-3 (preserved)", merged.ModelName)
	}
	if merged.PressureLevel != "low" {
		t.Errorf("pressure: got %q, want low (preserved)", merged.PressureLevel)
	}
}

func TestMergeMetrics_NilHandling(t *testing.T) {
	old := &session.SessionMetrics{TotalTokens: 42}
	if session.MergeMetrics(nil, old) != old {
		t.Error("nil new should return old")
	}
	newM := &session.SessionMetrics{TotalTokens: 99}
	if session.MergeMetrics(newM, nil) != newM {
		t.Error("nil old should return new")
	}
	if session.MergeMetrics(nil, nil) != nil {
		t.Error("nil,nil should return nil")
	}
}

// --- helpers -----------------------------------------------------------------

const (
	StateReady           = session.StateReady
	StateWorking         = session.StateWorking
	StateWaiting         = session.StateWaiting
	StateCancelledByUser = session.StateCancelledByUser
	StateDeleteSession   = session.StateDeleteSession

	CompactionNotCompacting = session.CompactionStateNotCompacting
	CompactionCompacting    = session.CompactionStateCompacting
	CompactionPostCompact   = session.CompactionStatePostCompact
)

func containsStr(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || stringContains(s, sub))
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
