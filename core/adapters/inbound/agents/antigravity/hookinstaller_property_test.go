// hookinstaller_property_test.go is the generator-driven half of this
// adapter's install coverage, required by docs/testing-philosophy.md for code
// that emits bytes from a structural diff.
//
// # What it covers that hookjson's own property test does not
//
// The bytes are emitted by jsonc.go's splice, and that splicer already carries
// TestSplice_PropertyRandomMutations — the test whose history is the argument
// for this file existing at all: seven hand-written round-trip tests passed
// green while ~11% of randomly shaped documents got `,,` written into them,
// and a fourth defect survived until the generator varied ARRAYS and
// multi-item removals.
//
// This file grades the layer above it: the MERGE, over a document shape the
// splicer knows nothing about. `~/.gemini/config/hooks.json` is keyed by
// top-level hook NAME, each name holding per-event arrays whose structure
// differs by event, and it is shared with Antigravity's own /hooks command. So
// the obligations are preservation ones, and a hand-written case list cannot
// establish them: the failure mode is "some shape nobody thought of gets
// clobbered".
//
// # The axis the generator varies is the one a real file varies on
//
// #1753 is why that sentence is here rather than assumed. mistral-vibe's
// splicer shipped with a property test whose generator produced only documents
// the splicer modelled, so the refusal path was exercised as a guard and never
// as the thing that fires on real input — and vibe's own config.toml, written
// by vibe, tripped it twelve times over. The generator below therefore
// produces what Antigravity's /hooks command and its shipped doc actually
// produce: several named hooks, both the GROUPED (PreToolUse/PostToolUse) and
// FLAT (Pre/PostInvocation, Stop) per-event structures, `enabled` flags,
// handler objects with and without `type`/`timeout`, comments, CRLF, varying
// key order — plus, crucially, documents that ALREADY contain irrlicht's own
// named hook in each state the installer has to reconcile.
package antigravity

import (
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/pkg/hookbeacon"
)

// propertyCases is how many generated documents each property runs against.
// Large enough that a shape appearing in a few percent of documents is seen
// many times; small enough that the whole file stays well under a second.
const propertyCases = 400

// TestMergeProperty_OverGeneratedUserDocuments drives install → verify →
// re-install → uninstall over generated documents and asserts the five
// properties that make this merge safe on a file irrlicht does not own.
func TestMergeProperty_OverGeneratedUserDocuments(t *testing.T) {
	rng := rand.New(rand.NewSource(17231122)) //nolint:gosec // deterministic corpus, not crypto
	shapes := map[string]int{}

	for i := 0; i < propertyCases; i++ {
		doc, rendered, shape := randomDocument(rng)
		shapes[shape]++

		t.Run(fmt.Sprintf("case%03d", i), func(t *testing.T) {
			path := antigravityConfigHome(t)
			writeDoc(t, path, rendered)

			// Each line below is one property, named for what it claims. The
			// bodies are helpers rather than inline blocks so this reads as
			// the list of obligations the merge owes — which is the point of
			// the file, and which a single 60-line body buried.
			before := assertGeneratorRoundTrips(t, path, doc, rendered)
			after := installOnce(t, path, rendered)

			assertForeignNamesPreserved(t, before, after)
			assertOurHookIsCorrect(t, before, after)
			assertInstallIsIdempotent(t, path)
			assertVerifyReportsClean(t, "immediately after a successful install")
			assertUninstallRestores(t, path, before, rendered)
		})
	}

	assertCorpusCoversEveryShape(t, shapes)
}

// assertGeneratorRoundTrips is the guard that runs BEFORE any property: the
// rendering the generator wrote must decode back to the document it built.
//
// It is not ceremony. renderJSONC serializes by hand — comments, key order,
// CRLF — so a generator bug produces a document that parses into something
// else, and every property below would then grade an input nobody intended
// while reporting green. Returns the decoded document the properties compare
// against.
func assertGeneratorRoundTrips(t *testing.T, path string, doc map[string]interface{}, rendered string) map[string]interface{} {
	t.Helper()
	before, err := hookjson.ReadSettings(path)
	if err != nil {
		t.Fatalf("the generator produced a document hookjson cannot read: %v\n%s", err, rendered)
	}
	if !reflect.DeepEqual(normalize(before), normalize(doc)) {
		t.Fatalf("the generator's rendering does not decode back to the document it built —\n"+
			"the generator is broken, and every property below would be grading the wrong input\n%s", rendered)
	}
	return before
}

