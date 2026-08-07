package processlifecycle

import (
	"strings"
	"testing"
)

// TestLauncherPermissionDetailListsEveryWhitelistedKey keeps the consent copy
// honest. The whitelist exists twice — as launcherEnvKeys, which decides what
// is actually read, and as prose in the permission's Detail, which is what the
// user is asked to agree to — and nothing but this test makes them agree. A
// key added to the map alone would make the wizard understate what irrlicht
// reads, which is a consent defect no other test would catch.
func TestLauncherPermissionDetailListsEveryWhitelistedKey(t *testing.T) {
	perms := LauncherPermissionDeclaration().Permissions
	if len(perms) != 1 {
		t.Fatalf("expected exactly one launcher permission, got %d", len(perms))
	}
	detail := perms[0].Detail
	for key := range launcherEnvKeys {
		if !strings.Contains(detail, key) {
			t.Errorf("env var %q is read but not named in the consent detail", key)
		}
	}
}
