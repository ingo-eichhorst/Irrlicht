package zaiaccountapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDestination_HasTheReviewedRequestShape(t *testing.T) {
	d := Destination()
	if d.Key != DestinationKey || d.URL != "https://api.z.ai/api/monitor/usage/quota/limit" || d.Method != "GET" || d.Body != nil {
		t.Errorf("Destination = %+v, want a bodyless GET to the reviewed quota URL", d)
	}
	if a := Auth(); a.Header != "Authorization" || a.Prefix != "Bearer " {
		t.Errorf("Auth = %+v, want Authorization: Bearer", a)
	}
}

// The binding compares the parsed hostname exactly.
func TestIsZaiRoute(t *testing.T) {
	for endpoint, want := range map[string]bool{
		"https://api.z.ai/api/anthropic":         true,
		"https://API.Z.AI/api/anthropic":         true,
		"https://api.z.ai:443/api/anthropic":     true,
		"https://api.z.ai.example.com/anthropic": false,
		"https://example.com/api.z.ai":           false,
		"https://z.ai/api/anthropic":             false,
		"https://open.bigmodel.cn/api/anthropic": false,
		"api.z.ai/api/anthropic":                 false,
		"":                                       false,
	} {
		if got := IsZaiRoute(endpoint); got != want {
			t.Errorf("IsZaiRoute(%q) = %v, want %v", endpoint, got, want)
		}
	}
}

// TestNoProductionFileBuildsATransport is this package's half of "the suite
// never dials api.z.ai": no non-test file here constructs an HTTP transport,
// so nothing in this package can send a request at all. The daemon's one
// construction lives in core/cmd/irrlichd/zaiaccountapi_effects.go, and
// that package's TestZaiAccountAPI_ProductionPathNeverLeavesLoopback drives
// the production path under a loopback-only dial guard.
func TestNoProductionFileBuildsATransport(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"NewHTTPTransport(", "net/http\"", ".Reveal()"} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("%s contains %q: this package must neither send requests nor unwrap a credential", name, forbidden)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("vacuity: scanned no production file")
	}
}
