// hook_endpoint_test.go carries direct unit coverage of the two default
// bridges (endpointOfRaw, rawEntriesOf) that does not need a whole reference
// installer — both were found, by a coordinator review of #1734, to have a
// silent-failure shape the rest of this file's own obligations do not
// exercise, because the obligations only ever run these bridges through a
// CORRECT reference installer.
//
// endpointOfRaw's default bridge used to return "" when an entry did not
// unmarshal as JSON, which is not a neutral value under the
// DeliveryAddressFree route: assertDeliveryCarriesNoAddress
// (hook_endpoint.go) checks that the delivery is IDENTICAL across bind
// addresses and carries no address-shaped fragment, and "" satisfies both
// trivially — so a non-JSON entry shape with EndpointOfRaw left unset made
// obligation 1 pass having graded nothing. It was caught in review only
// because a NEIGHBOURING guard (assertDeliveryIsOurs) happened to run first
// in the one obligation that calls it before the address check; the fix
// (see endpointOfRaw's own doc comment) makes the bridge itself fail loudly,
// and TestEndpointOfRaw_MissingBridgeFailsLoudlyNotSilently below is the
// isolated lock for it — no neighbour anywhere in the call.
//
// rawEntriesOf's default bridge was the natural next question: can IT
// return an empty slice, no error, on a settings file it only "half
// understands" — present, valid JSON, but shaped wrong? An empty slice with
// no error is indistinguishable from "nothing installed yet" downstream
// (onlyRawEntry's own "expected exactly 1" check would fire either way), so
// if the bridge produced one for a malformed file it would be the read-side
// twin of the write-side defect, just with a less exotic failure mode. It
// does not (traced and locked below), but the tracing is committed as tests
// rather than left as a claim in a PR body.
package contracttesting

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRawEntriesOf_HooksKeyPresentButWrongShape_IsAnErrorNotEmpty is the
// concrete case: "hooks" exists in the settings file — so
// readSettingsHooksMap's own type assertion is the one that can go quiet —
// but its VALUE is not a map at all (a string, standing in for any
// non-object JSON value). readSettingsHooksMap's comma-ok assertion turns
// that into hooksMap == nil with no error; the traced claim under test is
// that entriesOf's own next assertion (hooksMap[event].([]interface{}) on a
// nil map) is what turns "wrong shape" into a real error rather than into a
// silently empty entry list.
func TestRawEntriesOf_HooksKeyPresentButWrongShape_IsAnErrorNotEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// "hooks" is a STRING, not an object — present, valid JSON, wrong shape.
	if err := os.WriteFile(path, []byte(`{"hooks":"not-an-object"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	h := HookInstaller{} // EntriesOf nil -> NestedHookEntries; ReadEntries nil -> the default bridge
	entries, err := rawEntriesOf(h)(path, "stop")
	if err == nil {
		t.Fatalf("rawEntriesOf on a settings file whose \"hooks\" key is a string: got %d entries and no error, want a refusal — a malformed shape must not read as \"nothing installed\"", len(entries))
	}
}

// TestRawEntriesOf_EventKeyGenuinelyAbsent_IsAlsoAnError is the companion
// case on the same code path, stated so the mutation evidence above is not
// mistaken for "any parse produces an error": a WELL-FORMED hooks object
// that simply does not mention the requested event is ALSO reported as an
// error by NestedHookEntries/FlatHookEntries ("missing from the settings
// file or not an array") — not silently zero — which is a stronger
// contract than strictly necessary but is the one this seam already makes,
// and this test locks it rather than leaving it to be discovered by reading
// NestedHookEntries's source.
func TestRawEntriesOf_EventKeyGenuinelyAbsent_IsAlsoAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	h := HookInstaller{}
	if _, err := rawEntriesOf(h)(path, "stop"); err == nil {
		t.Fatal("rawEntriesOf on a hooks object with no \"stop\" key: got no error, want one")
	}
}

// TestRawEntriesOf_EventPresentButEmptyArray_IsLegitimatelyZero is the
// vacuity guard for both tests above: an event key that IS present, and IS
// an array, and genuinely holds nothing, is the everyday "nothing installed
// for this event yet" state and must NOT be refused — refusing it would make
// the two error cases above meaningless (every input would error) and would
// make an adapter's very first, pre-install read of its own settings file
// fail this seam outright.
func TestRawEntriesOf_EventPresentButEmptyArray_IsLegitimatelyZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{"stop":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	h := HookInstaller{}
	entries, err := rawEntriesOf(h)(path, "stop")
	if err != nil {
		t.Fatalf("rawEntriesOf on a genuinely empty \"stop\" array: %v, want nil error", err)
	}
	if len(entries) != 0 {
		t.Fatalf("rawEntriesOf on a genuinely empty \"stop\" array: got %d entries, want 0", len(entries))
	}
}

// TestEndpointOfRaw_MissingBridgeFailsLoudlyNotSilently is the committed
// lock for endpointOfRaw's own doc comment: its default JSON bridge, called
// with an entry that does not unmarshal as JSON, reports the parse failure
// itself rather than answering "". It drives endpointOfRaw DIRECTLY through
// the package's own negative-self-test harness (observe/mustReport/
// mustBeSilent, selftest_test.go) — the same harness every <family>_selftest_
// test.go uses — so there is no neighbouring assertion anywhere in the call
// that could be catching this instead: a broken bridge here has nothing else
// standing between it and the recorder.
func TestEndpointOfRaw_MissingBridgeFailsLoudlyNotSilently(t *testing.T) {
	h := HookInstaller{
		EndpointOf: func(hook map[string]interface{}) string {
			c, _ := hook["command"].(string)
			return c
		},
		// EndpointOfRaw deliberately left nil: this is the case under test.
	}

	broken := observe(t, func(at armT) {
		endpointOfRaw(at, h)([]byte("[[hooks]]\ncommand = \"not json at all\"\n"))
	})
	mustReport(t, broken, "does not unmarshal as JSON", "non-JSON entry, EndpointOfRaw unset")

	// Vacuity guard: an ordinary JSON entry — every adapter wired before
	// #1718, and kiro-cli's flat shape — reaches h.EndpointOf silently, the
	// same default bridge every existing wiring already depends on.
	correct := observe(t, func(at armT) {
		endpointOfRaw(at, h)([]byte(`{"command":"curl -sf http://127.0.0.1:7837/api/v1/hooks"}`))
	})
	mustBeSilent(t, correct, "well-formed JSON entry")
}
