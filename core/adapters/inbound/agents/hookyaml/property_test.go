// property_test.go is this package's answer to AGENTS.md's rule that "code
// that emits bytes from a structural diff gets a property test". Hand-written
// cases encode what the author already thought of, which is the same set they
// got right; hookjson's splicer shipped with seven green round-trip tests and a
// defect writing `,,` into ~11% of randomly shaped documents.
//
// # Which structural axes the generator varies
//
// Naming them is half the value, because a generator that varies only the axis
// the author thought of is the same vacuous green wearing a different hat —
// hookjson's own first draft mutated only object members and never produced the
// multi-item removal its production path performs. These are the axes here, and
// each is a shape a real hermes config.yaml takes:
//
//  1. WHETHER the target block exists, and in which of the three modelled
//     forms: absent, present-and-empty (`hooks:` with nothing under it),
//     present as the empty flow mapping (`hooks: {}`, the spelling hermes' own
//     DEFAULT_CONFIG carries), or present with user event keys.
//  2. The INDENT of the block's own keys — 2 or 4 spaces — which the region has
//     to adopt rather than impose.
//  3. WHERE the block sits: first, in the middle, or last among the top-level
//     keys. Last is the case with no following key to terminate the block.
//  4. The SURROUNDING content: scalars, nested mappings, block sequences,
//     dotted keys, quoted values carrying `#` and `:`, standalone comments at
//     column 0 and inside a nested mapping, trailing comments, and blank lines.
//  5. The TRAILING NEWLINE, present or absent — a file that does not end in one
//     is where an append is most likely to fuse two lines together.
//  6. The LINE ENDING, LF or CRLF. The scanner keeps each original line's bytes
//     exactly as they were, so a CRLF document must round-trip unchanged even
//     though the region itself is written with LF.
//  7. The number of USER event keys already in the block (0-3), drawn from
//     names that never collide with the region's own.
//  8. STRAY entries: entries carrying the owner's sentinel with no markers
//     around them, which is what the agent's own config writer leaves behind
//     when it re-serializes the document from a parsed dict and drops every
//     comment. Those must be recovered, not read as a user's colliding hooks.
//
// # Which properties survive which mutation
//
// Stated per assertion below rather than as one blanket claim, because they are
// not all the same. Round-tripping is exact for install-then-uninstall.
// Preservation is exact for install alone in the sense that the ONLY lines the
// output adds are the region's (plus at most one synthesized `hooks:` line) —
// but it is NOT "the output contains the input as a substring", since a `{}`
// install rewrites one line in place.
package hookyaml

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// propertyIterations is committed small enough to stay in the suite (2000
// documents, ~0.3s). A much larger sweep was run locally before landing —
// 40,000 iterations across seeds 1, 2, 999, 424242, 31337 and this one, 240,000
// documents in total, all green — and that sweep is reproduced by editing the
// two constants below, which is why they are constants rather than literals.
//
// The committed run is not decoration: this generator found three real defects
// in the package while it was being written, and the third is the one that
// justifies the axis census below.
//
//  1. Uninstall deleted a `hooks:` key the USER had written, because the first
//     draft cleaned up any block it left empty (iteration 1).
//  2. The `hooks: {}` rewrite emitted an LF into a CRLF document, changing a
//     line it promises only to normalize (iteration 78).
//  3. A block key that is the file's LAST line with no final newline had the
//     BEGIN marker spliced onto it as a trailing comment. The document still
//     parses, so nothing complains — and the marker is no longer a line, so
//     uninstall can never find the region again and the install is permanent.
//     That position existed in this file's stated axis list but the generator
//     never produced it: `writeBlock` ran inside the rendering loop and one
//     more filler section was always written after it. The census reported
//     `block is the last top-level key   0 of 2000` on its very first run,
//     which is the whole argument for counting what was produced rather than
//     trusting the list.
const propertyIterations = 2000

// propertySeed is fixed so a failure reproduces exactly.
const propertySeed = 17221722

