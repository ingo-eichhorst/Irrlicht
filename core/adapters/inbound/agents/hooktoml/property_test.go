package hooktoml

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// property_test.go is the property test AGENTS.md requires for any code
// that emits bytes from a structural diff — the shape to copy is
// hookjson's TestSplice_PropertyRandomMutations
// (core/adapters/inbound/agents/hookjson/jsonc_test.go): a fixed seed so a
// failure reproduces, the document AND the mutation printed on failure, and
// a committed iteration count small enough to stay in the suite.
//
// Two generators, one per structural shape this package edits — an
// array-of-tables and a top-level scalar — because hookjson's own history
// is the warning: a generator that only varies the axis its author first
// thought of is the same vacuous green wearing a different hat, and here
// the two shapes do not share a code path at all (see hooktoml.go).
//
// --- TestEnsureAndUninstall_PropertyRandomMutations ([[hooks]]) ---
//
// Structural axes this generator varies:
//   - the NUMBER of pre-existing, unrelated [[hooks]] blocks (0-3), each
//     with its own random name/type/command and comment placement;
//   - unrelated [table] sections interspersed AMONG the generated blocks,
//     not only trailing at the end;
//   - whether irrlicht's OWN sentinel-bearing block is ABSENT, present and
//     CANONICAL, or present and STALE (a different command) before the
//     operation runs — the three states EnsureInstalled must tell apart,
//     and the one axis that determines which of the properties below can
//     even fire;
//   - a standalone comment line and a run of 0-2 blank lines as glue
//     BEFORE, BETWEEN and AFTER the generated blocks (hookjson's first
//     draft varied only the tail — see its own postmortem in AGENTS.md);
//   - presence or absence of a final trailing newline;
//   - which operation runs: EnsureInstalled or Uninstall;
//   - MULTI-LINE ARRAYS (#1753), the construct the scanner used to refuse
//     outright and now has to walk THROUGH. This is the axis the paragraph
//     in docs/testing-philosophy.md is about: without it the generator
//     produces only documents the pre-#1753 scanner already modelled, and a
//     widening whose whole content is "one more construct" would be graded
//     by a generator that never emits it. Varied on five sub-axes of its
//     own, because a multi-line array is not one shape:
//     its ELEMENT COUNT (0-3, including the empty `[\n]`); its POSITION
//     (inside a generated [[hooks]] block, inside an unrelated [table],
//     and — the case the real defect lived in — in the document PREAMBLE
//     above every header, which is the only region topLevelKeyLine scans);
//     NESTING (an element that is itself a multi-line array); DECORATION
//     (a trailing comment on an element line, and a comment-only line
//     inside the array, neither of which may be mistaken for a section-
//     leading comment); and ADVERSARIAL ELEMENT TEXT — quoted strings
//     spelling `[[hooks]]`, `[table]`, `#`, `]` and `"""`-free bracket
//     soup, each of which reads as a header, a comment or a delimiter to
//     any scanner that classifies continuation lines — including, as the
//     LAST element and written without a trailing comma, a bare nested
//     array (`[1, 2]`, `[[1], [2]]`) whose rendered line is byte-for-byte
//     what isTableHeaderLine and isArrayHeaderLine match. That last shape
//     is the only one of the pool that a quoted string cannot stand in
//     for, and it is what the mutation run showed was missing.
//
// Which properties survive which mutation:
//   - "every UNRELATED block's bytes survive verbatim" holds for BOTH
//     operations and every state, because neither operation ever computes
//     an edit inside a block it does not own — checked by substring search
//     for each unrelated snippet's own rendering.
//   - "the pre-existing SENTINEL block's bytes (comment included) survive"
//     is true ONLY when EnsureInstalled finds it already canonical (a
//     true no-op). It is FALSE the moment the block is rewritten (stale ->
//     canonical) or removed (Uninstall): a generated sentinel block
//     therefore always carries its own random comment specifically so the
//     test can assert that comment is GONE after a rewrite or a removal,
//     not merely missing from the "must survive" list by omission — the
//     exact distinction AGENTS.md calls out ("every comment is preserved"
//     is false for a deletion).
//   - "the operation is idempotent" holds for every state/op combination:
//     running the SAME operation again on the result reports no further
//     modification and produces byte-identical output.
//   - "no document this generator emits is REFUSED" is a property in its own
//     right (fail("unexpected refusal")), and it is the one the multi-line-
//     array axis exists to exercise: before #1753 every iteration carrying
//     one failed there. It survives every mutation because the generator
//     emits only constructs the scanner is claimed to model — the ones it
//     still refuses are pinned separately, by name and line, in
//     TestScannerRefusals_Corpus, since a generator that emitted them would
//     be asserting the opposite property in the same loop.
//   - "an unrelated block's bytes survive verbatim" is the property a
//     multi-line array is most likely to break, and it holds for both
//     operations: a scanner that mis-attributed an array's closing `]` line
//     to the NEXT section would move that byte out of the block it belongs
//     to, and the substring check catches it.
func TestEnsureAndUninstall_PropertyRandomMutations(t *testing.T) {
	const iterations = 2000
	const seed = int64(17180001)
	rng := rand.New(rand.NewSource(seed))
	arraysSeen, preambleArraysSeen := 0, 0

	for i := 0; i < iterations; i++ {
		unrelated, ourState, ourComment, doc := genHooksDoc(rng)
		if total, preamble := countGeneratedArrays(doc); total > 0 {
			arraysSeen++
			if preamble > 0 {
				preambleArraysSeen++
			}
		}
		op := "ensure"
		if rng.Intn(2) == 0 {
			op = "uninstall"
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "hooks.toml")
		if len(doc) > 0 {
			if err := os.WriteFile(path, doc, 0o600); err != nil {
				t.Fatalf("iter %d (seed %d): seed write: %v", i, seed, err)
			}
		}
		cfg := testConfig(t, path, canonicalTestCommand)

		fail := func(format string, args ...interface{}) {
			t.Helper()
			msg := fmt.Sprintf(format, args...)
			t.Fatalf("iteration %d (seed %d), op=%s ourState=%s:\ndocument:\n%s\n%s",
				i, seed, op, ourState, doc, msg)
		}

		var modified bool
		var err error
		switch op {
		case "ensure":
			modified, err = EnsureInstalled(cfg)
		case "uninstall":
			modified, err = Uninstall(cfg)
		}
		if err != nil {
			fail("unexpected refusal: %v", err)
		}

		result, _ := os.ReadFile(path)

		// Property: every unrelated block survives verbatim, always.
		//
		// Trimmed of ITS OWN trailing newline before the check: the
		// generator itself may drop the whole document's final '\n' (one
		// of the axes it varies), and when the last slot happens to be an
		// unrelated block, that single byte is the document's own, not
		// something hooktoml rewrote — asserting it survives would be
		// testing the generator's own cosmetic trim, not hooktoml.
		for _, snip := range unrelated {
			if !strings.Contains(string(result), strings.TrimSuffix(snip, "\n")) {
				fail("an unrelated block did not survive verbatim:\n%s", snip)
			}
		}

		// Property: whether our own block's bytes/comment survive depends
		// on state x op, per the doc comment's state table.
		ourBytesShouldSurvive := op == "ensure" && ourState == "canonical"
		if ourComment != "" {
			commentSurvives := strings.Contains(string(result), ourComment)
			if ourBytesShouldSurvive && !commentSurvives {
				fail("a true no-op (ensure over an already-canonical entry) lost the existing comment")
			}
			if !ourBytesShouldSurvive && ourState != "absent" && commentSurvives {
				fail("op=%s over ourState=%s should have removed the pre-existing block's own comment, but it survived", op, ourState)
			}
		}

		// Property: modified reporting matches the state table.
		wantModified := true
		if op == "ensure" && ourState == "canonical" {
			wantModified = false
		}
		if op == "uninstall" && ourState == "absent" {
			wantModified = false
		}
		if modified != wantModified {
			fail("modified=%v, want %v for op=%s ourState=%s", modified, wantModified, op, ourState)
		}

		// Property: after EnsureInstalled, Inspect reports present+canonical;
		// after Uninstall, Inspect reports NOT present.
		present, canonical, err := Inspect(cfg)
		if err != nil {
			fail("Inspect after %s: %v", op, err)
		}
		switch op {
		case "ensure":
			if !present || !canonical {
				fail("Inspect after EnsureInstalled: present=%v canonical=%v, want true/true", present, canonical)
			}
		case "uninstall":
			if present {
				fail("Inspect after Uninstall: present=%v, want false", present)
			}
		}

		// Property: idempotent. Re-running the SAME op changes nothing.
		var modified2 bool
		switch op {
		case "ensure":
			modified2, err = EnsureInstalled(cfg)
		case "uninstall":
			modified2, err = Uninstall(cfg)
		}
		if err != nil {
			fail("second %s: %v", op, err)
		}
		if modified2 {
			fail("second %s reported a modification — not idempotent", op)
		}
		result2, _ := os.ReadFile(path)
		if string(result) != string(result2) {
			fail("second %s changed bytes:\nfirst:\n%s\nsecond:\n%s", op, result, result2)
		}
	}

	assertArrayAxisWasExercised(t, iterations, arraysSeen, preambleArraysSeen)
}

