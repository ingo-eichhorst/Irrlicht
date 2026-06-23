//go:build darwin || linux

package processlifecycle

import "syscall"

// IsAlive reports whether pid names a live process, probed with signal 0 — the
// same liveness check the claudecode PID binder uses. A pid <= 0 is never
// alive. EPERM means the process exists but is owned by another user, which
// still counts as alive (the diagnostics bundle, #736, only needs to know the
// claimed PID is occupied, not that it's signalable).
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
