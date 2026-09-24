package services

import (
	"os"
	"testing"

	"irrlicht/core/adapters/outbound/git/gittest"
)

// TestMain warms the git binary the real git adapter runs before any test in
// this package calls it (#2047): on macOS that binary is the xcrun stub, whose
// first cold call can take the adapter's whole 5s per-call ceiling. See
// package gittest for the measurement and what is assumed about CI.
func TestMain(m *testing.M) {
	gittest.PrimeOrExit()
	os.Exit(m.Run())
}
