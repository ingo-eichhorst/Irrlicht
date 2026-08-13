// consent_test.go is the committed mutation evidence for issue #1488.
//
// Consent's whole claim is that a receiver cannot act on an inbound body
// without naming the permissions it acts under. Half of that claim is a type
// guarantee and needs no test — NewHandler takes a Consent and RequireConsent
// takes its first key positionally, so a keyless receiver does not compile, in
// any spelling, and AGENTS.md's rule is that a type needs no proof where a
// guard would.
//
// The other half CAN silently stop discriminating, so it is mutated here:
//
//   - the zero Consent, the one degenerate value the type still admits;
//   - a receiver that DECLARES a permission and never checks it — #1466's exact
//     shape, committed below as forgetfulReceiver rather than described, since
//     a paragraph in a merged PR body is re-run by nothing;
//   - the vacuity guard beside each, because an arm that refused
//     unconditionally would satisfy both mutations and read as coverage.
package hookjson

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"irrlicht/core/ports/outbound"
)

// keyedGranter answers per key, so one permission can be held denied while the
// others are granted — the state that separates a receiver gated on the right
// permission from one gated on any permission at all (issue #1475).
type keyedGranter map[string]bool

func (g keyedGranter) Granted(_ string, key string) bool { return g[key] }

func TestRequireConsent_DeclaresEveryKeyInOrder(t *testing.T) {
	c := RequireConsent(keyedGranter{}, "agent-x", "hooks", "transcripts", "statusline")

	if got, want := strings.Join(c.Keys(), ","), "hooks,transcripts,statusline"; got != want {
		t.Fatalf("Keys() = %q, want %q — a contract wiring derives its arms from this order", got, want)
	}
}

// TestConsentKeys_ReturnsACopy pins the copy in Keys(). Without it a contract
// wiring that sorted or truncated the returned slice would edit the receiver's
// own declaration, and the request path would then consult a set nobody
// declared.
func TestConsentKeys_ReturnsACopy(t *testing.T) {
	c := RequireConsent(keyedGranter{"hooks": true}, "agent-x", "hooks", "transcripts")

	keys := c.Keys()
	keys[0] = "clobbered"

	if got := c.Keys()[0]; got != "hooks" {
		t.Fatalf("mutating the returned slice changed the declaration: Keys()[0] = %q, want %q", got, "hooks")
	}
}

// TestConsentGranted_AnUndeclaredKeyIsAlwaysFalse is the load-bearing arm of
// Granted. A receiver checking a permission it never declared would gate itself
// on something the published set does not mention, so the contract would derive
// one set while the request path consulted another — the two-objects-that-
// disagree failure #1390 removed for the confiner. The granter here answers
// TRUE for that key, so only the declaration check can produce the false.
func TestConsentGranted_AnUndeclaredKeyIsAlwaysFalse(t *testing.T) {
	c := RequireConsent(keyedGranter{"hooks": true, "transcripts": true}, "agent-x", "hooks")

	if !c.Granted("hooks") {
		t.Fatal("a declared, granted key answered false — the vacuity guard for the arm below")
	}
	if c.Granted("transcripts") {
		t.Fatal("an UNDECLARED key answered true because the granter said so — a receiver could " +
			"then gate on a permission its published set never names, and the contract would " +
			"grade a different set than the request path consults (issue #1488)")
	}
}

// TestConsentGranted_NilGranterIsUngatedForDeclaredKeysOnly pins the test-only
// ungated shape every receiver constructor documents, and pins its bound: a nil
// granter is not a way to reach a permission that was never declared.
func TestConsentGranted_NilGranterIsUngatedForDeclaredKeysOnly(t *testing.T) {
	c := RequireConsent(nil, "agent-x", "hooks")

	if !c.Granted("hooks") {
		t.Error("a nil granter denied a declared key — every receiver constructor documents " +
			"`a nil gate means no gating`, and dozens of tests build handlers that way")
	}
	if c.Granted("transcripts") {
		t.Error("a nil granter granted an UNDECLARED key")
	}
}

// --- the chokepoint backstop, and the committed receiver that needs it ---

