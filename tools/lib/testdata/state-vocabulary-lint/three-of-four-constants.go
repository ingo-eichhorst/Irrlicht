// three-of-four-constants.go — the CONSTANT spelling of the mutation.
//
// The state names appear only inside `State<Capitalised>` identifiers, never
// as lowercase words, so any pre-filter that matches case-sensitively skips
// this file entirely — silently, while still exiting 0. That is precisely
// what happened when the scan's cheap index() pre-check was first added
// (measured: the repo scan dropped session_detector_activity.go and reported
// its waiver as stale). This fixture is the standing guard against it.
package services

func promote(cur string) bool {
	switch cur {
	case StateWorking, StateWaiting, StateReady:
		return true
	}
	return false
}
