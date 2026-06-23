//go:build !darwin && !linux

package processlifecycle

// IsAlive is a conservative stub on platforms without a real process observer
// (newObserver returns stubObserver there). It reports nothing as alive so the
// diagnostics bundle never claims liveness it can't verify.
func IsAlive(pid int) bool { return false }
