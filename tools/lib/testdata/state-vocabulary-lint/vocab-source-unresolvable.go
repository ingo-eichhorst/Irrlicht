// vocab-source-unresolvable.go — the slice names a constant that has no
// declaration in the file. A half-parsed vocabulary must REFUSE, not quietly
// scan for the part it managed to read.
package session

const (
	StateWorking = "working"
	StateWaiting = "waiting"
	StateReady   = "ready"
)

var canonicalStates = []string{StateWorking, StateWaiting, StateReady, StateMissing}
