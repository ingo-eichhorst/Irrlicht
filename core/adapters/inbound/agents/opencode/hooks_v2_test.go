// hooks_v2_test.go grades what the receiver does with the opencode 2.x
// plugin's payloads (#2108): the three session.execution.* turn ends, and the
// event-id dedupe that keeps one event from being dispatched once per plugin
// instance.
package opencode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// v2Payload renders the body plugin_v2.js writes: the 1.x fields plus the
// event's own id. Hand-built with the wire names rather than marshalled from
// openCodeHookPayload, so it is the PLUGIN's shape these tests post, not
// whatever the receiver's struct happens to decode.
func v2Payload(event, eventID string) string {
	body, err := json.Marshal(map[string]string{
		"hook_event_name": event,
		"session_id":      contractSessionID,
		"event_id":        eventID,
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// TestHookReceiver_ARepeatedEventIDDispatchesOnce is red-first. The 2.x plugin
// runs once per project directory the service has open, every instance
// receives the same stream, and #2104 measured each event arriving twice (item
// 12). Before #2108 the receiver dispatched every delivery.
func TestHookReceiver_ARepeatedEventIDDispatchesOnce(t *testing.T) {
	opencodeStoreRoot(t)
	h, target := newReceiver(t)

	for i := 0; i < 2; i++ {
		if rec := post(t, h, v2Payload(HookEventPermissionAsked, "evt_asked_1")); rec.Code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200", i+1, rec.Code)
		}
	}

	if n := len(target.promptCalls()); n != 1 {
		t.Errorf("HandlePermissionPromptHook called %d times for one event delivered twice, want 1", n)
	}
}

// TestHookReceiver_ALateRepeatOfAskedDoesNotHoldAgain is red-first, and it is
// the ordering the dedupe exists for: the second instance's copy of
// permission.asked lands AFTER permission.replied released the hold. Without
// the dedupe that copy holds the session waiting again with no reply left to
// release it.
func TestHookReceiver_ALateRepeatOfAskedDoesNotHoldAgain(t *testing.T) {
	opencodeStoreRoot(t)
	h, target := newReceiver(t)

	post(t, h, v2Payload(HookEventPermissionAsked, "evt_asked_1"))
	post(t, h, v2Payload(HookEventPermissionReplied, "evt_replied_1"))
	post(t, h, v2Payload(HookEventPermissionAsked, "evt_asked_1"))
	post(t, h, v2Payload(HookEventPermissionReplied, "evt_replied_1"))

	if n := len(target.promptCalls()); n != 1 {
		t.Errorf("the session was held %d times for one permission prompt, want 1 — the late repeat "+
			"of permission.asked holds it waiting with no reply left to come", n)
	}
	if n := len(target.releaseCalls()); n != 1 {
		t.Errorf("released %d times for one reply, want 1", n)
	}
}

// TestHookReceiver_DistinctEventIDsAllDispatch is a LOCK on the dedupe's key:
// two DIFFERENT events of the same name and session — two prompts in one turn
// — both dispatch. A key coarser than the event id would drop the second.
func TestHookReceiver_DistinctEventIDsAllDispatch(t *testing.T) {
	opencodeStoreRoot(t)
	h, target := newReceiver(t)

	post(t, h, v2Payload(HookEventPermissionAsked, "evt_asked_1"))
	post(t, h, v2Payload(HookEventPermissionAsked, "evt_asked_2"))

	if n := len(target.promptCalls()); n != 2 {
		t.Errorf("two distinct permission.asked events dispatched %d times, want 2", n)
	}
}

// TestHookReceiver_AOneXPayloadDispatchesEveryTime is a LOCK: the 1.x plugin
// forwards no event id, each of its events arrives once, and the dedupe must not
// touch it — two identical 1.x deliveries are two events.
func TestHookReceiver_AOneXPayloadDispatchesEveryTime(t *testing.T) {
	opencodeStoreRoot(t)
	h, target := newReceiver(t)

	post(t, h, contractPayload(HookEventPermissionAsked))
	post(t, h, contractPayload(HookEventPermissionAsked))

	if n := len(target.promptCalls()); n != 2 {
		t.Errorf("two 1.x permission.asked deliveries dispatched %d times, want 2", n)
	}
}

// TestHookReceiver_TwoXTurnEndsReleaseAndReportTurnEnd is red-first: 2.x ends a
// turn with session.execution.succeeded, .failed or .interrupted and never
// publishes session.idle (#2104 item 8). Each must do what session.idle does —
// release a held prompt, then report the authoritative turn end. Before #2108
// each was an unknown event, so a 2.x turn never ended through the hook channel
// and a prompt torn down by an interrupt stayed held.
func TestHookReceiver_TwoXTurnEndsReleaseAndReportTurnEnd(t *testing.T) {
	for i, event := range []string{HookEventExecutionSucceeded, HookEventExecutionFailed, HookEventExecutionInterrupted} {
		t.Run(event, func(t *testing.T) {
			opencodeStoreRoot(t)
			h, target := newReceiver(t)

			rec := post(t, h, v2Payload(event, fmt.Sprintf("evt_end_%d", i)))

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
			if got := target.releaseCalls(); len(got) != 1 || got[0] != contractSessionID {
				t.Errorf("ReleasePermissionPromptHold calls = %v, want exactly [%q] — an interrupt "+
					"emits no permission.replied, so this is the only release it gets", got, contractSessionID)
			}
			stops := target.stops()
			if len(stops) != 1 {
				t.Fatalf("HandleStopHook called %d times, want 1", len(stops))
			}
			if want := transcriptPathFor(contractSessionID); stops[0].transcriptPath != want {
				t.Errorf("transcriptPath = %q, want the watcher's key %q", stops[0].transcriptPath, want)
			}
		})
	}
}

// TestRecentEventIDs_ForgetsTheOldestPastItsBound pins the bound: the set holds
// at most its capacity, evicting in arrival order, so a long-running daemon's
// memory does not grow with every event it ever saw.
func TestRecentEventIDs_ForgetsTheOldestPastItsBound(t *testing.T) {
	r := newRecentEventIDs(2)
	for _, id := range []string{"a", "b"} {
		if !r.firstSighting(id) {
			t.Fatalf("%q reported as a repeat on first sight", id)
		}
	}
	if r.firstSighting("a") {
		t.Error(`"a" reported new while still within the bound`)
	}
	r.firstSighting("c") // evicts "a"
	if !r.firstSighting("a") {
		t.Error(`"a" still reported as a repeat after two newer ids pushed it out of a 2-id bound`)
	}
	if len(r.ids) > 2 {
		t.Errorf("the set holds %d ids, past its bound of 2", len(r.ids))
	}
}
