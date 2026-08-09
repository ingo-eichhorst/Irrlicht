package hookbeacon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func liveBinary(t *testing.T) string {
	t.Helper()
	path, err := BinaryPath()
	if err != nil {
		t.Fatalf("BinaryPath(): %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("BinaryPath() = %q, want an absolute path — a relative one is the un-PATHed no-op #1161 removed", path)
	}
	return path
}

// executableAt creates a runnable stand-in binary and returns its path.
func executableAt(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// TestCommandCarriesNoAddress is requirement 4 of #1373 stated as a property of
// the installed text: there is nothing address-shaped in it to go stale.
//
// This is what makes the beacon a structural fix rather than another instance of
// the #1178 repair. A curl entry embeds a port at install time, so every daemon
// that binds elsewhere has to detect and rewrite it; a beacon entry has no port
// to be wrong about, because the address is resolved in the beacon process on the
// tool call that fires it.
func TestCommandCarriesNoAddress(t *testing.T) {
	cmd := Command("/Applications/Irrlicht.app/Contents/MacOS/irrlichd", "gemini-cli")
	for _, forbidden := range []string{"7837", "7838", "http://", "https://", "localhost", "127.0.0.1", "/api/v1/"} {
		if strings.Contains(cmd, forbidden) {
			t.Errorf("Command() = %q contains %q — an installed entry must carry no address, or the #1178 stale-port class is back", cmd, forbidden)
		}
	}
}

// TestCommandEndsWithTheGuardToken locks the defence against an older irrlichd
// being invoked as a beacon and starting a daemon instead. See
// LegacyGuardToken's doc for why --version specifically.
func TestCommandEndsWithTheGuardToken(t *testing.T) {
	cmd := Command("/usr/local/bin/irrlichd", "gemini-cli")
	if !strings.Contains(cmd, " "+LegacyGuardToken) {
		t.Fatalf("Command() = %q, want it to carry the %q guard token — without it an irrlichd predating this subcommand starts a daemon on every tool call", cmd, LegacyGuardToken)
	}
	// The guard must come after the adapter, so a current binary reads the
	// adapter first and a stale one still finds the flag anywhere in argv.
	if strings.Index(cmd, LegacyGuardToken) < strings.Index(cmd, "gemini-cli") {
		t.Errorf("Command() = %q puts the guard token before the adapter", cmd)
	}
	if !strings.Contains(cmd, stdoutRedirect) {
		t.Errorf("Command() = %q, want stdout redirected — the guard token's own banner must not reach a hook's decision channel", cmd)
	}
}

// TestCommandQuotesThePath — this repo tracks a path with a space in it, and a
// user is free to install into one.
func TestCommandQuotesThePath(t *testing.T) {
	cmd := Command("/Users/a b/Irrlicht.app/Contents/MacOS/irrlichd", "codex")
	if !strings.Contains(cmd, `'/Users/a b/Irrlicht.app/Contents/MacOS/irrlichd'`) {
		t.Errorf("Command() = %q, want the binary path quoted as one argument", cmd)
	}
	awkward := "/tmp/it's here/irrlichd"
	if got := shellUnquote(shellQuote(awkward)); got != awkward {
		t.Errorf("shellQuote/shellUnquote round trip = %q, want %q", got, awkward)
	}
}

// TestSentinelIsPathIndependent — identity has to be the part that does not
// vary, or a moved binary reads as somebody else's entry and gets a duplicate
// appended beside it instead of being rewritten in place.
func TestSentinelIsPathIndependent(t *testing.T) {
	a := Command("/one/irrlichd", "gemini-cli")
	b := Command("/two/somewhere/else/irrlichd", "gemini-cli")
	sentinel := Sentinel("gemini-cli")

	if !strings.Contains(a, sentinel) || !strings.Contains(b, sentinel) {
		t.Fatalf("sentinel %q does not appear in both %q and %q", sentinel, a, b)
	}
	if Sentinel("gemini-cli") == Sentinel("codex") {
		t.Error("the sentinel does not distinguish adapters; one adapter's uninstall would take another's entries")
	}
}

// TestIsCanonicalAcceptsTheLiveCommand is the vacuity guard for the drift table
// below: a reconciliation rule that called everything stale would rewrite the
// user's config on every daemon start and still pass every negative case.
func TestIsCanonicalAcceptsTheLiveCommand(t *testing.T) {
	live := liveBinary(t)
	cmd := Command(live, "gemini-cli")
	if drift := Inspect(cmd, "gemini-cli"); drift != DriftNone {
		t.Errorf("Inspect(the command we would write now) = %q, want DriftNone", drift)
	}
	if !IsCanonical(cmd, "gemini-cli") {
		t.Error("IsCanonical rejected the command it just rendered")
	}
}

// TestInspectDetectsEveryDrift covers requirement 5 of #1373: an installed entry
// pointing at a binary path that no longer exists must be DETECTED and rewritten,
// not left to fail quietly.
func TestInspectDetectsEveryDrift(t *testing.T) {
	live := liveBinary(t)
	const adapter = "gemini-cli"

	tests := map[string]struct {
		command string
		want    Drift
	}{
		"an entry naming a different, still-present irrlichd": {
			command: Command(executableAt(t, "irrlichd"), adapter),
			want:    DriftBinaryPath,
		},
		"an entry naming a binary that no longer exists": {
			command: Command(filepath.Join(t.TempDir(), "gone", "irrlichd"), adapter),
			want:    DriftBinaryMissing,
		},
		"an entry naming a path that is a directory": {
			command: Command(t.TempDir(), adapter),
			want:    DriftBinaryMissing,
		},
		"an entry naming a file with no execute bit": {
			command: Command(nonExecutableAt(t), adapter),
			want:    DriftBinaryMissing,
		},
		"our binary, but an older command shape without the guard token": {
			command: shellQuote(live) + " " + Subcommand + " " + adapter,
			want:    DriftShape,
		},
		"a curl entry from before the beacon existed": {
			command: "curl -fsS --max-time 1 -X POST --data-binary @- http://localhost:7837/api/v1/hooks/gemini-cli || true",
			want:    DriftShape,
		},
		"an entry for a different adapter entirely": {
			command: Command(live, "codex"),
			want:    DriftShape,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Inspect(tt.command, adapter); got != tt.want {
				t.Errorf("Inspect(%q) = %q, want %q", tt.command, got, tt.want)
			}
			if IsCanonical(tt.command, adapter) {
				t.Errorf("IsCanonical(%q) = true — a drifted entry that reads as canonical is never rewritten, and fails silently forever", tt.command)
			}
		})
	}
}

func nonExecutableAt(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "irrlichd")
	if err := os.WriteFile(path, []byte("not runnable"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// TestBinaryPathOfRecoversTheInstalledPath — a reconciliation that can only say
// "stale" cannot tell the user what it changed away from.
func TestBinaryPathOfRecoversTheInstalledPath(t *testing.T) {
	const want = "/Users/a b/Irrlicht.app/Contents/MacOS/irrlichd"
	got, ok := binaryPathOf(Command(want, "codex"), "codex")
	if !ok {
		t.Fatalf("binaryPathOf did not recognize the command it rendered")
	}
	if got != want {
		t.Errorf("binaryPathOf = %q, want %q", got, want)
	}
	if _, ok := binaryPathOf("something else entirely", "codex"); ok {
		t.Error("binaryPathOf claimed a non-beacon command as ours")
	}
}

// TestIsExecutableFile pins the three shapes that must all read the same way —
// absent, directory, and present-but-not-runnable are equally unrunnable, and a
// rule that distinguished them would let two of the three drift invisibly.
func TestIsExecutableFile(t *testing.T) {
	if isExecutableFile(filepath.Join(t.TempDir(), "nope")) {
		t.Error("an absent path read as executable")
	}
	if isExecutableFile(t.TempDir()) {
		t.Error("a directory read as executable")
	}
	if isExecutableFile(nonExecutableAt(t)) {
		t.Error("a mode-0644 file read as executable")
	}
	if !isExecutableFile(executableAt(t, "irrlichd")) {
		t.Error("a mode-0755 regular file did not read as executable")
	}
}