// assertArrayAxisWasExercised fails when the generator produced too few
// documents carrying the #1753 construct for the run to have graded it. The
// floors are a tenth and a fiftieth of the iteration count — far below what
// the committed seeds produce, so this fires on a generator that BROKE, not
// on ordinary variance.
func assertArrayAxisWasExercised(t *testing.T, iterations, arrays, preambleArrays int) {
	t.Helper()
	t.Logf("multi-line-array axis: %d/%d documents carried one, %d of those in the preamble",
		arrays, iterations, preambleArrays)
	if arrays < iterations/10 {
		t.Errorf("only %d of %d generated documents carried a multi-line array — "+
			"the #1753 axis is not being exercised", arrays, iterations)
	}
	if preambleArrays < iterations/50 {
		t.Errorf("only %d of %d generated documents carried a multi-line array in the PREAMBLE — "+
			"the region topLevelKeyLine walks, and where #1753's own input had one",
			preambleArrays, iterations)
	}
}

// multiLineArrayOpener is what a generated multi-line array looks like in a
// rendered document: an assignment whose line ends at the open bracket, or at
// a comment after it. countGeneratedArrays is the VACUITY GUARD both property
// tests below run: a generator that silently stopped emitting the construct
// its whole reason for existing is the construct — a one-character typo in a
// rng.Intn threshold does it — would leave both tests green while covering
// exactly what they covered before #1753. Absence of a finding and inability
// to look must not produce the same output.
var multiLineArrayOpener = regexp.MustCompile(`(?m)^[ \t]*[A-Za-z0-9_.-]*[ \t]*=[ \t]*\[[ \t]*(#.*)?$`)