// installOnce runs the install and returns the document it produced.
func installOnce(t *testing.T, path, rendered string) map[string]interface{} {
	t.Helper()
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v\n%s", err, rendered)
	}
	after, err := hookjson.ReadSettings(path)
	if err != nil {
		t.Fatalf("read after install: %v", err)
	}
	return after
}

// assertInstallIsIdempotent pins the property on the BYTES, not on the decoded
// document: hookjson has no "the bytes did not change, skip the write" branch,
// so an install that reported no change while rewriting the file would still be
// churning a user's config on every daemon start.
func assertInstallIsIdempotent(t *testing.T, path string) {
	t.Helper()
	firstBytes := mustRead(t, path)
	changed, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if changed {
		t.Errorf("second install reported a change:\n%s", firstBytes)
	}
	if got := mustRead(t, path); got != firstBytes {
		t.Errorf("second install rewrote the file:\n--- first ---\n%s\n--- second ---\n%s", firstBytes, got)
	}
}

// assertVerifyReportsClean is the read-only half agreeing with the write half.
// when names the moment being graded, so a failure says which of the two call
// sites (post-install, post-repair) disagreed.
func assertVerifyReportsClean(t *testing.T, when string) {
	t.Helper()
	status, err := VerifyHooksInstalled()
	if err != nil {
		t.Fatalf("verify %s: %v", when, err)
	}
	if len(status.Missing) > 0 || len(status.Stale) > 0 {
		t.Errorf("verify reports %+v %s", status, when)
	}
}

// assertUninstallRestores is the round-trip property: uninstalling from an
// installed document leaves exactly the user's original, minus irrlicht's own
// handler.
func assertUninstallRestores(t *testing.T, path string, before map[string]interface{}, rendered string) {
	t.Helper()
	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	final, err := hookjson.ReadSettings(path)
	if err != nil {
		t.Fatalf("read after uninstall: %v", err)
	}
	want := normalize(stripOurs(before))
	if got := normalize(final); !reflect.DeepEqual(got, want) {
		t.Errorf("uninstall did not restore the user's document.\nwant: %#v\ngot:  %#v\nseed doc:\n%s",
			want, got, rendered)
	}
}

// assertCorpusCoversEveryShape is the #1753 failure one level up: a generator
// that stopped producing one of the states the installer must reconcile would
// leave every property green while covering less. The weights are not trusted;
// the shapes actually produced are counted.
func assertCorpusCoversEveryShape(t *testing.T, shapes map[string]int) {
	t.Helper()
	for _, shape := range []string{"ours-absent", "ours-correct", "ours-stale", "ours-with-foreign"} {
		if shapes[shape] == 0 {
			t.Errorf("the generator produced no %q document in %d cases — the corpus has stopped "+
				"covering a state the installer has to reconcile", shape, propertyCases)
		}
	}
}

// TestMergeProperty_ConvergesAfterAnArbitraryMutation is the mutation half.
//
// A user's /hooks run, a hand-edit or another tool can change this file
// underneath a granted permission at any time. The obligation is not that
// every mutation is repaired — some are none of irrlicht's business — but that
// the installer always CONVERGES: one Ensure makes Verify clean, and a second
// Ensure writes nothing. A repair that never converges rewrites a user's
// config on every daemon start forever, which is the failure
// hookbeacon.IsCanonical's two deliberate "true" answers exist to prevent.
func TestMergeProperty_ConvergesAfterAnArbitraryMutation(t *testing.T) {
	rng := rand.New(rand.NewSource(984417231)) //nolint:gosec // deterministic corpus, not crypto
	mutated := 0

	for i := 0; i < propertyCases; i++ {
		_, rendered, _ := randomDocument(rng)

		t.Run(fmt.Sprintf("case%03d", i), func(t *testing.T) {
			path := antigravityConfigHome(t)
			writeDoc(t, path, rendered)
			installOnce(t, path, rendered)

			if !mutateInstalledDocument(t, rng, path) {
				return
			}
			mutated++
			assertConverges(t, path)
		})
	}

	if mutated == 0 {
		t.Fatalf("no document was mutated in %d cases — the mutation generator did nothing, and "+
			"every convergence assertion above graded an unmutated file", propertyCases)
	}
}

