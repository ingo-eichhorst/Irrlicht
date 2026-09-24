package git

import (
	"testing"

	"irrlicht/core/adapters/outbound/git/gittest"
)

// TestPrimerWarmsTheAdaptersBinary is the check behind gittest's claim that it
// warms "the exact binary the adapter uses" (#2047). The two resolve
// separately — gittest cannot import this package, whose own tests import
// gittest — so this pins them to the same path.
func TestPrimerWarmsTheAdaptersBinary(t *testing.T) {
	if got, want := gittest.Binary(), New().binary(); got != want {
		t.Fatalf("gittest primes %q but the adapter runs %q", got, want)
	}
}
