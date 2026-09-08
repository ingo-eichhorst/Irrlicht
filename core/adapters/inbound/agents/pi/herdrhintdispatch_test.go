// herdrhintdispatch_test.go covers the receiver's half of #1936: which
// payloads reach HandleHerdrPaneHint, and what a rejected one does to the
// turn-end signal riding in the same request.
//
// The confinement itself is graded in herdrhint_test.go. What is asserted here
// is that the receiver applies it, and that failing it is quiet rather than
// fatal — a bad pane must never cost the session its lifecycle signal, which
// is the whole reason the two are separate dispatches instead of one payload.
package pi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// herdrPayload renders a body with the two extra fields the extension sends
// when it is running inside herdr.
func herdrPayload(transcriptPath, event, paneID, socketPath string) string {
	body, err := json.Marshal(piHookPayload{
		HookEventName:   event,
		TranscriptPath:  transcriptPath,
		HerdrPaneID:     paneID,
		HerdrSocketPath: socketPath,
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func TestAgentSettledHook_DispatchesAConfinedHerdrPane(t *testing.T) {
	root := piSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-herdr")
	sock := listenAt(t, filepath.Join(shortHerdrRoot(t), "sessions", "f", "herdr.sock"))
	h, target := newReceiver(t)

	rec := post(t, h, herdrPayload(tp, HookEventAgentSettled, "w1:p2", sock))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	panes := target.panes()
	if len(panes) != 1 {
		t.Fatalf("HandleHerdrPaneHint called %d times, want 1", len(panes))
	}
	if panes[0].sessionID != "sess-herdr" {
		t.Errorf("sessionID = %q, want the transcript filename stem — the pane is "+
			"attributed the same way every other pi hook is", panes[0].sessionID)
	}
	if panes[0].paneID != "w1:p2" || panes[0].socketPath != sock {
		t.Errorf("dispatched %q on %q, want w1:p2 on %q", panes[0].paneID, panes[0].socketPath, sock)
	}
	if len(target.stops()) != 1 {
		t.Errorf("HandleStopHook called %d times, want 1 — the pane rides along with "+
			"the turn-end signal, it does not replace it", len(target.stops()))
	}
}

// TestHerdrPaneIsDispatchedBeforeTheTurnEnd pins the ordering serveHookRequest
// documents. HandleStopHook drives a classify pass that pushes the session, so
// a pane adopted after it would not be in that push and the app would show the
// repair one turn late.
func TestHerdrPaneIsDispatchedBeforeTheTurnEnd(t *testing.T) {
	root := piSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-order")
	sock := listenAt(t, filepath.Join(shortHerdrRoot(t), "herdr.sock"))
	h, target := newReceiver(t)

	post(t, h, herdrPayload(tp, HookEventAgentSettled, "w1:p1", sock))

	if got := strings.Join(target.dispatchOrder(), ","); got != "pane,stop" {
		t.Errorf("dispatch order %q, want \"pane,stop\"", got)
	}
}

// TestRejectedHerdrPaneStillDeliversTheTurnEnd is the failure-mode assertion.
// Every reason a pane can be refused is a reason to store nothing, and none of
// them is a reason to drop the lifecycle event the same request carries.
func TestRejectedHerdrPaneStillDeliversTheTurnEnd(t *testing.T) {
	confinedRoot := shortHerdrRoot(t)
	outside := listenAt(t, filepath.Join(filepath.Dir(confinedRoot), "elsewhere", "herdr.sock"))
	good := listenAt(t, filepath.Join(confinedRoot, "herdr.sock"))

	for _, tc := range []struct {
		name, paneID, socketPath string
	}{
		{"no herdr at all", "", ""},
		{"socket outside herdr's tree", "w1:p1", outside},
		{"socket that is not there", "w1:p1", filepath.Join(confinedRoot, "gone", "herdr.sock")},
		{"pane that is not pane-shaped", "w1:p1; rm -rf /", good},
		{"pane with no socket", "w1:p1", ""},
		{"socket with no pane", "", good},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := piSessionRoot(t)
			tp := writeSessionTranscript(t, root, "sess-rejected")
			h, target := newReceiver(t)

			rec := post(t, h, herdrPayload(tp, HookEventAgentSettled, tc.paneID, tc.socketPath))

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 — a refused pane is not a bad request, "+
					"it is a session that has none", rec.Code)
			}
			if n := len(target.panes()); n != 0 {
				t.Errorf("HandleHerdrPaneHint called %d times for %q on %q", n, tc.paneID, tc.socketPath)
			}
			if n := len(target.stops()); n != 1 {
				t.Errorf("HandleStopHook called %d times, want 1: refusing the pane must "+
					"not cost the session its turn-end signal", n)
			}
		})
	}
}

// TestHerdrPaneNeedsTheHooksConsent pins that the pane goes through the same
// gates the rest of the payload does rather than around them. It is dispatched
// from inside the event switch, so a receiver that is not allowed to act on
// the request cannot act on the pane either.
func TestHerdrPaneNeedsTheHooksConsent(t *testing.T) {
	root := piSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-denied")
	sock := listenAt(t, filepath.Join(shortHerdrRoot(t), "herdr.sock"))

	target := &mockTarget{}
	denied := keyedGate{PermissionKeyHooks: false, PermissionKeyTranscripts: true}
	h := NewHookHandler(target, denied, mockLogger{})

	rec := post(t, h, herdrPayload(tp, HookEventAgentSettled, "w1:p1", sock))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want a quiet 200", rec.Code)
	}
	if n := len(target.panes()); n != 0 {
		t.Errorf("HandleHerdrPaneHint called %d times without the hooks consent", n)
	}
}