// mutateInstalledDocument applies one arbitrary structural change to the
// installed file and reports whether anything changed. A false means this case
// has nothing to grade — the caller returns rather than asserting convergence
// against an unmutated document.
func mutateInstalledDocument(t *testing.T, rng *rand.Rand, path string) bool {
	t.Helper()
	doc, err := hookjson.ReadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !mutate(rng, doc) {
		return false
	}
	if err := hookjson.WriteSettings(path, doc, atomicWriteFile); err != nil {
		t.Fatalf("write the mutated document: %v", err)
	}
	return true
}

// assertConverges is the whole obligation after a mutation: ONE Ensure makes
// Verify clean, and a second writes nothing.
//
// It deliberately does not require that every mutation be REPAIRED — some are
// none of irrlicht's business — only that the installer settles. A repair that
// never converges rewrites a user's config on every daemon start forever, which
// is the failure hookbeacon.IsCanonical's two deliberate "true" answers exist
// to prevent.
func assertConverges(t *testing.T, path string) {
	t.Helper()
	if _, err := EnsureHooksInstalled(); err != nil {
		// A mutation can legitimately produce the one document the installer
		// refuses (a non-object at our key). That is a refusal, not a
		// convergence failure — but it must be THAT refusal and not something
		// else, so the message is matched rather than the error tolerated.
		if strings.Contains(err.Error(), "refusing to overwrite") {
			return
		}
		t.Fatalf("install after mutation: %v", err)
	}

	assertVerifyReportsClean(t, "after one Ensure repaired the mutation")

	settled := mustRead(t, path)
	changed, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if changed || mustRead(t, path) != settled {
		t.Errorf("the installer did not converge — a second Ensure changed the file again")
	}
}

// --- assertions ---

// reporter is the seam the two preservation assertions below report through.
//
// *testing.T satisfies it, which is what the live property test passes; a
// recording implementation satisfies it too, which is what lets
// hookinstaller_mutations_test.go drive the same assertions against documents
// that are wrong in exactly one way and require them to REPORT. Without the
// seam, "these assertions catch a broken merge" would be a claim in a PR body
// that nothing re-runs — and docs/testing-philosophy.md asks for a committed
// fixture instead.
//
// Errorf, never Fatalf: Fatalf's runtime.Goexit is not something a recorder
// can survive, so a mutation that produced a differently-shaped document would
// take the corpus down instead of being reported by it.
type reporter interface {
	Helper()
	Errorf(format string, args ...interface{})
}

// assertForeignNamesPreserved is the central obligation: irrlicht does not own
// this document, so every top-level name that is not ours must survive an
// install byte-for-byte in meaning.
func assertForeignNamesPreserved(t reporter, before, after map[string]interface{}) {
	t.Helper()
	for name, want := range before {
		if name == hookName {
			continue
		}
		got, present := after[name]
		if !present {
			t.Errorf("the install deleted the named hook %q", name)
			continue
		}
		if !reflect.DeepEqual(normalizeValue(got), normalizeValue(want)) {
			t.Errorf("the install changed the named hook %q:\nwant %#v\ngot  %#v", name, want, got)
		}
	}
	for name := range after {
		if name == hookName {
			continue
		}
		if _, present := before[name]; !present {
			t.Errorf("the install invented a named hook %q", name)
		}
	}
}

