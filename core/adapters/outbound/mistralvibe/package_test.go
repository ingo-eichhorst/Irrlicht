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
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || name == "package_test.go" {
			continue
		}
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

// TestPermission_TextNamesWhatTheCodeTouches derives every name the
// permission must state from the code that acts on it, so the consent text
// cannot drift from the selection, the destination or the binding.
func TestPermission_TextNamesWhatTheCodeTouches(t *testing.T) {
	a := PermissionDeclaration(func() error { return nil }, func() error { return nil })
	if a.Identity.Name != Name || len(a.Permissions) != 1 {
		t.Fatalf("declaration = %+v", a)
	}
	p := a.Permissions[0]
	if p.Key != PermissionKey || p.Key == agent.AccountAPIPermissionKey || p.Kind != permission.KindObserve {
		t.Fatalf("key/kind = %q/%v, want %q/observe, distinct from the account-API key", p.Key, p.Kind, PermissionKey)
	}
	if p.Apply == nil || p.Remove == nil {
		t.Fatal("Apply/Remove must be wired: Remove is what revokes in-flight work")
	}
	if !p.RequiresExplicitGrant {
		t.Error("the browser-session permission must require an explicit grant: grant-all must never assert the user's account link")
	}
	text := p.Title + " " + p.Touches + " " + p.Detail
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
	for _, s := range must {
		if !strings.Contains(text, s) {
			t.Errorf("the permission text does not name %q", s)
		}
	}
}
