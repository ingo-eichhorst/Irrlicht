package validate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// unevaluableInvariantRatchet is how many invariants across every committed
// cell the DSL cannot parse, and therefore never checks.
//
// Produced by this test — run it and read the failure, do not count by hand:
//
//	go test ./tools/onboarding-factory/internal/validate/ \
//	    -run TestNoCellCarriesAnUnevaluableInvariant -count=1
//
// It is an UPPER bound. It may only go down. A cell author reaching for
// semantics the DSL does not have (real per-session scoping, a phase-bounded
// window) writes a string that parses as nothing, and before the skip was made
// visible it was reported as "invariant ok" — so this number grew silently for
// as long as the repo has had invariants. The ratchet is what stops that.
//
// It stood at 68 of 632 when first measured, across 10 of 13 adapters. Twelve
// cells were then repaired: "no state_transition to <state> after this phase"
// dropped its redundant qualifier (the window already starts at the phase),
// "no child sessions spawned" became "no parent_linked for session" (a real
// events.jsonl kind), and "no function_call event" / "no tool_use events" were
// deleted outright — events.jsonl has no tool-call vocabulary at all, so those
// asserted nothing that any recording could contradict.
const unevaluableInvariantRatchet = 51

// TestNoCellCarriesAnUnevaluableInvariant keeps the unparseable-invariant count
// from growing back.
//
// The count is deliberately not zero. Closing the remainder needs a DSL change,
// not cell edits: see the "Invariant DSL" doc block in expected.go for the two
// limitations (a decorative session-noun, an unbounded window) and for why
// simply widening the grammar was tried and reverted — it turns a silent no-op
// into a misleading red rather than into a real check.
func TestNoCellCarriesAnUnevaluableInvariant(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "replaydata", "agents")
	matches, err := filepath.Glob(filepath.Join(root, "*", "scenarios", "*", "expected.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	// Fail loudly when the check cannot run at all: an empty glob and a clean
	// repo must not produce the same green.
	if len(matches) == 0 {
		t.Fatalf("no expected.jsonl files found under %s — the census cannot run, which is not the same as finding nothing", root)
	}

	type site struct{ cell, inv string }
	var dead []site
	total := 0

	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		rel := strings.TrimPrefix(filepath.ToSlash(path), filepath.ToSlash(root)+"/")
		cell := strings.TrimSuffix(rel, "/expected.jsonl")
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var p ExpectedPhase
			if json.Unmarshal([]byte(line), &p) != nil {
				continue // meta line, or a shape this test does not own
			}
			for _, inv := range p.Invariants {
				total++
				// Ask the evaluator itself rather than re-implementing its
				// grammar here — a second copy of the regexes would drift from
				// the one that actually runs, and this test would then be
				// measuring something nobody uses.
				if _, _, known := checkInvariant(inv, nil, &recordedEvent{}, time.Time{}); !known {
					dead = append(dead, site{cell, strings.TrimSpace(inv)})
				}
			}
		}
	}

	if total == 0 {
		t.Fatalf("parsed %d expected.jsonl files but found no invariants at all — the census cannot run", len(matches))
	}

	if len(dead) > unevaluableInvariantRatchet {
		sort.Slice(dead, func(i, j int) bool {
			if dead[i].cell != dead[j].cell {
				return dead[i].cell < dead[j].cell
			}
			return dead[i].inv < dead[j].inv
		})
		var b strings.Builder
		fmt.Fprintf(&b, "%d of %d committed invariants cannot be evaluated by the DSL (ratchet: %d).\n\n",
			len(dead), total, unevaluableInvariantRatchet)
		b.WriteString("An invariant the DSL cannot parse is never checked. It is recorded as\n")
		b.WriteString("SKIPPED rather than failing its phase, so nothing else will tell you it\n")
		b.WriteString("is there. Either express it in an accepted form, or delete it — a\n")
		b.WriteString("decorative invariant is worse than none, because it reads as coverage.\n\n")
		b.WriteString("Accepted forms:\n")
		b.WriteString("  no <kind> for <session-noun>     (kind must be a real events.jsonl kind)\n")
		b.WriteString("  no state_transition to <state>\n\n")
		for _, d := range dead {
			fmt.Fprintf(&b, "  %s\n      %s\n", d.cell, d.inv)
		}
		t.Error(b.String())
	}

	if len(dead) < unevaluableInvariantRatchet {
		t.Errorf("only %d of %d invariants are unevaluable, below the ratchet of %d — lower unevaluableInvariantRatchet to %d so the gain is locked in",
			len(dead), total, unevaluableInvariantRatchet, len(dead))
	}
}