// TestSplice_PropertyRandomDocuments generates a random document, applies a
// random operation, and asserts the properties above.
func TestSplice_PropertyRandomDocuments(t *testing.T) {
	rng := rand.New(rand.NewSource(propertySeed)) // #nosec G404 -- a deterministic generator, not a security primitive
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	census := map[string]int{}
	installed, refused := 0, 0
	for i := 0; i < propertyIterations; i++ {
		doc := generateDocument(rng)
		doc.recordAxes(census)
		op := operations[rng.Intn(len(operations))]

		if err := os.WriteFile(path, []byte(doc.text), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		cfg := propertyConfig(path, rng)

		doc.collides = collides(doc, cfg)

		ok := op.run(t, cfg, doc, i)
		if !ok {
			t.Fatalf("iteration %d, operation %q, seed %d\n--- document ---\n%s\n--- end ---",
				i, op.name, propertySeed, doc.text)
		}
		if doc.collides {
			refused++
		} else {
			installed++
		}
	}

	census["installed"], census["refused"] = installed, refused
	assertEveryAxisWasExercised(t, census)
}

// requiredAxes is the census this generator must produce, in the vocabulary of
// the code under test rather than of YAML. A generator can stop emitting a
// construct entirely — one character in an `rng.Intn` threshold does it — and a
// mechanism that silently stops running must fail loudly, so the counts are
// printed on every run and a zero is an error.
//
// "installed" and "refused" are the two branches of the collision decision:
// without both, every property above is graded on one of them, and the refusal
// branch is where a wrong answer silently disables the whole install.
var requiredAxes = []string{
	"installed",
	"refused",
	"block absent (the region owns the key)",
	"block present and empty",
	"block present as the empty flow mapping",
	"block present with user event keys",
	"block content at indent 4",
	"CRLF line endings",
	"no trailing newline",
	"block is the last top-level key",
	"stray sentinel-bearing entries (markers dropped by the agent's own writer)",
}

func assertEveryAxisWasExercised(t *testing.T, census map[string]int) {
	t.Helper()
	for _, axis := range requiredAxes {
		t.Logf("axis %-45s %5d of %d documents", axis, census[axis], propertyIterations)
	}
	for _, axis := range requiredAxes {
		if census[axis] == 0 {
			t.Errorf("the generator produced no document exercising %q — that axis is claimed "+
				"in this file's header and graded by nothing", axis)
		}
	}
}

// recordAxes counts the structural axes one generated document exercises.
func (d document9) recordAxes(census map[string]int) {
	switch {
	case !d.hasBlock:
		census["block absent (the region owns the key)"]++
	case d.flowEmpty:
		census["block present as the empty flow mapping"]++
	case len(d.declaredEvents) > 0:
		census["block present with user event keys"]++
	default:
		census["block present and empty"]++
	}
	if d.blockIndent == 4 {
		census["block content at indent 4"]++
	}
	if strings.Contains(d.text, "\r\n") {
		census["CRLF line endings"]++
	}
	if d.text != "" && !strings.HasSuffix(d.text, "\n") {
		census["no trailing newline"]++
	}
	if d.blockIsLast {
		census["block is the last top-level key"]++
	}
	if d.hasStrays {
		census["stray sentinel-bearing entries (markers dropped by the agent's own writer)"]++
	}
}

// propertyConfig varies the region's own contents too: one to three entries,
// with and without a timeout, and a command carrying the characters most likely
// to break a YAML scalar.
func propertyConfig(path string, rng *rand.Rand) Config {
	commands := []string{
		`/bin/sh -c '/usr/local/bin/irrlichd --version hook-post hermes >/dev/null || true'`,
		`/bin/sh -c '/Users/o'\''brien/my apps/irrlichd hook-post hermes'`,
		`/bin/sh -c '/w#eird/ir:rlichd hook-post hermes'`,
		`/back\slash/irrlichd hook-post hermes`,
	}
	events := []string{"on_session_end", "pre_approval_request", "post_approval_response"}
	n := 1 + rng.Intn(len(events))
	entries := make([]Entry, 0, n)
	for i := 0; i < n; i++ {
		e := Entry{Event: events[i], Command: commands[rng.Intn(len(commands))]}
		if rng.Intn(2) == 0 {
			e.TimeoutSeconds = 1 + rng.Intn(5)
		}
		entries = append(entries, e)
	}
	return Config{Path: path, BlockKey: testBlock, Owner: testOwner, Entries: entries, Sentinel: testSentinel}
}

// testSentinel is the substring every command above carries. It stands in for
// the beacon sentinel a real adapter passes.
const testSentinel = "hook-post hermes"

// --- the operations ---

type operation struct {
	name string
	run  func(t *testing.T, cfg Config, doc document9, i int) bool
}

var operations = []operation{
	{name: "install", run: opInstall},
	{name: "install twice", run: opInstallTwice},
	{name: "install then uninstall", run: opRoundTrip},
	{name: "uninstall without install", run: opUninstallOnly},
}

// opInstall asserts the preservation property: the only lines the output adds
// are the region's, plus at most one synthesized block key, and every other
// line survives in its original order and its original bytes.
func opInstall(t *testing.T, cfg Config, doc document9, _ int) bool {
	t.Helper()
	changed, err := EnsureInstalled(cfg)
	if !expectOutcome(t, doc, changed, err, true) {
		return false
	}
	if doc.collides {
		return unchangedOnDisk(t, cfg, doc)
	}
	out := mustRead(t, cfg.Path)
	if doc.hasStrays {
		// The line-preservation claim does not hold for a recovery: the stray
		// entries are deliberately REMOVED. What must hold instead is that
		// nothing of ours survives outside the region, and that the user's own
		// keys are untouched — asserted here rather than folded into
		// wantRoundTrip, because pretending a recovery is a no-op edit is the
		// kind of blanket property that produces false failures.
		return assertStraysRecovered(t, cfg, doc, out)
	}
	return assertOnlyTheRegionWasAdded(t, cfg, doc, out)
}

// assertStraysRecovered is opInstall's claim for a document the agent's own
// writer had stripped: exactly one region, every event named once (a duplicate
// YAML key silently keeps only one), and the user's own keys still there.
func assertStraysRecovered(t *testing.T, cfg Config, doc document9, out string) bool {
	t.Helper()
	if n := strings.Count(out, BeginMarker(cfg.Owner)); n != 1 {
		t.Errorf("found %d regions after recovery, want 1:\n%s", n, out)
		return false
	}
	// Counted as KEY LINES rather than at a fixed indent: when the strays were
	// the block's only content the block empties out, so the region is
	// rendered at the default indent rather than the one the strays sat at.
	// That is correct, and an indent-pinned assertion reported it as a
	// disappearing event.
	for _, e := range cfg.Entries {
		if n := countKeyLines(out, e.Event); n != 1 {
			t.Errorf("event %q appears as a key %d times after recovery, want 1 — a duplicate YAML key keeps only one:\n%s", e.Event, n, out)
			return false
		}
	}
	for _, declared := range doc.declaredEvents {
		if countKeyLines(out, declared) != 1 {
			t.Errorf("the user's own event %q was removed by the recovery:\n%s", declared, out)
			return false
		}
	}
	return true
}

// countKeyLines counts lines whose whole content is `<name>:`.
func countKeyLines(out, name string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(strings.TrimRight(line, "\r")) == name+":" {
			n++
		}
	}
	return n
}