// assertOurHookIsCorrect checks the half of the document irrlicht does own:
// exactly one canonical handler of ours under Stop, every foreign handler in
// that array preserved in order, and every other key under our name untouched.
func assertOurHookIsCorrect(t reporter, before, after map[string]interface{}) {
	t.Helper()
	ours, ok := after[hookName].(map[string]interface{})
	if !ok {
		t.Errorf("no %q object after install: %#v", hookName, after)
		return
	}

	handlers, ok := ours[HookEventStop].([]interface{})
	if !ok {
		t.Errorf("no %q array under %q after install: %#v", HookEventStop, hookName, ours)
		return
	}

	beforeOurs, _ := before[hookName].(map[string]interface{})
	mine, foreignAfter := partitionHandlers(handlers)
	_, foreignBefore := partitionHandlers(stopHandlersOf(beforeOurs))

	assertExactlyOneCanonicalHandlerOfOurs(t, mine)
	assertForeignHandlersPreserved(t, foreignBefore, foreignAfter)
	assertForeignKeysUnderOurNamePreserved(t, beforeOurs, ours)
}

// stopHandlersOf returns a named hook's Stop array, or nil for a spec that has
// none. Indexing a nil map is legal in Go, so a document with no named hook of
// ours flows through here as "no handlers" rather than needing a branch at each
// call site.
func stopHandlersOf(spec map[string]interface{}) []interface{} {
	arr, _ := spec[HookEventStop].([]interface{})
	return arr
}

// partitionHandlers splits one event's array into the handlers irrlicht owns
// and everything else, preserving order in both.
func partitionHandlers(handlers []interface{}) (mine []map[string]interface{}, foreign []interface{}) {
	for _, h := range handlers {
		entry, isObject := h.(map[string]interface{})
		if isObject && isOurs(entry) {
			mine = append(mine, entry)
			continue
		}
		foreign = append(foreign, h)
	}
	return mine, foreign
}

// assertExactlyOneCanonicalHandlerOfOurs pins the half irrlicht does own. Two
// of ours on one event means two beacon spawns per turn, forever; zero means
// the install did not land.
func assertExactlyOneCanonicalHandlerOfOurs(t reporter, mine []map[string]interface{}) {
	t.Helper()
	if len(mine) != 1 {
		t.Errorf("found %d handlers of ours under %s, want exactly 1", len(mine), HookEventStop)
	}
	for _, entry := range mine {
		if !entryIsCanonical(entry) {
			t.Errorf("our installed handler is not canonical: %#v", entry)
		}
	}
}

// assertForeignHandlersPreserved covers a handler somebody else put inside OUR
// named hook's event array. Upstream merges and runs multiple named hooks per
// event sequentially, so this is unusual rather than impossible — and it is
// still not irrlicht's to delete.
func assertForeignHandlersPreserved(t reporter, before, after []interface{}) {
	t.Helper()
	if !reflect.DeepEqual(normalizeValue(after), normalizeValue(before)) {
		t.Errorf("foreign handlers inside our own event array were changed:\nwant %#v\ngot  %#v",
			before, after)
	}
}

// assertForeignKeysUnderOurNamePreserved covers every key under our own name
// that is not the event we install — `enabled` above all, which is how a user
// turns our hook off through the /hooks TUI and which the installer must not
// reassert on the next daemon start.
func assertForeignKeysUnderOurNamePreserved(t reporter, before, after map[string]interface{}) {
	t.Helper()
	for key, want := range before {
		if key == HookEventStop {
			continue
		}
		got, present := after[key]
		if !present {
			t.Errorf("the install deleted %q from under our own name", key)
			continue
		}
		if !reflect.DeepEqual(normalizeValue(got), normalizeValue(want)) {
			t.Errorf("the install changed %q under our own name:\nwant %#v\ngot %#v", key, want, got)
		}
	}
}

