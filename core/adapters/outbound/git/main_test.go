package git

import (
	"os"
	"testing"

	"irrlicht/core/adapters/outbound/git/gittest"
)

// TestMain primes git before any test runs; see package gittest (#2047).
func TestMain(m *testing.M) {
	gittest.PrimeOrExit()
	os.Exit(m.Run())
}
