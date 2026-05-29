package viewer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// sha256hex is the in-test oracle: the hash promote-recording.sh would produce
// from `printf '%s' "$BLOB" | shasum -a 256`, given an already-compact blob.
func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestRecipeHashOf_PreservesKeyOrder(t *testing.T) {
	// jq -c emits object keys in SOURCE order. recipeHashOf must too, so two
	// recipes that differ only in key order hash differently.
	a := recipeHashOf(json.RawMessage(`{"b":1,"a":2}`))
	b := recipeHashOf(json.RawMessage(`{"a":2,"b":1}`))
	if a == "" || b == "" {
		t.Fatalf("unexpected empty hash: a=%q b=%q", a, b)
	}
	if a == b {
		t.Fatalf("key order must affect the hash, but both were %q", a)
	}
}

func TestRecipeHashOf_IgnoresWhitespace(t *testing.T) {
	// json.Compact strips insignificant whitespace, so a pretty-printed recipe
	// and its compact form must hash identically.
	compact := recipeHashOf(json.RawMessage(`{"b":1,"a":2}`))
	pretty := recipeHashOf(json.RawMessage("{\n  \"b\": 1,\n  \"a\": 2\n}"))
	if compact != pretty {
		t.Fatalf("whitespace must not affect the hash: compact=%q pretty=%q", compact, pretty)
	}
}

func TestRecipeHashOf_MatchesJqShasum(t *testing.T) {
	// The whole point: recipeHashOf == sha256 of the order-preserving compact
	// form, i.e. what `jq -c … | shasum -a 256` yields. The OLD Unmarshal→Marshal
	// path sorted keys, so it would have hashed the sorted form instead — assert
	// we are NOT producing that, to lock in the fix.
	const src = `{"b":1,"a":2}`
	got := recipeHashOf(json.RawMessage(src))
	if want := sha256hex(`{"b":1,"a":2}`); got != want {
		t.Fatalf("hash mismatch: got %q want %q (compact, source-order)", got, want)
	}
	if sorted := sha256hex(`{"a":2,"b":1}`); got == sorted {
		t.Fatalf("hash equals the key-SORTED form %q — the old reorder bug is back", sorted)
	}
}

func TestRecipeHashOf_EmptyAndMalformed(t *testing.T) {
	if h := recipeHashOf(nil); h != "" {
		t.Fatalf("nil input must hash to empty, got %q", h)
	}
	if h := recipeHashOf(json.RawMessage("")); h != "" {
		t.Fatalf("empty input must hash to empty, got %q", h)
	}
	if h := recipeHashOf(json.RawMessage(`{not json`)); h != "" {
		t.Fatalf("malformed input must hash to empty, got %q", h)
	}
}
