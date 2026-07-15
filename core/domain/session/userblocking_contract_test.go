package session

import (
	"testing"

	"irrlicht/core/internal/contracttesting/userblocking"
)

// TestUserBlockingListsAgree is one half of a paired contract: the same
// assertion runs against tailer.isUserBlockingToolName in
// core/pkg/tailer/userblocking_contract_test.go. The two predicates are
// deliberately duplicated (the tailer avoids a domain import), so this pins
// the thing the duplication actually risks — silent drift between them.
//
// In-package (not session_test) because isUserBlockingTool is unexported.
func TestUserBlockingListsAgree(t *testing.T) {
	userblocking.AssertMatchesCanonical(t, "session.isUserBlockingTool", isUserBlockingTool)
}
