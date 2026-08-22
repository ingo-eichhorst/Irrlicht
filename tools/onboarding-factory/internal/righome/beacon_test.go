package righome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryBeaconAdapterDriverPassesTheDaemonAddress is the beacon counterpart
// to TestEveryRigHomeRowsDriverPassesTheHomeThroughTmux, and it exists because
// the rig's own comment was wrong in a way that measured perfectly.
//
// run-cell.sh said hook-driven observation works in coexist mode "since #1178:
// the endpoint the installers write follows IRRLICHT_BIND_ADDR". That is true
// of URL delivery — claudecode, codex and copilot bake the address into the
// bytes they write — and false of beacon delivery, where the installed entry
// carries no address at all and the beacon resolves the target at FIRE time
// from its OWN process environment. That process is a child of the agent CLI,
// which the driver launches through `tmux new-session`, so it inherits the tmux
// SERVER's environment and not the one run-cell.sh exported.
//
// The consequence is the shape this package was created for: nothing fails. The
// pane's beacon reads the default state tree's addr file, posts every hook to
// the PRODUCTION daemon, and the recording daemon records a complete, healthy,
// hook-free session. #1735 measured exactly that twice for mistral-vibe —
// driver_exit_reason=ok, completeness=complete, and not one hook_received in
// the raw capture — and the three adapters with zero hook-bearing recordings
// were precisely the three that import core/pkg/hookbeacon.
//
// So the obligation is derived from the import graph rather than from a list
// somebody remembers to extend: an adapter that adopts beacon delivery
// tomorrow is graded by existing.
func TestEveryBeaconAdapterDriverPassesTheDaemonAddress(t *testing.T) {
	root := repoRoot(t)
	adapters, err := BeaconAdapters(root)
	if err != nil {
		t.Fatalf("scanning for beacon adapters: %v — a check that cannot run must say so", err)
	}

	for _, adapter := range adapters {
		t.Run(adapter, func(t *testing.T) {
			path := filepath.Join(root, "replaydata", "agents", adapter, "driver-interactive.sh")
			src, err := os.ReadFile(path) // #nosec G304 -- path built from the repo root and a scanned adapter slug
			if err != nil {
				t.Fatalf("reading %s's driver: %v — a beacon adapter with no driver cannot be "+
					"checked, and a check that cannot run must say so", adapter, err)
			}
			for _, l := range tmuxLaunchesMissingEnv(string(src), DaemonAddrEnvVar) {
				t.Errorf("%s:%d launches the agent under tmux without passing %s.\n%s\n"+
					"%s's hooks are beacon-delivered, so the installed entry carries no address and "+
					"the beacon — a child of this pane — resolves one at fire time. Without this the "+
					"pane reads the production addr file and a coexisting recording daemon sees no "+
					"hook at all, while the recording still comes back complete and healthy.",
					path, l.line, DaemonAddrEnvVar, l.window, adapter)
			}
			// Vacuity guard: a driver with no tmux launch satisfies the loop
			// above by having nothing to walk.
			if countTmuxLaunches(string(src)) == 0 {
				t.Errorf("%s contains no `tmux new-session` at all — this arm graded nothing", path)
			}
		})
	}
}

