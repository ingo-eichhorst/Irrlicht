package processlifecycle

import (
	"strings"
	"testing"
)

// TestEndpointPermissionDetailListsEveryKey mirrors
// TestLauncherPermissionDetailListsEveryWhitelistedKey (launcher_permission_test.go):
// the key set exists twice — endpointEnvKeys, which decides what is actually
// read, and the permission's Detail prose, which is what the user agrees to
// — and nothing else keeps them in sync. Also pins the declaration to
// exactly one permission entry, matching LauncherPermissionDeclaration's
// shape (a single observe-kind, daemon-wide capability).
func TestEndpointPermissionDetailListsEveryKey(t *testing.T) {
	perms := EndpointPermissionDeclaration().Permissions
	if len(perms) != 1 {
		t.Fatalf("expected exactly one endpoint permission, got %d", len(perms))
	}
	detail := perms[0].Detail
	for key := range endpointEnvKeys {
		if !strings.Contains(detail, key) {
			t.Errorf("env var %q is read but not named in the consent detail", key)
		}
	}
}

// TestEndpointAndLauncherKeySetsAreDisjoint pins #2002's core requirement at
// the data level: granting one permission must never expose the other's
// values, which starts with the two key sets never overlapping.
func TestEndpointAndLauncherKeySetsAreDisjoint(t *testing.T) {
	for key := range endpointEnvKeys {
		if _, ok := launcherEnvKeys[key]; ok {
			t.Errorf("%q is in both endpointEnvKeys and launcherEnvKeys — a caller holding either permission would receive it", key)
		}
	}
}