// stripOurs is the expected result of uninstalling from a document: our
// sentinel-bearing handlers gone, an emptied event key gone, and our whole
// named hook gone once nothing of anyone's is left under it.
//
// It is a deliberate REIMPLEMENTATION of what UninstallHooks does, not a call
// into it. An oracle computed by the code under test cannot fail: the
// round-trip property would hold for any uninstall, including one that deleted
// the user's whole file. The duplication with dropOurHandlers is the price of
// the property meaning anything.
func stripOurs(doc map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for name, value := range doc {
		if name != hookName {
			out[name] = value
			continue
		}
		ours, isObject := value.(map[string]interface{})
		if !isObject {
			// Not a shape uninstall can model, so it is left exactly as it is.
			out[name] = value
			continue
		}
		if kept := stripOursFromNamedHook(ours); len(kept) > 0 {
			out[name] = kept
		}
	}
	return out
}

// stripOursFromNamedHook removes our handler from one named hook's events,
// dropping an event key whose array we emptied. An empty result means the whole
// named hook goes.
func stripOursFromNamedHook(ours map[string]interface{}) map[string]interface{} {
	kept := map[string]interface{}{}
	for key, v := range ours {
		if key != HookEventStop {
			kept[key] = v
			continue
		}
		arr, isArray := v.([]interface{})
		if !isArray {
			kept[key] = v
			continue
		}
		if survivors := withoutOurHandlers(arr); len(survivors) > 0 {
			kept[key] = survivors
		}
	}
	return kept
}

// withoutOurHandlers drops every handler carrying our sentinel, keeping the
// rest in order.
func withoutOurHandlers(arr []interface{}) []interface{} {
	var survivors []interface{}
	for _, h := range arr {
		entry, isEntry := h.(map[string]interface{})
		if isEntry && isOurs(entry) {
			continue
		}
		survivors = append(survivors, h)
	}
	return survivors
}

// --- generator ---

// eventStructure records which of the two per-event shapes an event takes.
// Getting this wrong is not a validation error upstream — the hook simply
// never loads — so the generator produces BOTH, and produces them for the
// right events.
var groupedEvents = map[string]bool{"PreToolUse": true, "PostToolUse": true}

var allEvents = []string{"PreToolUse", "PostToolUse", "PreInvocation", "PostInvocation", "Stop"}

// randomDocument builds one plausible user hooks.json, returns it alongside a
// JSONC rendering of it and a label naming which irrlicht-side shape it
// carries (so the corpus can assert it covers all of them).
func randomDocument(rng *rand.Rand) (map[string]interface{}, string, string) {
	doc := map[string]interface{}{}

	for i, n := 0, rng.Intn(4); i < n; i++ {
		doc[randomHookName(rng, i)] = randomNamedHook(rng)
	}

	shape := seedOurNamedHook(rng, doc)
	return doc, renderJSONC(rng, doc), shape
}

// seedOurNamedHook plants irrlicht's own named hook in one of the states the
// installer has to reconcile, and reports which.
func seedOurNamedHook(rng *rand.Rand, doc map[string]interface{}) string {
	switch rng.Intn(5) {
	case 0, 1:
		return "ours-absent"
	case 2:
		beacon, err := hookbeacon.InstalledCommand(AdapterName)
		if err != nil {
			return "ours-absent"
		}
		doc[hookName] = map[string]interface{}{
			HookEventStop: []interface{}{hookEntry(beacon)},
		}
		return "ours-correct"
	case 3:
		beacon, err := hookbeacon.Command("/nonexistent-irrlicht-property/irrlichd", AdapterName)
		if err != nil {
			return "ours-absent"
		}
		entry := hookEntry(beacon)
		if rng.Intn(2) == 0 {
			entry["timeout"] = float64(30) // a stale value from an older install
		}
		doc[hookName] = map[string]interface{}{
			HookEventStop: []interface{}{entry},
		}
		return "ours-stale"
	default:
		ours := map[string]interface{}{
			HookEventStop: []interface{}{randomHandler(rng)},
		}
		if rng.Intn(2) == 0 {
			ours["enabled"] = rng.Intn(2) == 0
		}
		if rng.Intn(2) == 0 {
			ours["PreInvocation"] = []interface{}{randomHandler(rng)}
		}
		doc[hookName] = ours
		return "ours-with-foreign"
	}
}

func randomHookName(rng *rand.Rand, i int) string {
	names := []string{"lint-checker", "safety-gate", "reminder", "audit", "fmt.on-save", "team_policy"}
	return fmt.Sprintf("%s-%d", names[rng.Intn(len(names))], i)
}

