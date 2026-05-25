package processlifecycle

// exitHandler is called when a watched process exits.
//
// The concrete pidMonitor that invokes it is selected at compile time by
// build tag: kqueue EVFILT_PROC on darwin (monitor_darwin.go),
// pidfd_open(2)+poll(2) on linux (monitor_linux.go), and a no-op stub on
// every other platform (monitor_other.go). Each variant exposes the same
// NewMonitor constructor and implements outbound.ProcessWatcher.
type exitHandler func(pid int, sessionID string)
