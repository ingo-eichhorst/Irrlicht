package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSessionState_BackgroundJSON locks the #744 client contract: nil Background
// is omitted (so existing goldens are unchanged), a populated one serializes as
// {name, detached}, and detached=false is dropped by omitempty.
func TestSessionState_BackgroundJSON(t *testing.T) {
	s := &SessionState{SessionID: "s", State: StateReady}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "background") {
		t.Errorf("nil Background must be omitted, got: %s", b)
	}

	s.Background = &BackgroundAgent{Name: "bg job", Detached: true}
	b, _ = json.Marshal(s)
	js := string(b)
	if !strings.Contains(js, `"background":{`) ||
		!strings.Contains(js, `"name":"bg job"`) ||
		!strings.Contains(js, `"detached":true`) {
		t.Errorf("background JSON shape wrong: %s", js)
	}

	s.Background = &BackgroundAgent{Name: "bg job"} // Detached false
	b, _ = json.Marshal(s)
	if strings.Contains(string(b), "detached") {
		t.Errorf("detached=false must be omitted, got: %s", b)
	}
}