// forgetfulReceiver is issue #1466 reduced to a fixture and kept in the tree.
//
// It declares BOTH permissions and then checks only the first, which is exactly
// what claudecode's hook receiver did for the whole of its life: "hooks" was
// consulted, "transcripts" was declared by the adapter and never asked about,
// and every test in the tree was green. Before #1488 nothing below the
// receiver could tell it apart from a correct one — DecodeConfined took no
// keys, so it had nothing to compare against.
//
// It is committed rather than described because that is the difference between
// evidence that is re-run on every build and a paragraph in a merged PR body.
func forgetfulReceiver(confiner *PathConfiner, consent Consent, log outbound.Logger) HookHandler {
	return NewHandler(confiner, consent,
		func(c Consent, pc *PathConfiner, w http.ResponseWriter, r *http.Request) {
			// Checks the FIRST declared key only. The second is declared and
			// never asked about — the mutation.
			if !c.Granted(c.Keys()[0]) {
				w.WriteHeader(http.StatusOK)
				return
			}
			var p decodeTestPayload
			if !DecodeConfined(w, r, log, "forgetful-receiver", c, pc, &p, getDecodePath, setDecodePath) {
				return
			}
			// Dispatch: the effect a consent gate exists to prevent.
			w.WriteHeader(http.StatusTeapot)
		})
}

// postTo drives a handler with an in-tree transcript path and reports the
// status. 418 means the receiver dispatched.
func postTo(t *testing.T, h HookHandler, transcript string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postDecode(t, `{"transcript_path":`+strconv.Quote(transcript)+`}`))
	return rec.Code
}

// TestDecodeConfined_RefusesADeclaredPermissionThatWasNeverChecked drives the
// committed broken receiver above. With every declared key granted it must
// still dispatch (the vacuity guard — a backstop that refused unconditionally
// would satisfy the mutation below while discriminating nothing); with the
// declared-but-unchecked key denied it must not.
func TestDecodeConfined_RefusesADeclaredPermissionThatWasNeverChecked(t *testing.T) {
	confiner, root := decodeConfinerRooted(t)
	// An in-tree transcript, so the confiner's resolve step has a real leaf to
	// accept — these cases are about consent, and a path rejection would fail
	// them for the wrong reason.
	transcript := writeFile(t, filepath.Join(root, "session.jsonl"))

	t.Run("vacuity_guard_all_granted_still_dispatches", func(t *testing.T) {
		granter := keyedGranter{"hooks": true, "transcripts": true}
		h := forgetfulReceiver(confiner, RequireConsent(granter, "agent-x", "hooks", "transcripts"), &countingLogger{})

		if got := postTo(t, h, transcript); got != http.StatusTeapot {
			t.Fatalf("status = %d, want 418 — a receiver whose every declared permission is "+
				"granted must still dispatch, or this file's mutation proves nothing", got)
		}
	})

	t.Run("declared_but_unchecked_key_denied_drops_the_payload", func(t *testing.T) {
		granter := keyedGranter{"hooks": true, "transcripts": false}
		log := &countingLogger{}
		h := forgetfulReceiver(confiner, RequireConsent(granter, "agent-x", "hooks", "transcripts"), log)

		got := postTo(t, h, transcript)
		if got == http.StatusTeapot {
			t.Fatal("a receiver that DECLARES \"transcripts\" and never checks it dispatched with " +
				"\"transcripts\" denied — that is issue #1466 verbatim, and the whole point of " +
				"carrying the key set into the decode (issue #1488)")
		}
		if got < 200 || got > 299 {
			t.Errorf("status = %d, want 2xx — a consent refusal is reported by the log, never by a "+
				"status code on the user's critical path (#1361, #1364)", got)
		}
		if len(log.lines) == 0 {
			t.Error("the refusal was silent — the log is the only surface a dropped hook has")
		}
	})
}

