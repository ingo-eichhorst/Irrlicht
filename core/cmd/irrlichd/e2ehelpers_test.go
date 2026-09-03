package main

import (
	"testing"
	"time"
)

// This file holds the small e2e helpers several live-daemon tests share. They
// used to live inside the session-control e2e file that #1875 deleted;
// re-homing them here keeps the callers that never had anything to do with
// session control (wiring_test.go, the three uninstall/restart live-daemon
// tests) working.

// e2eLog is a no-op outbound.Logger for tests that need a logger but assert
// nothing about what was logged.
type e2eLog struct{}

func (e2eLog) LogInfo(_, _, _ string)                                  {}
func (e2eLog) LogError(_, _, _ string)                                 {}
func (e2eLog) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (e2eLog) Close() error                                            { return nil }

// pollUntil calls cond until it returns true or the timeout elapses, so a
// fixture observes the condition it waits for instead of sleeping past it.
func pollUntil(t *testing.T, timeout, every time.Duration, cond func() bool) bool {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		if cond() {
			return true
		}
		time.Sleep(every)
	}
	return false
}
