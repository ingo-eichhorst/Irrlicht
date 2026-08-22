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

			before, err := hookjson.ReadSettings(path)
			if err != nil {
				t.Fatalf("the generator produced a document hookjson cannot read: %v\n%s", err, rendered)
			}
			if !reflect.DeepEqual(normalize(before), normalize(doc)) {
				t.Fatalf("the generator's rendering does not decode back to the document it built —\n"+
					"the generator is broken, and every property below would be grading the wrong input\n%s", rendered)
			}

			// --- install ---
			if _, err := EnsureHooksInstalled(); err != nil {
				t.Fatalf("install: %v\n%s", err, rendered)
			}
			after, err := hookjson.ReadSettings(path)
			if err != nil {
				t.Fatalf("read after install: %v", err)
			}

			assertForeignNamesPreserved(t, before, after)
			assertOurHookIsCorrect(t, before, after)

			// --- idempotence, on the bytes ---
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

			// --- verify agrees ---
			status, err := VerifyHooksInstalled()
			if err != nil {
				t.Fatalf("verify after install: %v", err)
			}
			if len(status.Missing) > 0 || len(status.Stale) > 0 {
				t.Errorf("verify reports %+v immediately after a successful install", status)
			}

			// --- uninstall round-trips to the original, minus us ---
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
		})
	}

	// A generator that stopped producing one of the shapes it exists to
	// produce would leave every property above green while covering less —
	// which is the #1753 failure, one level up. Assert the corpus actually
	// contains each shape rather than trusting the weights.
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
			if _, err := EnsureHooksInstalled(); err != nil {
				t.Fatalf("install: %v", err)
			}

			doc, err := hookjson.ReadSettings(path)
			if err != nil {
				t.Fatal(err)
			}
			if !mutate(rng, doc) {
				return
			}
			mutated++
			if err := hookjson.WriteSettings(path, doc, atomicWriteFile); err != nil {
				t.Fatalf("write the mutated document: %v", err)
			}

			if _, err := EnsureHooksInstalled(); err != nil {
				// A mutation can legitimately produce the one document the
				// installer refuses (a non-object at our key). That is a
				// refusal, not a convergence failure — but it must be THAT
				// refusal and not something else.
				if strings.Contains(err.Error(), "refusing to overwrite") {
					return
				}
				t.Fatalf("install after mutation: %v", err)
			}

			status, err := VerifyHooksInstalled()
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if len(status.Missing) > 0 || len(status.Stale) > 0 {
				t.Errorf("one Ensure did not repair the mutation: verify still reports %+v", status)
			}

			settled := mustRead(t, path)
			changed, err := EnsureHooksInstalled()
			if err != nil {
				t.Fatal(err)
			}
			if changed || mustRead(t, path) != settled {
				t.Errorf("the installer did not converge — a second Ensure changed the file again")
			}
		})
	}

	if mutated == 0 {
		t.Fatalf("no document was mutated in %d cases — the mutation generator did nothing, and "+
			"every convergence assertion above graded an unmutated file", propertyCases)
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
	mine := 0
	var foreignAfter []interface{}
	for _, h := range handlers {
		entry, isObject := h.(map[string]interface{})
		if isObject && isOurs(entry) {
			mine++
			if !entryIsCanonical(entry) {
				t.Errorf("our installed handler is not canonical: %#v", entry)
			}
			continue
		}
		foreignAfter = append(foreignAfter, h)
	}
	if mine != 1 {
		t.Errorf("found %d handlers of ours under %s, want exactly 1", mine, HookEventStop)
	}

	// Foreign handlers in our own event array, and every other key under our
	// name, are somebody else's content and must survive.
	beforeOurs, _ := before[hookName].(map[string]interface{})
	var foreignBefore []interface{}
	if arr, ok := beforeOurs[HookEventStop].([]interface{}); ok {
		for _, h := range arr {
			entry, isObject := h.(map[string]interface{})
			if isObject && isOurs(entry) {
				continue
			}
			foreignBefore = append(foreignBefore, h)
		}
	}
	if !reflect.DeepEqual(normalizeValue(foreignAfter), normalizeValue(foreignBefore)) {
		t.Errorf("foreign handlers inside our own event array were changed:\nwant %#v\ngot  %#v",
			foreignBefore, foreignAfter)
	}
	for key, want := range beforeOurs {
		if key == HookEventStop {
			continue
		}
		got, present := ours[key]
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
func stripOurs(doc map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for name, value := range doc {
		if name != hookName {
			out[name] = value
			continue
		}
		ours, isObject := value.(map[string]interface{})
		if !isObject {
			out[name] = value
			continue
		}
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
			var survivors []interface{}
			for _, h := range arr {
				entry, isEntry := h.(map[string]interface{})
				if isEntry && isOurs(entry) {
					continue
				}
				survivors = append(survivors, h)
			}
			if len(survivors) > 0 {
				kept[key] = survivors
			}
		}
		if len(kept) > 0 {
			out[name] = kept
		}
	}
	return out
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

	switch rng.Intn(6) {
	case 0: // delete a whole named hook — the sync-by-omission clobber shape
		delete(doc, key)
		return true
	case 1: // replace a named hook with something unmodellable
		doc[key] = "clobbered by another tool"
		return true
	case 2: // add a named hook nobody has seen
		doc["newcomer"] = map[string]interface{}{"Stop": []interface{}{map[string]interface{}{"command": "./new.sh"}}}
		return true
	case 3: // empty a named hook's event arrays
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
	case 4: // scramble one handler's command, which is what makes ours stale
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
	default: // toggle enabled
		spec, ok := doc[key].(map[string]interface{})
		if !ok {
			return false
		}
		spec["enabled"] = rng.Intn(2) == 0
		return true
	}
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
