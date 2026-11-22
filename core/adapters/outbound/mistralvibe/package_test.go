package mistralvibe

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// TestPackage_NeverBuildsATransport is #2025's no-dial check, in deepseek's
// stricter form: no .go file in this package, test files included, imports
// net/http or builds an accountquota transport, so no test here can reach
// console.mistral.ai or admin.mistral.ai. Unlike museaccountapi and
// deepseekaccountapi there is no live_probe file at all: issue #2012 forbade
// an agent-run request to any mistral.ai host, and the probe evidence came
// from the maintainer's browser instead. This file is excluded from its own
// scan because it names what it forbids.
func TestPackage_NeverBuildsATransport(t *testing.T) {
	scanned := 0
	for _, name := range scannedGoFiles(t) {
		scanned++
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		for _, forbidden := range []string{"NewHTTPTransport(", `"net/http"`, `"net"`} {
			if strings.Contains(string(src), forbidden) {
				t.Errorf("%s contains %s — nothing in this package may build a transport or dial", name, forbidden)
			}
		}
	}
	if scanned < 4 {
		t.Fatalf("vacuity: scanned only %d .go files", scanned)
	}
}

// scannedGoFiles lists the .go files TestPackage_NeverBuildsATransport
// scans: every one in this package, test files included, except this file.
func scannedGoFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if isScannedGoFile(e) {
			names = append(names, e.Name())
		}
	}
	return names
}

func isScannedGoFile(e os.DirEntry) bool {
	name := e.Name()
	return !e.IsDir() && strings.HasSuffix(name, ".go") && name != "package_test.go"
}

// TestPermission_TextNamesWhatTheCodeTouches derives every name the
// permission must state from the code that acts on it, so the consent text
// cannot drift from the selection, the destination or the binding.
func TestPermission_TextNamesWhatTheCodeTouches(t *testing.T) {
	p := browserSessionPermission(t)
	if !p.RequiresExplicitGrant {
		t.Error("the browser-session permission must require an explicit grant: grant-all must never assert the user's account link")
	}
	text := p.Title + " " + p.Touches + " " + p.Detail
	for _, s := range permissionTextMustName(t) {
		if !strings.Contains(text, s) {
			t.Errorf("the permission text does not name %q", s)
		}
	}
}

// browserSessionPermission is the one permission PermissionDeclaration
// declares, checked for its key, its kind and its wired effects.
func browserSessionPermission(t *testing.T) agent.Permission {
	t.Helper()
	a := PermissionDeclaration(func() error { return nil }, func() error { return nil })
	if a.Identity.Name != Name || len(a.Permissions) != 1 {
		t.Fatalf("declaration = %+v", a)
	}
	p := a.Permissions[0]
	if p.Key != PermissionKey || p.Kind != permission.KindObserve {
		t.Fatalf("key/kind = %q/%v, want %q/observe", p.Key, p.Kind, PermissionKey)
	}
	if p.Key == agent.AccountAPIPermissionKey {
		t.Fatalf("key = %q, want a key distinct from the account-API key", p.Key)
	}
	if p.Apply == nil || p.Remove == nil {
		t.Fatal("Apply/Remove must be wired: Remove is what revokes in-flight work")
	}
	return p
}

// permissionTextMustName is every name the browser-session permission's text
// must state, taken from the code that acts on it: the selected hosts,
// cookie names and prefixes, and the destination's host and path.
func permissionTextMustName(t *testing.T) []string {
	t.Helper()
	u, err := url.Parse(UsageURL)
	if err != nil {
		t.Fatal(err)
	}
	must := []string{"Google Chrome", "macOS", "Chrome last used", "Local State", "Cookies database",
		"\"Chrome Safe Storage\"", "not Always Allow", CSRFHeader, u.Host + u.Path, "user-asserted", "vibe CLI"}
	must = append(must, CookieSelector.Hosts...)
	must = append(must, CookieSelector.Names...)
	for _, prefix := range CookieSelector.NamePrefixes {
		must = append(must, prefix+"*")
	}
	return must
}