// countGeneratedArrays reports how many multi-line arrays doc opens in total
// and how many of those sit in the PREAMBLE, above every [table] or [[array]]
// header — the region only topLevelKeyLine walks, and where #1753's real
// input had its own.
func countGeneratedArrays(doc []byte) (total, preamble int) {
	firstHeader := len(doc)
	for _, line := range bytes.SplitAfter(doc, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) >= 2 && trimmed[0] == '[' && bytes.HasSuffix(trimmed, []byte("]")) {
			firstHeader = bytes.Index(doc, line)
			break
		}
	}
	for _, loc := range multiLineArrayOpener.FindAllIndex(doc, -1) {
		total++
		if loc[0] < firstHeader {
			preamble++
		}
	}
	return total, preamble
}

// canonicalTestCommand is the one canonical beacon command every hooktoml
// test in this package renders and checks against.
const canonicalTestCommand = "irrlichd hook-post mistral-vibe >/dev/null || true"

const staleTestCommand = "/old/irrlichd hook-post mistral-vibe >/dev/null || true"

func genHooksDoc(rng *rand.Rand) (unrelated []string, ourState, ourComment string, doc []byte) {
	n := rng.Intn(4)
	unrelated = make([]string, n)
	for i := range unrelated {
		if rng.Intn(2) == 0 {
			unrelated[i] = randomUnrelatedHookBlock(rng, i)
		} else {
			unrelated[i] = randomUnrelatedTable(rng, i)
		}
	}

	states := []string{"absent", "canonical", "stale"}
	ourState = states[rng.Intn(len(states))]
	var ourBlock string
	if ourState != "absent" {
		command := canonicalTestCommand
		if ourState == "stale" {
			command = staleTestCommand
		}
		if rng.Intn(2) == 0 {
			ourComment = fmt.Sprintf("# installed by an older irrlicht build %d", rng.Intn(100000))
		}
		var b strings.Builder
		if ourComment != "" {
			b.WriteString(ourComment)
			b.WriteString("\n")
		}
		b.Write(testEntry(command))
		ourBlock = b.String()
	}

	slots := append([]string{}, unrelated...)
	if ourBlock != "" {
		pos := rng.Intn(len(slots) + 1)
		slots = append(slots[:pos:pos], append([]string{ourBlock}, slots[pos:]...)...)
	}

	var out strings.Builder
	// A preamble ABOVE every header: the only region topLevelKeyLine scans,
	// and where the real #1753 defect lived (vibe's own applied_migrations
	// sits at line 44 of a 392-line config, before the first header).
	if rng.Intn(2) == 0 {
		pre := randomMultiLineArray(rng, fmt.Sprintf("preamble_arr_%d", rng.Intn(100000)), 1)
		unrelated = append(unrelated, strings.TrimSuffix(pre, "\n"))
		out.WriteString(pre)
	}
	out.WriteString(randomGlue(rng))
	for i, s := range slots {
		out.WriteString(s)
		if i != len(slots)-1 {
			out.WriteString(randomGlue(rng))
		}
	}
	rendered := out.String()
	if rng.Intn(5) == 0 {
		rendered = strings.TrimSuffix(rendered, "\n")
	}
	return unrelated, ourState, ourComment, []byte(rendered)
}

