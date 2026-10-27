//go:build !linux

package processlifecycle

import "irrlicht/core/domain/agent"

// hasLiveFromTable is linux-only (livetable_linux.go); elsewhere HasLiveProcess
// keeps asking the observer per matcher.
func hasLiveFromTable(agent.ProcessMatcher) (live, ok bool) { return false, false }