// TestScanBeaconPackageNamesEveryWrongShape is the committed mutation evidence
// for the scan, in the shape righome_corpus_test.go already uses for Reconcile:
// the live wiring cannot supply it, because mutating THAT means editing an
// adapter's imports, which is a different change with a different blast radius
// and whose evidence would live in a PR body again.
//
// The want:false rows carry most of the value. A test-only import and a
// same-named import of a DIFFERENT package are both things a substring grep for
// "hookbeacon" would call a beacon adapter, putting an obligation on a driver
// that cannot satisfy it — which is how a rule gets ignored rather than fixed.
func TestScanBeaconPackageNamesEveryWrongShape(t *testing.T) {
	const beaconImport = `import "irrlicht/core/pkg/hookbeacon"`

	cases := []struct {
		name     string
		files    map[string]string
		wantSlug string
		wantUses bool
		wantErr  string // a fragment of the error; "" means no error
	}{
		{
			// The vacuity guard. Without it a scan that reported everything as
			// a beacon adapter would satisfy every negative row below.
			name: "a beacon adapter is found and named",
			files: map[string]string{
				"adapter.go": "package kirocli\n\nconst AdapterName = \"kiro-cli\"\n",
				"hooks.go":   "package kirocli\n\n" + beaconImport + "\n\nvar _ = hookbeacon.Sentinel\n",
			},
			wantSlug: "kiro-cli", wantUses: true,
		},
		{
			name: "an adapter that does not use the beacon is not one",
			files: map[string]string{
				"adapter.go": "package codex\n\nconst AdapterName = \"codex\"\n",
				"hooks.go":   "package codex\n\nimport \"irrlicht/core/adapters/inbound/agents/hookjson\"\n\nvar _ = hookjson.RejectPath\n",
			},
			wantUses: false,
		},
		{
			// want:false — a package whose TESTS reach for the beacon has no
			// beacon-delivered install, so its driver owes nothing.
			name: "a beacon import in a _test.go file only does not count",
			files: map[string]string{
				"adapter.go":      "package copilot\n\nconst AdapterName = \"copilot\"\n",
				"hooks_test.go":   "package copilot\n\n" + beaconImport + "\n\nvar _ = hookbeacon.Sentinel\n",
				"another_test.go": "package copilot\n\n" + beaconImport + "\n",
			},
			wantUses: false,
		},
		{
			// want:false — the substring "hookbeacon" appears and the import
			// path is not ours. A grep-based rule reports this one.
			name: "a same-named package from somewhere else is not the beacon",
			files: map[string]string{
				"adapter.go": "package elsewhere\n\nconst AdapterName = \"elsewhere\"\n",
				"hooks.go":   "package elsewhere\n\nimport \"example.com/vendor/hookbeacon\"\n",
			},
			wantUses: false,
		},
		{
			name: "the AdapterName may live in a different file from the import",
			files: map[string]string{
				"zzz_names.go": "package vibe\n\nconst (\n\tOther = \"x\"\n\tAdapterName = \"mistral-vibe\"\n)\n",
				"hooks.go":     "package vibe\n\n" + beaconImport + "\n",
			},
			wantSlug: "mistral-vibe", wantUses: true,
		},
		{
			name: "an aliased beacon import still counts",
			files: map[string]string{
				"adapter.go": "package geminicli\n\nconst AdapterName = \"gemini-cli\"\n",
				"hooks.go":   "package geminicli\n\nimport beacon \"irrlicht/core/pkg/hookbeacon\"\n\nvar _ = beacon.Sentinel\n",
			},
			wantSlug: "gemini-cli", wantUses: true,
		},
		{
			// The measurement-broke case. A beacon package whose slug cannot be
			// derived must not fall out of the set silently — that is the
			// "absence of a finding and inability to look" shape, and it would
			// remove the adapter's driver from the obligation entirely.
			name: "a beacon package with no AdapterName is an error, not a skip",
			files: map[string]string{
				"hooks.go": "package mystery\n\n" + beaconImport + "\n",
			},
			wantUses: true,
			wantErr:  "declares no `const AdapterName`",
		},
		{
			name: "unparseable source is an error, not a quiet miss",
			files: map[string]string{
				"broken.go": "package oops\n\nimport (\n",
			},
			wantErr: "parsing broken.go",
		},
		{
			// The row that stops importsTheBeacon returning early. Map order is
			// unspecified, so a scan that answered on the first beacon import
			// it found would report this package as a clean beacon adapter or
			// as an error depending on which file it happened to visit first —
			// a check dropped nondeterministically, which is strictly worse
			// than a check dropped always because it cannot be reproduced.
			name: "a beacon import does not excuse a file that will not parse",
			files: map[string]string{
				"adapter.go": "package half\n\nconst AdapterName = \"half\"\n",
				"hooks.go":   "package half\n\n" + beaconImport + "\n",
				"broken.go":  "package half\n\nimport (\n",
			},
			wantErr: "parsing broken.go",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slug, uses, err := ScanBeaconPackage(tc.files)
			if tc.wantErr != "" {
				assertScanRefused(t, slug, uses, err, tc.wantErr)
				return
			}
			assertScanReported(t, slug, uses, err, tc.wantSlug, tc.wantUses)
		})
	}
}

// assertScanRefused grades a row that must produce an error. It names a
// FRAGMENT of the scan's own message and refuses a bare failure: "the scan
// errored" and "the scan reported THIS" are different claims, and only the
// second is evidence that the row reached the check it was written for.
func assertScanRefused(t *testing.T, slug string, uses bool, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error naming %q, got none (slug=%q uses=%v)", want, slug, uses)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
}

// assertScanReported grades a row that must succeed, on both returned facts.
func assertScanReported(t *testing.T, slug string, uses bool, err error, wantSlug string, wantUses bool) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uses != wantUses {
		t.Errorf("usesBeacon = %v, want %v", uses, wantUses)
	}
	if slug != wantSlug {
		t.Errorf("slug = %q, want %q", slug, wantSlug)
	}
}