// randomMultiLineArray renders one multi-line array assignment named key,
// varying every sub-axis the doc comment above lists. Returned WITH its
// trailing newline; the caller decides where it goes.
//
// Element text is drawn from a deliberately adversarial pool: every entry is
// a legal TOML string whose CONTENT looks like something the line scanner
// classifies — a table header, an array-of-tables header, a comment, a bare
// closing bracket. A scanner that classified continuation lines would read
// them as structure and either split a section in the wrong place or refuse.
func randomMultiLineArray(rng *rand.Rand, key string, depth int) string {
	adversarial := []string{
		"[[hooks]]",
		"[session_logging]",
		"# not a comment",
		"]",
		"[",
		"a \\\" quoted ] bracket",
		"plain-value",
		"/Users/x/.vibe/logs/session/*",
	}
	// headerShaped elements are the ones that matter most, and they are
	// deliberately NOT strings: a QUOTED "[[hooks]]" starts with a quote and
	// is header-shaped to nobody. A bare nested array written without a
	// trailing comma — legal TOML for the last element — renders a line whose
	// trimmed text is `[1, 2]` or `[[1], [2]]`, which is EXACTLY what
	// isTableHeaderLine and isArrayHeaderLine match. This distinction was
	// found by mutation: with only the quoted pool above, deleting the
	// scanner's "classify nothing inside an array" guard left the whole
	// suite green.
	headerShaped := []string{"[1, 2]", "[[1], [2]]", "[\"x\"]"}
	var b strings.Builder
	b.WriteString(key + " = [")
	if rng.Intn(4) == 0 {
		b.WriteString("  # why this array exists")
	}
	b.WriteString("\n")
	n := rng.Intn(4)
	for i := 0; i < n; i++ {
		if rng.Intn(5) == 0 {
			b.WriteString("    # a comment line INSIDE the array\n")
		}
		if depth > 0 && rng.Intn(4) == 0 {
			// A nested multi-line array element, indented one level.
			nested := randomMultiLineArray(rng, "", depth-1)
			nested = strings.TrimPrefix(nested, " = ")
			for _, line := range strings.Split(strings.TrimSuffix(nested, "\n"), "\n") {
				b.WriteString("    " + line + "\n")
			}
			continue
		}
		if i == n-1 && rng.Intn(2) == 0 {
			// Last element, no trailing comma, header-shaped.
			b.WriteString("    " + headerShaped[rng.Intn(len(headerShaped))] + "\n")
			continue
		}
		b.WriteString("    " + Quote(adversarial[rng.Intn(len(adversarial))]) + ",")
		if rng.Intn(5) == 0 {
			b.WriteString("  # trailing comment on an element")
		}
		b.WriteString("\n")
	}
	b.WriteString("]\n")
	return b.String()
}

