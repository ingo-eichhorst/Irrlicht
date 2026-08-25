// vocab-source-too-small.go — three values total. The "names >= 3 but not all"
// rule cannot match anything at that size, so a scan would report a serene
// zero over the whole repo. The gate must REFUSE instead.
package session

const (
	StateWorking = "working"
	StateWaiting = "waiting"
	StateReady   = "ready"
)

var canonicalStates = []string{StateWorking, StateWaiting, StateReady}