// opInstallTwice asserts idempotence — the second call must report no change
// and leave the bytes alone. Without it a splicer that appends rather than
// replaces looks correct after one run.
func opInstallTwice(t *testing.T, cfg Config, doc document9, i int) bool {
	t.Helper()
	if !opInstall(t, cfg, doc, i) {
		return false
	}
	if doc.collides {
		return true
	}
	first := mustRead(t, cfg.Path)
	changed, err := EnsureInstalled(cfg)
	if err != nil {
		t.Errorf("second install: %v", err)
		return false
	}
	if changed {
		t.Error("the second install reported a change")
		return false
	}
	if got := mustRead(t, cfg.Path); got != first {
		t.Errorf("the second install rewrote the file:\n%s", got)
		return false
	}
	return true
}

// opRoundTrip is the exactness property: install then uninstall must return the
// original document byte for byte. It is the one that would have caught
// hookjson's `,,` defect directly.
func opRoundTrip(t *testing.T, cfg Config, doc document9, i int) bool {
	t.Helper()
	if !opInstall(t, cfg, doc, i) {
		return false
	}
	if doc.collides {
		return true
	}
	if _, err := Uninstall(cfg); err != nil {
		t.Errorf("uninstall: %v", err)
		return false
	}
	got := mustRead(t, cfg.Path)
	if doc.hasStrays {
		// A recovery is not reversible by definition — the strays were ours
		// and are gone. What uninstall owes here is that NOTHING of ours is
		// left, which is the property the sentinel exists for.
		if strings.Contains(got, cfg.Sentinel) {
			t.Errorf("an entry of ours survived uninstall:\n%s", got)
			return false
		}
		return true
	}
	if want := doc.wantRoundTrip(); got != want {
		t.Errorf("round trip is not the original:\n--- got ---\n%q\n--- want ---\n%q", got, want)
		return false
	}
	return true
}

