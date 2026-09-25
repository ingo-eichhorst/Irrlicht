package main

import "testing"

// Issue #2057 review: the defect #2057 fixed was "no production caller", so
// the builder that hands the daemon its sweep is pinned here. It does not
// reach startBackgroundLoops' `go ...Run` line; that start is covered only by
// the live check named in the PR.
func TestMuseAccountAPI_SweeperIsBuiltWhenTheTransportIs(t *testing.T) {
	api := museAccountAPIEffects(e2eLog{})
	if api.transport == nil {
		t.Fatal("museAccountAPIEffects built no transport for the fixed Muse destination")
	}
	if api.sweeper(nil, nil, nil, e2eLog{}) == nil {
		t.Fatal("sweeper() returned nil with a transport; the daemon would start no sweep")
	}
}

func TestMuseAccountAPI_NoSweeperWithoutATransport(t *testing.T) {
	if got := (museAccountAPI{}).sweeper(nil, nil, nil, e2eLog{}); got != nil {
		t.Fatal("sweeper() built a sweep with no transport to poll through")
	}
}