func randomNamedHook(rng *rand.Rand) map[string]interface{} {
	spec := map[string]interface{}{}
	if rng.Intn(3) == 0 {
		spec["enabled"] = rng.Intn(2) == 0
	}
	for _, event := range allEvents {
		if rng.Intn(3) != 0 {
			continue
		}
		spec[event] = randomEventArray(rng, event)
	}
	if len(spec) == 0 {
		spec["Stop"] = randomEventArray(rng, "Stop")
	}
	return spec
}

func randomEventArray(rng *rand.Rand, event string) []interface{} {
	var out []interface{}
	for i, n := 0, 1+rng.Intn(2); i < n; i++ {
		if groupedEvents[event] {
			group := map[string]interface{}{
				"hooks": []interface{}{randomHandler(rng)},
			}
			if rng.Intn(4) != 0 {
				group["matcher"] = []string{"*", "", "run_command", "browser_.*", `view_file|edit_file`}[rng.Intn(5)]
			}
			out = append(out, group)
			continue
		}
		out = append(out, randomHandler(rng))
	}
	return out
}

func randomHandler(rng *rand.Rand) map[string]interface{} {
	handler := map[string]interface{}{
		"command": []string{
			"./scripts/lint.sh", "~/bin/audit", "echo hi && exit 0",
			`printf '{"decision":"continue"}'`, "/usr/local/bin/check --json",
		}[rng.Intn(5)],
	}
	if rng.Intn(2) == 0 {
		handler["type"] = "command"
	}
	if rng.Intn(2) == 0 {
		handler["timeout"] = float64(1 + rng.Intn(60))
	}
	return handler
}