func randomUnrelatedHookBlock(rng *rand.Rand, i int) string {
	names := []string{"lint", "deny-rm-rf", "notify", "audit-log", "custom-hook"}
	types := []string{"post_agent_turn", "before_tool", "after_tool"}
	name := fmt.Sprintf("%s-%d-%d", names[rng.Intn(len(names))], i, rng.Intn(100000))
	typ := types[rng.Intn(len(types))]
	var b strings.Builder
	if rng.Intn(3) == 0 {
		b.WriteString("# a user comment above their own block\n")
	}
	b.WriteString("[[hooks]]\n")
	b.WriteString("name = " + Quote(name))
	if rng.Intn(2) == 0 {
		b.WriteString("  # trailing comment on name")
	}
	b.WriteString("\n")
	b.WriteString("type = " + Quote(typ) + "\n")
	b.WriteString("command = " + Quote("do-something-"+strconv.Itoa(rng.Intn(100000))) + "\n")
	if rng.Intn(2) == 0 {
		b.WriteString("timeout = " + strconv.Itoa(10+rng.Intn(50)) + ".0\n")
	}
	if rng.Intn(2) == 0 {
		b.WriteString(randomMultiLineArray(rng, "match_any", 1))
	}
	return b.String()
}

func randomUnrelatedTable(rng *rand.Rand, i int) string {
	names := []string{"session_logging", "some_table", "experiment_overrides"}
	name := fmt.Sprintf("%s_%d", names[rng.Intn(len(names))], i)
	var b strings.Builder
	b.WriteString("[" + name + "]\n")
	b.WriteString("save_dir = " + Quote(fmt.Sprintf("/tmp/x%d", rng.Intn(100000))) + "\n")
	if rng.Intn(2) == 0 {
		b.WriteString(randomMultiLineArray(rng, "allowlist", 1))
	}
	return b.String()
}

func randomGlue(rng *rand.Rand) string {
	var b strings.Builder
	for i := 0; i < rng.Intn(3); i++ {
		b.WriteString("\n")
	}
	if rng.Intn(4) == 0 {
		b.WriteString("# a standalone comment between blocks\n")
	}
	return b.String()
}

// --- TestSetTopLevelBool_PropertyRandomMutations (top-level scalar) ---
//
// Structural axes: the flag key ABSENT, present-true, or present-false in a
// random preamble carrying 0-3 unrelated top-level assignments — each of
// which is EITHER a scalar (with an optional trailing comment) OR a
// multi-line array (#1753), varied on the five sub-axes randomMultiLineArray
// lists — plus an optional further array AFTER the flag; 0-2 standalone
// comment/blank lines; a random SUFFIX of 0-2 unrelated [table] sections
// after the preamble, themselves optionally carrying a multi-line array
// (present precisely to prove the scanner never inserts INSIDE a table);
// presence or absence of a trailing newline; and which operation runs —
// EnsureBoolTrue or ClearBoolIfPresent.
//
// The array axis matters most on THIS generator, not the sibling one: the
// top-level scan is a second, independent walk over the bytes
// (topLevelKeyLine, not scanDocument), it is the walk EnsureBoolTrue takes,
// and it is where #1753's own defect fired — vibe's config opens a
// multi-line array 4 lines before its first header, so the scan refused
// without ever reaching one.
//
// Which properties survive which mutation: no document this generator emits
// is refused (the property the array axis exists for — every iteration
// carrying an array failed here before #1753), and every unrelated scalar
// line, every unrelated ARRAY and every table section survives verbatim
// under BOTH operations and every starting state — there is no analogue of hookjson's "the deleted
// subtree's comments go with it" here, because a scalar set/clear never
// deletes a LINE, only rewrites the one line it targets (or adds one), so
// nothing this generator places is ever a casualty the way an unrelated
// [[hooks]] block's own comment is in the sibling property test above.
func TestSetTopLevelBool_PropertyRandomMutations(t *testing.T) {
	const iterations = 2000
	const seed = int64(17180002)
	rng := rand.New(rand.NewSource(seed))
	const key = "enable_experimental_hooks"

	arraysSeen, preambleArraysSeen := 0, 0
	for i := 0; i < iterations; i++ {
		unrelated, startState, doc := genScalarDoc(rng, key)
		if total, preamble := countGeneratedArrays(doc); total > 0 {
			arraysSeen++
			if preamble > 0 {
				preambleArraysSeen++
			}
		}
		op := "ensure"
		if rng.Intn(2) == 0 {
			op = "clear"
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "config.toml")
		if len(doc) > 0 {
			if err := os.WriteFile(path, doc, 0o600); err != nil {
				t.Fatalf("iter %d (seed %d): seed write: %v", i, seed, err)
			}
		}

		fail := func(format string, args ...interface{}) {
			t.Helper()
			msg := fmt.Sprintf(format, args...)
			t.Fatalf("iteration %d (seed %d), op=%s startState=%s:\ndocument:\n%s\n%s",
				i, seed, op, startState, doc, msg)
		}

		var modified bool
		var err error
		switch op {
		case "ensure":
			modified, err = EnsureBoolTrue(path, key, AtomicWriteFile)
		case "clear":
			modified, err = ClearBoolIfPresent(path, key, AtomicWriteFile)
		}
		if err != nil {
			fail("unexpected refusal: %v", err)
		}

		result, _ := os.ReadFile(path)
		for _, snip := range unrelated {
			if !strings.Contains(string(result), snip) {
				fail("unrelated content did not survive verbatim:\n%s", snip)
			}
		}

		wantModified := (op == "ensure" && startState != "true") || (op == "clear" && startState == "true")
		if modified != wantModified {
			fail("modified=%v, want %v", modified, wantModified)
		}

		value, found, err := TopLevelBool(path, key)
		if err != nil {
			fail("TopLevelBool after %s: %v", op, err)
		}
		switch op {
		case "ensure":
			if !found || !value {
				fail("after EnsureBoolTrue: found=%v value=%v, want true/true", found, value)
			}
		case "clear":
			if startState == "absent" {
				if found {
					fail("ClearBoolIfPresent created an absent key")
				}
			} else if !found || value {
				fail("after ClearBoolIfPresent: found=%v value=%v, want true/false", found, value)
			}
		}

		var modified2 bool
		switch op {
		case "ensure":
			modified2, err = EnsureBoolTrue(path, key, AtomicWriteFile)
		case "clear":
			modified2, err = ClearBoolIfPresent(path, key, AtomicWriteFile)
		}
		if err != nil {
			fail("second %s: %v", op, err)
		}
		if modified2 {
			fail("second %s reported a modification — not idempotent", op)
		}
		result2, _ := os.ReadFile(path)
		if string(result) != string(result2) {
			fail("second %s changed bytes", op)
		}
	}

	assertArrayAxisWasExercised(t, iterations, arraysSeen, preambleArraysSeen)
}