// TestBeaconAdaptersRefusesRatherThanReturningAShortList pins the refusal that
// makes every check over the result mean something. A tree with no beacon
// adapter in it satisfies TestEveryBeaconAdapterDriverPassesTheDaemonAddress
// perfectly by having nothing to grade.
func TestBeaconAdaptersRefusesRatherThanReturningAShortList(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, AgentsDir, "somewhere"), 0o750); err != nil {
		t.Fatalf("building the empty tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, AgentsDir, "somewhere", "adapter.go"),
		[]byte("package somewhere\n\nconst AdapterName = \"somewhere\"\n"), 0o600); err != nil {
		t.Fatalf("writing the stub adapter: %v", err)
	}
	_, err := BeaconAdapters(dir)
	if err == nil {
		t.Fatal("a tree with no beacon adapter returned a clean empty list; " +
			"'none use the beacon' and 'the scan broke' must not be the same answer")
	}
	if !strings.Contains(err.Error(), "broken scan") {
		t.Errorf("refusal %q does not name it as a broken scan", err)
	}
}

// TestBeaconAdaptersFindsTheRealOnes is the live half: the scan over this repo
// must return a non-empty set whose drivers exist. It deliberately does NOT pin
// the membership — a committed list of adapter names is the second declaration
// this package exists to remove.
func TestBeaconAdaptersFindsTheRealOnes(t *testing.T) {
	root := repoRoot(t)
	adapters, err := BeaconAdapters(root)
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	for _, a := range adapters {
		if _, err := os.Stat(filepath.Join(root, "replaydata", "agents", a)); err != nil {
			t.Errorf("beacon adapter %q has no catalog directory: %v — either the slug derived "+
				"from its AdapterName is not the on-disk one, or the adapter is not onboarded", a, err)
		}
	}
	t.Logf("beacon-delivered adapters: %v", adapters)
}

// TestLaunchWindowCarriesNamesEverySpelling is the corpus for the detector both
// tmux arms rest on. It exists because #1735 REWROTE that detector — the
// original accepted only an `env VAR=` prefix in first position — and a
// rewritten guard owes its predecessor's cases as locks on top of its own, or
// it is not known to be a superset. Rows 1 and 2 are those locks.
//
// The want:false rows are where the value is. A detector that answered true
// unconditionally would satisfy every positive row and read as excellent
// coverage, and "the variable is carried" is the claim whose false positive is
// expensive: it is what lets a driver that silently posts its hooks to the
// production daemon pass this package's tripwire.
func TestLaunchWindowCarriesNamesEverySpelling(t *testing.T) {
	const v = "IRRLICHT_BIND_ADDR"

	cases := []struct {
		name   string
		window string
		want   bool
	}{
		// --- locks on the predecessor's two accepted spellings ---
		{"quoted env prefix, first position", `tmux new-session -d -- env "IRRLICHT_BIND_ADDR=$X" cli`, true},
		{"unquoted env prefix, first position", `tmux new-session -d -- env IRRLICHT_BIND_ADDR=$X cli`, true},

		// --- what the rewrite adds ---
		{"tmux -e flag, quoted", `tmux new-session -d -e "IRRLICHT_BIND_ADDR=$X" cli`, true},
		{"tmux -e flag, unquoted", `tmux new-session -d -e IRRLICHT_BIND_ADDR=$X cli`, true},
		{
			// The case the first draft failed, against a driver that was right.
			"second assignment of one env prefix",
			"tmux new-session -d -s x \\\n    env \"KIRO_HOME=$H\" \"IRRLICHT_BIND_ADDR=$X\" sh -c cmd",
			true,
		},
		{
			"third assignment across a continuation",
			"tmux new-session -d -- \\\n    env \"VIBE_HOME=$H\" \"IRRLICHT_BIND_ADDR=$X\" \\\n        \"VIBE_ENABLE_UPDATE_CHECKS=0\" vibe",
			true,
		},

		// --- want:false ---
		{"no env and no -e at all", `tmux new-session -d -- cli`, false},
		{"a different variable entirely", `tmux new-session -d -- env "KIRO_HOME=$H" cli`, false},
		{"-e naming a different variable", `tmux new-session -d -e "GEMINI_API_KEY=$K" cli`, false},
		{
			// A longer name ending in ours. Accepting this would report a
			// driver as compliant on the strength of an unrelated variable.
			"a variable whose name merely ends in ours",
			`tmux new-session -d -- env "FOO_IRRLICHT_BIND_ADDR=$X" cli`,
			false,
		},
		{
			// An assignment with no env/-e in front is a shell assignment to
			// the tmux CLIENT's environment, which the server-spawned pane does
			// not inherit — the whole defect this package exists for.
			"a bare shell assignment before the launch",
			`IRRLICHT_BIND_ADDR=$X tmux new-session -d -- cli`,
			false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := launchWindowCarries(tc.window, v); got != tc.want {
				t.Errorf("launchWindowCarries(%q, %q) = %v, want %v", tc.window, v, got, tc.want)
			}
		})
	}
}