// renderJSONC serializes doc by hand so the corpus varies the things a
// hand-maintained or TUI-written file varies on and a plain json.Marshal never
// would: key order, indentation, comments, and line endings.
func renderJSONC(rng *rand.Rand, doc map[string]interface{}) string {
	var b strings.Builder
	if rng.Intn(3) == 0 {
		b.WriteString("// managed by /hooks — do not edit by hand\n")
	}
	b.WriteString("{\n")
	keys := sortedKeys(doc)
	if rng.Intn(2) == 0 {
		// reverse, so key ORDER varies rather than always being sorted
		for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
			keys[i], keys[j] = keys[j], keys[i]
		}
	}
	for i, key := range keys {
		if rng.Intn(4) == 0 {
			b.WriteString("  /* " + key + " */\n")
		}
		b.WriteString("  " + quote(key) + ": " + renderValue(rng, doc[key], 1))
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		if rng.Intn(5) == 0 {
			b.WriteString(" // kept")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")

	out := b.String()
	if rng.Intn(6) == 0 {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out
}

func renderValue(rng *rand.Rand, v interface{}, depth int) string {
	pad := strings.Repeat("  ", depth+1)
	closePad := strings.Repeat("  ", depth)
	switch value := v.(type) {
	case map[string]interface{}:
		if len(value) == 0 {
			return "{}"
		}
		var parts []string
		for _, key := range sortedKeys(value) {
			parts = append(parts, pad+quote(key)+": "+renderValue(rng, value[key], depth+1))
		}
		return "{\n" + strings.Join(parts, ",\n") + "\n" + closePad + "}"
	case []interface{}:
		if len(value) == 0 {
			return "[]"
		}
		var parts []string
		for _, elem := range value {
			parts = append(parts, pad+renderValue(rng, elem, depth+1))
		}
		return "[\n" + strings.Join(parts, ",\n") + "\n" + closePad + "]"
	case string:
		return quote(value)
	case bool:
		if value {
			return "true"
		}
		return "false"
	case float64:
		return fmt.Sprintf("%g", value)
	case int:
		return fmt.Sprintf("%d", value)
	default:
		panic(fmt.Sprintf("renderValue: unhandled %T", v))
	}
}

// mutate applies one arbitrary structural change to a decoded document,
// reporting whether it changed anything. It deliberately reaches inside our
// own named hook as well as the user's: a /hooks run can rewrite either.
func mutate(rng *rand.Rand, doc map[string]interface{}) bool {
	keys := sortedKeys(doc)
	if len(keys) == 0 {
		return false
	}
	key := keys[rng.Intn(len(keys))]
	return documentMutations[rng.Intn(len(documentMutations))].apply(rng, doc, key)
}

// documentMutation is one arbitrary structural change a /hooks run, a
// hand-edit or another tool could make to this file.
//
// A table of named functions rather than a switch, because the value of this
// generator is in WHICH changes it makes: a reader has to be able to see the
// list, and an unnamed `case 3:` inside a 50-line switch is a change nobody can
// audit for coverage.
type documentMutation struct {
	name string
	// apply mutates doc in place and reports whether it changed anything. A
	// false is normal — a mutation can find no site to act on — and the caller
	// skips that case rather than grading an unmutated document.
	apply func(rng *rand.Rand, doc map[string]interface{}, key string) bool
}

var documentMutations = []documentMutation{
	{"delete a whole named hook", deleteNamedHook},
	{"replace a named hook with something unmodellable", clobberNamedHook},
	{"add a named hook nobody has seen", addUnknownNamedHook},
	{"empty a named hook's event array", emptyAnEventArray},
	{"scramble a handler's command", scrambleAHandlerCommand},
	{"toggle enabled", toggleEnabled},
}

// deleteNamedHook is the sync-by-omission clobber shape #1372 documents for
// gemini-cli's settings writer.
func deleteNamedHook(_ *rand.Rand, doc map[string]interface{}, key string) bool {
	delete(doc, key)
	return true
}

// clobberNamedHook replaces a named hook with a value the installer cannot
// model — which, at our own key, is the one document it refuses.
func clobberNamedHook(_ *rand.Rand, doc map[string]interface{}, key string) bool {
	doc[key] = "clobbered by another tool"
	return true
}

func addUnknownNamedHook(_ *rand.Rand, doc map[string]interface{}, _ string) bool {
	doc["newcomer"] = map[string]interface{}{
		"Stop": []interface{}{map[string]interface{}{"command": "./new.sh"}},
	}
	return true
}

func emptyAnEventArray(_ *rand.Rand, doc map[string]interface{}, key string) bool {
	spec, ok := doc[key].(map[string]interface{})
	if !ok {
		return false
	}
	for _, event := range allEvents {
		if _, present := spec[event]; present {
			spec[event] = []interface{}{}
			return true
		}
	}
	return false
}

// scrambleAHandlerCommand is what makes OUR entry stale when it lands on our
// own named hook, and an ordinary user edit when it lands elsewhere.
func scrambleAHandlerCommand(_ *rand.Rand, doc map[string]interface{}, key string) bool {
	spec, ok := doc[key].(map[string]interface{})
	if !ok {
		return false
	}
	for _, event := range allEvents {
		arr, isArray := spec[event].([]interface{})
		if !isArray || len(arr) == 0 {
			continue
		}
		entry, isObject := arr[0].(map[string]interface{})
		if !isObject {
			continue
		}
		entry["command"] = "./tampered.sh"
		return true
	}
	return false
}

func toggleEnabled(rng *rand.Rand, doc map[string]interface{}, key string) bool {
	spec, ok := doc[key].(map[string]interface{})
	if !ok {
		return false
	}
	spec["enabled"] = rng.Intn(2) == 0
	return true
}

// --- small helpers ---

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// normalize / normalizeValue put a decoded document into one comparable shape:
// ints written by this process and float64s read back through encoding/json
// must compare equal, and a nil slice must compare equal to an empty one.
func normalize(m map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range m {
		out[k] = normalizeValue(v)
	}
	return out
}

func normalizeValue(v interface{}) interface{} {
	switch value := v.(type) {
	case map[string]interface{}:
		return normalize(value)
	case []interface{}:
		out := make([]interface{}, 0, len(value))
		for _, elem := range value {
			out = append(out, normalizeValue(elem))
		}
		return out
	case int:
		return float64(value)
	default:
		return v
	}
}
