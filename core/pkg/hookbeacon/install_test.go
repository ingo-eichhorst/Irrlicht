package hookbeacon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustCommand renders a command and fails the test if the arguments are
// rejected. Command returns an error because its output is written into a
// user's config and executed by a shell.
func mustCommand(t *testing.T, binaryPath, adapter string) string {
	t.Helper()
	cmd, err := Command(binaryPath, adapter)
	if err != nil {
		t.Fatalf("Command(%q, %q): %v", binaryPath, adapter, err)
	}
	return cmd
}

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
	cmd := mustCommand(t, "/Applications/Irrlicht.app/Contents/MacOS/irrlichd", "gemini-cli")
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
	cmd := mustCommand(t, "/usr/local/bin/irrlichd", "gemini-cli")
	if !strings.Contains(cmd, " "+LegacyGuardToken) {
		t.Fatalf("Command() = %q, want it to carry the %q guard token — without it an irrlichd predating this subcommand starts a daemon on every tool call", cmd, LegacyGuardToken)
	}
	// The guard must LEAD. irrlichd v0.2.0-v0.3.2 dispatch on os.Args[1] only
	// (verified with git show <tag>:core/cmd/irrlichd/main.go), so a trailing
	// guard misses them and they start a daemon — the exact hazard it exists
	// to prevent.
	if strings.Index(cmd, LegacyGuardToken) > strings.Index(cmd, Subcommand) {
		t.Errorf("Command() = %q puts the guard token after the verb; releases up to v0.3.2 only match argv[1]", cmd)
	}
	if !strings.HasSuffix(cmd, failOpenSuffix) {
		t.Errorf("Command() = %q, want it to end in %q — a binary that has stopped existing makes the SHELL exit 127 before any of our code runs", cmd, failOpenSuffix)
	}
	if !strings.Contains(cmd, stdoutRedirect) {
		t.Errorf("Command() = %q, want stdout redirected — the guard token's own banner must not reach a hook's decision channel", cmd)
	}
}

// TestCommandQuotesThePath — this repo tracks a path with a space in it, and a
// user is free to install into one.
func TestCommandQuotesThePath(t *testing.T) {
	cmd := mustCommand(t, "/Users/a b/Irrlicht.app/Contents/MacOS/irrlichd", "codex")
	if !strings.Contains(cmd, `'/Users/a b/Irrlicht.app/Contents/MacOS/irrlichd'`) {
		t.Errorf("Command() = %q, want the binary path quoted as one argument", cmd)
	}
	for _, awkward := range []string{
		"/tmp/it's here/irrlichd",
		"/tmp/'/irrlichd",
		"/tmp/a'b'c/irrlichd",
		"/tmp/trailing'/irrlichd",
	} {
		got, ok := leadingArg(shellQuote(awkward) + " " + LegacyGuardToken)
		if !ok || got != awkward {
			t.Errorf("leadingArg(shellQuote(%q)) = %q, %v; want %q, true", awkward, got, ok, awkward)
		}
	}
}