// opUninstallOnly asserts that a document with no region of ours is never
// touched — the property that protects a user who never granted the
// permission, and the one an over-eager block cleanup breaks.
func opUninstallOnly(t *testing.T, cfg Config, doc document9, _ int) bool {
	t.Helper()
	changed, err := Uninstall(cfg)
	if err != nil {
		t.Errorf("uninstall: %v", err)
		return false
	}
	if doc.hasStrays {
		// Not "no region of ours" at all: the entries are ours, only the
		// markers are gone. Uninstall must still take them out — that is the
		// half of the sentinel that keeps an install removable.
		if !changed {
			t.Error("uninstall found nothing to remove in a document holding our own marker-less entries")
			return false
		}
		if got := mustRead(t, cfg.Path); strings.Contains(got, cfg.Sentinel) {
			t.Errorf("an entry of ours survived uninstall:\n%s", got)
			return false
		}
		return true
	}
	if changed {
		t.Error("uninstall reported a change against a document holding no region of ours")
		return false
	}
	return unchangedOnDisk(t, cfg, doc)
}

// --- assertions ---

func expectOutcome(t *testing.T, doc document9, changed bool, err error, wantChange bool) bool {
	t.Helper()
	if doc.collides {
		if err == nil {
			t.Error("a document already declaring one of the region's events was installed into")
			return false
		}
		return true
	}
	if err != nil {
		t.Errorf("EnsureInstalled: %v", err)
		return false
	}
	if changed != wantChange {
		t.Errorf("changed = %v, want %v", changed, wantChange)
		return false
	}
	return true
}

func unchangedOnDisk(t *testing.T, cfg Config, doc document9) bool {
	t.Helper()
	if got := mustRead(t, cfg.Path); got != doc.text {
		t.Errorf("the document was modified:\n--- got ---\n%q\n--- want ---\n%q", got, doc.text)
		return false
	}
	return true
}