// TestDecodeConfined_RefusesBeforeReadingAnything is the arm the guard's own
// doc sentence needs and the delete-mutation cannot supply: the refusal must
// come BEFORE the body read and the confine, not merely before the dispatch.
//
// Review of PR #1506 moved the ungranted() check verbatim to just after
// set(p, confined) and every package stayed green — so nothing held it in
// place. What that permits is #570 broken inside the function added to enforce
// it: a receiver whose declared key is denied would still allocate and decode
// up to 1 MiB of caller-supplied body, run PathConfiner.Confine (a real
// symlink resolution and stat of a path the user denied us), and move the
// confiner's rejection counters, which AssertHookPathConfined and the
// diagnostics bundle both read.
//
// Both observations are needed. The untouched payload catches a guard moved
// below the decode; the untouched counters catch one moved below the confine,
// which the payload alone cannot see for an out-of-tree path.
func TestDecodeConfined_RefusesBeforeReadingAnything(t *testing.T) {
	confiner, _ := decodeConfinerRooted(t)

	granter := keyedGranter{"hooks": true, "transcripts": false}
	consent := RequireConsent(granter, "agent-x", "hooks", "transcripts")

	before := confiner.RejectionCount()
	rec := httptest.NewRecorder()
	p := decodeTestPayload{TranscriptPath: "untouched"}

	// An OUT-OF-TREE path: were the guard below the confine, this would be
	// refused there and bump a counter. Consent must refuse it first.
	if DecodeConfined(rec, postDecode(t, `{"transcript_path":"/nowhere/at/all.jsonl","hook_event_name":"Stop"}`),
		&countingLogger{}, "test-receiver", consent, confiner, &p, getDecodePath, setDecodePath) {
		t.Fatal("a request with a declared permission denied was accepted")
	}

	if p.TranscriptPath != "untouched" || p.Event != "" {
		t.Errorf("the body was DECODED for a consent-denied request (payload = %+v) — a receiver "+
			"whose permission is withheld must not allocate and parse up to %d bytes on its "+
			"behalf (#570: nothing is exercised while pending or denied)", p, maxHookBodyBytes)
	}
	if got := confiner.RejectionCount(); got != before {
		t.Errorf("the confiner ran for a consent-denied request (rejections %d -> %d) — that "+
			"resolves and stats a path the user denied us, and moves counters the path-confinement "+
			"contract and the diagnostics bundle read", before, got)
	}
}

// TestDecodeConfined_ZeroConsentFailsClosed covers the one degenerate value the
// type still admits. RequireConsent cannot produce it (its first key is
// positional), so this is reachable only by writing Consent{} — and a guard no
// test executes is a guard no mutation can redden, which is the argument
// decode_test.go's header makes for the nil-confiner arm.
func TestDecodeConfined_ZeroConsentFailsClosed(t *testing.T) {
	confiner, root := decodeConfinerRooted(t)
	transcript := writeFile(t, filepath.Join(root, "session.jsonl"))

	rec := httptest.NewRecorder()
	log := &countingLogger{}
	var p decodeTestPayload

	ok := DecodeConfined(rec, postDecode(t, `{"transcript_path":`+strconv.Quote(transcript)+`}`),
		log, "test-receiver", Consent{}, confiner, &p, getDecodePath, setDecodePath)

	if ok {
		t.Fatal("a receiver that named NO permission reached its payload — every hook receiver " +
			"acts under at least one (#570), and an empty declaration must not read a body")
	}
	if rec.Code < 200 || rec.Code > 299 {
		t.Errorf("status = %d, want 2xx — same rule as any other refusal", rec.Code)
	}
	if len(log.lines) == 0 {
		t.Error("the refusal was silent")
	}
}

// TestNewHandler_PublishesTheConsentTheRequestPathUses is the #1390 property
// restated for consent: a contract wiring derives its arms from
// HookHandler.Consent, so that value has to be the one the serve func is handed
// rather than a second one built beside it.
func TestNewHandler_PublishesTheConsentTheRequestPathUses(t *testing.T) {
	confiner, _ := decodeConfinerRooted(t)
	declared := RequireConsent(keyedGranter{"hooks": true}, "agent-x", "hooks", "transcripts")

	var served Consent
	h := NewHandler(confiner, declared, func(c Consent, _ *PathConfiner, w http.ResponseWriter, _ *http.Request) {
		served = c
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/test", strings.NewReader(`{}`))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got, want := strings.Join(served.Keys(), ","), strings.Join(h.Consent.Keys(), ","); got != want {
		t.Fatalf("the request path saw %q but the handler publishes %q — a contract would grade "+
			"a set the receiver does not use", got, want)
	}
	if got := strings.Join(h.Consent.Keys(), ","); got != "hooks,transcripts" {
		t.Fatalf("HookHandler.Consent.Keys() = %q, want %q", got, "hooks,transcripts")
	}
}