func genScalarDoc(rng *rand.Rand, key string) (unrelated []string, startState string, doc []byte) {
	nScalars := rng.Intn(4)
	var preamble strings.Builder
	for i := 0; i < nScalars; i++ {
		var line string
		if rng.Intn(2) == 0 {
			// A TOP-LEVEL multi-line array: the exact construct that made
			// topLevelKeyLine refuse a config vibe itself wrote (#1753), and
			// the one this scanner has to walk THROUGH to reach either the
			// flag or the first header.
			line = randomMultiLineArray(rng, fmt.Sprintf("some_arr_%d", i), 1)
		} else {
			line = fmt.Sprintf("some_key_%d = %s", i, Quote(fmt.Sprintf("v%d", rng.Intn(100000))))
			if rng.Intn(2) == 0 {
				line += "  # a comment"
			}
			line += "\n"
		}
		unrelated = append(unrelated, strings.TrimRight(line, "\n"))
		preamble.WriteString(line)
		if rng.Intn(3) == 0 {
			preamble.WriteString("\n")
		}
	}

	states := []string{"absent", "true", "false"}
	startState = states[rng.Intn(len(states))]
	if startState != "absent" {
		preamble.WriteString(key + " = " + startState + "\n")
	}
	// An array AFTER the flag as well as before it: the scan must resume
	// classifying top-level keys once an array closes, not stop at the first
	// one it walked through.
	if rng.Intn(3) == 0 {
		after := randomMultiLineArray(rng, fmt.Sprintf("tail_arr_%d", rng.Intn(100000)), 1)
		unrelated = append(unrelated, strings.TrimSuffix(after, "\n"))
		preamble.WriteString(after)
	}

	nTables := rng.Intn(3)
	var suffix strings.Builder
	for i := 0; i < nTables; i++ {
		t := randomUnrelatedTable(rng, i+100)
		unrelated = append(unrelated, strings.TrimSuffix(t, "\n"))
		suffix.WriteString(t)
	}

	rendered := preamble.String() + suffix.String()
	if rng.Intn(5) == 0 {
		rendered = strings.TrimSuffix(rendered, "\n")
	}
	return unrelated, startState, []byte(rendered)
}
