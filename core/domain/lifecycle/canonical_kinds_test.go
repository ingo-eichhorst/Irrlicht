package lifecycle

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

// TestCanonicalKindsCoversEveryDeclaredKind keeps canonicalKinds from going
// stale the moment someone adds a Kind constant.
//
// It parses event.go's own const block rather than restating the vocabulary,
// because a second hand-written list is exactly the failure canonicalKinds
// exists to prevent — the same reasoning behind
// tools/state-vocabulary-lint.sh for session states.
//
// Mutation, run not planned: deleting KindTaskDelta from canonicalKinds takes
// this red with
//
//	Kind constants declared in event.go but missing from canonicalKinds: [KindTaskDelta]
//
// and adding a Kind constant without listing it fails identically.
func TestCanonicalKindsCoversEveryDeclaredKind(t *testing.T) {
	src, err := os.ReadFile("event.go")
	if err != nil {
		t.Fatalf("read event.go: %v", err)
	}

	declRE := regexp.MustCompile(`(?m)^\t(Kind[A-Za-z0-9]+)\s+Kind\s*=\s*"([a-z_]+)"`)
	decls := declRE.FindAllStringSubmatch(string(src), -1)

	// Fail loudly when the parse finds nothing: an empty match set and a
	// fully-covered vocabulary must not produce the same green.
	if len(decls) == 0 {
		t.Fatal("parsed event.go but matched no Kind constant declarations — this test cannot run, which is not the same as finding nothing")
	}

	values := make([]Kind, 0, len(decls))
	byValue := make(map[Kind]string, len(decls))
	for _, m := range decls {
		values = append(values, Kind(m[2]))
		byValue[Kind(m[2])] = m[1]
	}

	var missing []string
	for _, v := range values {
		if !slices.Contains(canonicalKinds, v) {
			missing = append(missing, byValue[v])
		}
	}
	if len(missing) > 0 {
		t.Errorf("Kind constants declared in event.go but missing from canonicalKinds: %v", missing)
	}

	var extra []Kind
	for _, k := range canonicalKinds {
		if !slices.Contains(values, k) {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		t.Errorf("canonicalKinds lists values with no Kind constant in event.go: %v", extra)
	}
}

// TestIsCanonicalKind pins the predicate the invariant DSL relies on.
func TestIsCanonicalKind(t *testing.T) {
	for _, k := range []string{"state_transition", "transcript_removed", "parent_linked", "process_exited"} {
		if !IsCanonicalKind(k) {
			t.Errorf("IsCanonicalKind(%q) = false, want true", k)
		}
	}
	// Both are real strings found in committed invariants. Neither is an
	// events.jsonl kind: they are parser-internal EventType values, so an
	// invariant naming one can never fail.
	for _, k := range []string{"tool_use", "function_call", "", "STATE_TRANSITION"} {
		if IsCanonicalKind(k) {
			t.Errorf("IsCanonicalKind(%q) = true, want false", k)
		}
	}
}

// TestCanonicalKindsReturnsACopy guards the accessor, matching
// session.CanonicalStates()'s own contract: a caller must not be able to
// mutate the package's vocabulary.
func TestCanonicalKindsReturnsACopy(t *testing.T) {
	got := CanonicalKinds()
	if len(got) == 0 {
		t.Fatal("CanonicalKinds() is empty")
	}
	first := got[0]
	got[0] = Kind("clobbered")
	if CanonicalKinds()[0] != first {
		t.Error("CanonicalKinds() exposed the package's own slice — a caller mutated it")
	}
}