// TestSentinelIsPathIndependent — identity has to be the part that does not
// vary, or a moved binary reads as somebody else's entry and gets a duplicate
// appended beside it instead of being rewritten in place.
func TestSentinelIsPathIndependent(t *testing.T) {
	a := mustCommand(t, "/one/irrlichd", "gemini-cli")
	b := mustCommand(t, "/two/somewhere/else/irrlichd", "gemini-cli")
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
	cmd := mustCommand(t, live, "gemini-cli")
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
			command: mustCommand(t, executableAt(t, "irrlichd"), adapter),
			want:    DriftBinaryPath,
		},
		"an entry naming a binary that no longer exists": {
			command: mustCommand(t, filepath.Join(t.TempDir(), "gone", "irrlichd"), adapter),
			want:    DriftBinaryMissing,
		},
		"an entry naming a path that is a directory": {
			command: mustCommand(t, t.TempDir(), adapter),
			want:    DriftBinaryMissing,
		},
		"an entry naming a file with no execute bit": {
			command: mustCommand(t, nonExecutableAt(t), adapter),
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
			command: mustCommand(t, live, "codex"),
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
	got, ok := binaryPathOf(mustCommand(t, want, "codex"), "codex")
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

// TestCommandRejectsUnsafeArguments — Command's output is written into a user's
// config file and run by a shell, so the write side owes the same validation
// the read side (adapterFrom) already does. Unvalidated, `Command(exe, "x; rm
// -rf /tmp/pwn #")` renders a shell line that runs the injected command.
func TestCommandRejectsUnsafeArguments(t *testing.T) {
	for _, tt := range []struct{ path, adapter, why string }{
		{"irrlichd", "codex", "a relative binary path"},
		{"./bin/irrlichd", "codex", "a relative binary path"},
		{"/usr/bin/irrlichd", "x; rm -rf /tmp/pwn #", "a shell metacharacter in the adapter"},
		{"/usr/bin/irrlichd", "../../debug/bundle", "a path traversal in the adapter"},
		{"/usr/bin/irrlichd", "", "an empty adapter"},
	} {
		if got, err := Command(tt.path, tt.adapter); err == nil {
			t.Errorf("Command(%q, %q) = %q with no error, want a refusal (%s)", tt.path, tt.adapter, got, tt.why)
		}
	}
}

// TestIsCanonicalLeavesUnrepairableDriftAlone is the counterpart to
// TestInspectDetectsEveryDrift: detection and repair are separate questions.
//
// hookjson has no "the bytes did not change, skip the write" branch, so a drift
// reported as non-canonical that a rewrite cannot actually repair rewrites the
// user's settings file on every daemon start, forever, without converging.
func TestIsCanonicalLeavesUnrepairableDriftAlone(t *testing.T) {
	live := liveBinary(t)

	t.Run("we cannot work out what we would write", func(t *testing.T) {
		// An adapter Command refuses makes the drift unresolvable: there is no
		// replacement line to offer, so the entry must be left alone.
		if !IsCanonical("anything at all", "Not A Valid Segment") {
			t.Error("an unresolvable entry read as needing a rewrite; there is nothing to rewrite it to")
		}
		if got := Inspect("anything at all", "Not A Valid Segment"); got != DriftUnresolvable {
			t.Errorf("Inspect = %q, want %q — it must still be REPORTED, only not repaired", got, DriftUnresolvable)
		}
	})

	t.Run("our own binary is gone, so every candidate line is equally dead", func(t *testing.T) {
		gone := filepath.Join(t.TempDir(), "irrlichd")
		cmd := mustCommand(t, gone, "gemini-cli")
		if got := inspectAgainst(cmd, "gemini-cli", gone); got != DriftBinaryMissing {
			t.Errorf("Inspect = %q, want %q", got, DriftBinaryMissing)
		}
		if !canonicalAgainst(cmd, "gemini-cli", gone) {
			t.Error("an entry already identical to what we would write read as needing a rewrite — that rewrite changes nothing and repeats on every daemon start")
		}
	})

	t.Run("a repairable drift is still repaired", func(t *testing.T) {
		// The vacuity guard for this test: the leniency above must not swallow
		// the case the reconciliation exists for.
		stale := mustCommand(t, filepath.Join(t.TempDir(), "old", "irrlichd"), "gemini-cli")
		if canonicalAgainst(stale, "gemini-cli", live) {
			t.Error("a dead entry that a rewrite WOULD repair read as canonical")
		}
	})
}

// TestSentinelIsNotAPrefixOfAnother — hookjson matches a sentinel with
// strings.Contains, so an unterminated sentinel lets one adapter claim, rewrite
// and uninstall another adapter's entries whenever one id prefixes another.
func TestSentinelIsNotAPrefixOfAnother(t *testing.T) {
	full := mustCommand(t, "/usr/bin/irrlichd", "gemini-cli")
	if strings.Contains(full, Sentinel("gemini")) {
		t.Errorf("Sentinel(%q) matches an entry belonging to %q; one adapter would rewrite and uninstall the other's entries", "gemini", "gemini-cli")
	}
	if !strings.Contains(full, Sentinel("gemini-cli")) {
		t.Error("the sentinel no longer matches its own rendered command")
	}
}

// TestInstalledCommandMatchesTheRunningBinary pins InstalledCommand as exactly
// Command(BinaryPath(), adapter) — the composition an adapter would otherwise
// write itself, with the two error branches it would otherwise carry (#1453).
//
// The equality is the whole assertion, and it is the one that matters: a helper
// resolving a DIFFERENT path than Inspect and IsCanonical judge against would
// report drift against itself and rewrite the user's config on every daemon
// start without ever converging. Deliberately NOT re-asserted here via
// IsCanonical(got) — canonicalAgainst recomputes want from the same
// deterministic BinaryPath and returns early on command == want, so that arm
// cannot fail once this equality holds. TestIsCanonicalAcceptsTheLiveCommand is
// where that property is actually locked.
func TestInstalledCommandMatchesTheRunningBinary(t *testing.T) {
	got, err := InstalledCommand("gemini-cli")
	if err != nil {
		t.Fatalf("InstalledCommand: %v", err)
	}
	if want := mustCommand(t, liveBinary(t), "gemini-cli"); got != want {
		t.Errorf("InstalledCommand = %q, want %q", got, want)
	}
}

// TestInstalledCommandForwardsAdapterValidation confirms the wrapper does not
// lose the refusal Command performs — the rendered text is written into a
// user's config and executed by a shell. One case is enough: InstalledCommand
// only forwards, and the segment grammar itself is pinned by
// TestAdapterSegmentRejectsUnsafeValues (beacon_test.go) and its wrapper-level
// cases by TestCommandRejectsUnsafeArguments above.
func TestInstalledCommandForwardsAdapterValidation(t *testing.T) {
	if got, err := InstalledCommand("../../etc/passwd"); err == nil {
		t.Errorf("InstalledCommand = %q with no error, want a refusal for a path traversal in the adapter", got)
	}
}