// assertOnlyTheRegionWasAdded is the preservation property, stated as a
// multiset-and-order claim over LINES rather than as a substring claim: the
// `{}` case rewrites one line in place, so the input is not always a substring
// of the output, but every line that is not the region's and not the rewritten
// block key must survive in order and in its original bytes.
func assertOnlyTheRegionWasAdded(t *testing.T, cfg Config, doc document9, out string) bool {
	t.Helper()

	region, err := doc.wantRegion(cfg)
	if err != nil {
		t.Errorf("rendering the expected region: %v", err)
		return false
	}
	if !strings.Contains(out, string(region)) {
		t.Errorf("the rendered region is not present verbatim:\n--- out ---\n%s\n--- region ---\n%s", out, region)
		return false
	}
	if n := strings.Count(out, BeginMarker(cfg.Owner)); n != 1 {
		t.Errorf("found %d BEGIN markers, want 1:\n%s", n, out)
		return false
	}

	survivors := linesOutsideRegion(out, cfg.Owner)
	original := splitKeepingEndings(doc.text)

	// The block key line is the one line an install may rewrite (the `{}`
	// form) or add (the absent form). Everything else must match position for
	// position.
	oi := 0
	for _, line := range survivors {
		if oi < len(original) && line == original[oi] {
			oi++
			continue
		}
		// A document that did not end in a newline gains one when the region
		// is appended after it. That is the append doing its job — the
		// alternative fuses the last key onto the BEGIN marker — so the LAST
		// original line is allowed to differ by exactly that.
		if oi == len(original)-1 && line == original[oi]+"\n" {
			oi++
			continue
		}
		if isBlockKeyLine(line, cfg.BlockKey) {
			if oi < len(original) && isBlockKeyLine(original[oi], cfg.BlockKey) {
				oi++
			}
			continue
		}
		t.Errorf("output line %q is neither an original line in order nor the block key:\n--- out ---\n%s\n--- in ---\n%s",
			line, out, doc.text)
		return false
	}
	if oi != len(original) {
		t.Errorf("only %d of %d original lines survived in order:\n--- out ---\n%s\n--- in ---\n%s",
			oi, len(original), out, doc.text)
		return false
	}
	return true
}

// linesOutsideRegion returns every line of out that is not inside this owner's
// marker-delimited region, keeping their line endings.
func linesOutsideRegion(out, owner string) []string {
	begin, end := BeginMarker(owner), EndMarker(owner)
	var kept []string
	inRegion := false
	for _, line := range splitKeepingEndings(out) {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == begin:
			inRegion = true
		case trimmed == end:
			inRegion = false
		case !inRegion:
			kept = append(kept, line)
		}
	}
	return kept
}

func isBlockKeyLine(line, blockKey string) bool {
	t := strings.TrimRight(line, "\r\n")
	return t == blockKey+":" || strings.HasPrefix(t, blockKey+": ")
}

