package tailer

import (
	"testing"

	"irrlicht/core/internal/contracttesting/userblocking"
)

// TestUserBlockingListsAgree is one half of a paired contract: the same
// assertion runs against session.isUserBlockingTool in
// core/domain/session/userblocking_contract_test.go. The two predicates are
// deliberately duplicated (the tailer avoids a domain import), so this pins
// the thing the duplication actually risks — silent drift between them.
//
// In-package (not tailer_test) because isUserBlockingToolName is unexported.
func TestUserBlockingListsAgree(t *testing.T) {
	userblocking.AssertMatchesCanonical(t, "tailer.isUserBlockingToolName", isUserBlockingToolName)
}