func splitKeepingEndings(s string) []string {
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- the temp path this test created
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

// --- the generator ---

// document9 is a generated document plus what the properties need to know
// about it. Named for the nine-ish axes above rather than "document", which is
// this package's own production type.
type document9 struct {
	text string
	// collides is derived per iteration from declaredEvents and the config
	// actually being installed; see collides.
	collides bool
	// hasBlock reports that the document already carries the target key, so
	// the install merges into it rather than creating (and owning) it.
	hasBlock bool
	// blockIndent is the indent the block's own keys sit at, or 0 when the
	// block is absent or empty.
	blockIndent int
	// flowEmpty reports the `hooks: {}` form, the one line an install rewrites.
	flowEmpty bool
	// keyLine is the exact `hooks: {}` line, and normalizedKeyLine what an
	// install turns it into — the property the round trip is graded against
	// for that one shape.
	keyLine, normalizedKeyLine string
	// hasStrays records that the block holds sentinel-bearing entries with no
	// markers — the state the agent's own config writer leaves behind.
	hasStrays bool
	// blockIsLast records that no top-level key follows the block, which is
	// the case with nothing to terminate it.
	blockIsLast bool
	// keyLineIsLast records the sharper version: the block KEY is the file's
	// last line, so an install splices at end-of-file and a missing final
	// newline has to be supplied.
	keyLineIsLast bool
	// declaredEvents are the event keys the generated block already holds. An
	// install collides when it intersects the ENTRIES of the config actually
	// being installed, which is why this is a list rather than a bool: the
	// generator picks the document and propertyConfig independently picks how
	// many entries to install, and the first draft's bool conflated "the
	// document names a region event" with "this install names it too" and
	// reported a legal install as a missing refusal.
	declaredEvents []string
}

// collides reports whether cfg's entries intersect the events the document
// already declares.
func collides(d document9, cfg Config) bool {
	for _, e := range cfg.Entries {
		for _, declared := range d.declaredEvents {
			if declared == e.Event {
				return true
			}
		}
	}
	return false
}

func (d document9) wantIndent() int {
	if d.blockIndent > 0 {
		return d.blockIndent
	}
	return defaultBlockIndent
}

// wantRegion renders the bytes the install should have written, in whichever
// of the two placements this document forces. Deriving it from the DOCUMENT
// rather than from the package's own scan is what keeps the assertion
// independent of the code under test.
func (d document9) wantRegion(cfg Config) ([]byte, error) {
	if d.hasBlock {
		return renderRegion(cfg, d.wantIndent())
	}
	return renderOwnedRegion(cfg)
}

// wantRoundTrip is the document an install-then-uninstall must produce: the
// original, plus the two permanent normalizations the package doc lists. This
// encodes them rather than restating them, so a THIRD one appearing fails here
// instead of being absorbed into a looser assertion.
func (d document9) wantRoundTrip() string {
	out := d.text
	if d.flowEmpty {
		if strings.Contains(out, d.keyLine) {
			out = strings.Replace(out, d.keyLine, d.normalizedKeyLine, 1)
		} else {
			// The `{}` line was the document's LAST and its newline was
			// trimmed. The rewrite still has to terminate it, because the
			// region goes immediately after — so this shape normalizes the
			// value AND supplies the missing terminator, which is the same
			// normalization the package doc's item 1 describes reaching EOF.
			out = strings.Replace(out,
				strings.TrimRight(d.keyLine, "\r\n"),
				strings.TrimRight(d.normalizedKeyLine, "\r\n")+"\n", 1)
		}
	}
	// Only an insertion AT END-OF-FILE adds the newline: either the region is
	// appended (no block at all) or it goes after a block key that is itself
	// the last line. A region spliced into the middle leaves the tail alone,
	// so a missing final newline stays missing — which is why this is
	// conditioned rather than applied unconditionally.
	if (!d.hasBlock || d.keyLineIsLast) && out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

// regionEvents are the names propertyConfig draws from. A generated user hook
// on one of them is what makes a document collide.
var regionEvents = []string{"on_session_end", "pre_approval_request", "post_approval_response"}

// userEvents are hermes event names the region never installs, so a block
// holding one is a merge rather than a collision.
var userEvents = []string{"post_tool_call", "pre_tool_call", "on_session_start", "subagent_stop"}

func generateDocument(rng *rand.Rand) document9 {
	nl := "\n"
	if rng.Intn(4) == 0 {
		nl = "\r\n"
	}

	var doc document9

	// Filler sections are rendered first and the block is spliced between two
	// of them, so "the block is LAST" is an index rather than a special case.
	// The first draft placed the block inside the rendering loop and then
	// always wrote one more filler after it, so that position never occurred
	// at all — which the axis census below caught on its first run, and which
	// is exactly why the census exists.
	sections := make([]string, 1+rng.Intn(3))
	for i := range sections {
		sections[i] = filler(rng, nl, i)
	}

	blockMode := rng.Intn(4) // 0 absent, 1 empty, 2 flow-empty, 3 populated
	at := -1
	if blockMode != 0 {
		at = rng.Intn(len(sections) + 1)
		doc.blockIsLast = at == len(sections)
		doc.keyLineIsLast = doc.blockIsLast && blockMode != 3
	}

	var b strings.Builder
	for i, sec := range sections {
		if i == at {
			writeBlock(&b, rng, nl, blockMode, &doc)
		}
		b.WriteString(sec)
	}
	if at == len(sections) {
		writeBlock(&b, rng, nl, blockMode, &doc)
	}

	text := b.String()
	if rng.Intn(5) == 0 {
		text = strings.TrimRight(text, "\r\n")
	}
	doc.text = text
	return doc
}

func writeBlock(b *strings.Builder, rng *rand.Rand, nl string, mode int, doc *document9) {
	switch mode {
	case 1:
		doc.hasBlock = true
		b.WriteString("hooks:" + nl)
	case 2:
		doc.hasBlock = true
		doc.flowEmpty = true
		doc.keyLine, doc.normalizedKeyLine = "hooks: {}"+nl, "hooks:"+nl
		if rng.Intn(2) == 0 {
			doc.keyLine = "hooks: {}   # nothing configured" + nl
			doc.normalizedKeyLine = "hooks:   # nothing configured" + nl
		}
		b.WriteString(doc.keyLine)
	case 3:
		doc.hasBlock = true
		indent := 2
		if rng.Intn(2) == 0 {
			indent = 4
		}
		doc.blockIndent = indent
		pad := strings.Repeat(" ", indent)
		b.WriteString("hooks:" + nl)
		if rng.Intn(3) == 0 {
			b.WriteString(pad + "# my own hooks" + nl)
		}
		// used keeps the generated block a legal YAML mapping. Without it a
		// stray and a user entry can pick the same event name, which is a
		// duplicate key — a malformed FIXTURE, and every assertion downstream
		// would then be grading the wrong thing.
		used := map[string]bool{}
		n := 1 + rng.Intn(3)
		for i := 0; i < n; i++ {
			// One entry in five is a STRAY: ours, sentinel and all, with the
			// markers gone — the state the agent's own config writer leaves
			// behind. It is NOT recorded in declaredEvents, because it is not
			// a user key and must not be read as a collision.
			if rng.Intn(5) == 0 {
				event := pickUnused(rng, regionEvents, used)
				if event == "" {
					continue
				}
				doc.hasStrays = true
				b.WriteString(pad + event + ":" + nl)
				b.WriteString(pad + pad + `- command: "/bin/sh -c '/opt/irrlichd ` + testSentinel + `'"` + nl)
				continue
			}
			// One entry in six declares one of OUR event names as a user hook,
			// which is what exercises the collision refusal.
			pool := userEvents
			if rng.Intn(6) == 0 {
				pool = regionEvents
			}
			event := pickUnused(rng, pool, used)
			if event == "" {
				continue
			}
			doc.declaredEvents = append(doc.declaredEvents, event)
			b.WriteString(pad + event + ":" + nl)
			b.WriteString(pad + pad + `- command: "/usr/local/bin/tool --flag # not a comment"` + nl)
			if rng.Intn(2) == 0 {
				b.WriteString(pad + pad + "  timeout: 30" + nl)
			}
			if rng.Intn(4) == 0 {
				b.WriteString(nl)
			}
		}
	}
}

// pickUnused returns a name from pool that this block has not used yet, or ""
// when every one is taken.
func pickUnused(rng *rand.Rand, pool []string, used map[string]bool) string {
	start := rng.Intn(len(pool))
	for i := range pool {
		name := pool[(start+i)%len(pool)]
		if !used[name] {
			used[name] = true
			return name
		}
	}
	return ""
}

// filler renders one non-block top-level section. Between them the six shapes
// cover the surrounding-content axis: scalars, nested mappings, block
// sequences, dotted keys, quoted values carrying `#` and `:`, standalone
// comments at column 0 and inside a nested mapping, trailing comments, and
// blank lines.
func filler(rng *rand.Rand, nl string, s int) string {
	var b strings.Builder
	switch rng.Intn(6) {
	case 0:
		b.WriteString("# section comment: with a colon" + nl)
		b.WriteString(fmt.Sprintf("scalar%d: %d"+nl, s, rng.Intn(100)))
	case 1:
		b.WriteString(fmt.Sprintf("mapping%d:"+nl, s))
		b.WriteString(`  default: "anthropic/claude-opus-4.6"   # keep me` + nl)
		b.WriteString("  # a comment inside the mapping" + nl)
		b.WriteString("  nested:" + nl)
		b.WriteString("    deep: true" + nl)
	case 2:
		b.WriteString(fmt.Sprintf("seq%d:"+nl, s))
		b.WriteString("  - one" + nl)
		b.WriteString("  - two" + nl)
	case 3:
		b.WriteString(fmt.Sprintf("dotted.key%d: value"+nl, s))
		b.WriteString(nl)
	case 4:
		b.WriteString(fmt.Sprintf("quoted%d: \"a: b # c\""+nl, s))
	default:
		b.WriteString(nl)
		b.WriteString(fmt.Sprintf("plain%d: text without quotes"+nl, s))
	}
	return b.String()
}
