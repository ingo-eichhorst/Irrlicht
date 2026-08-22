package hermes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookyaml"
	"irrlicht/core/domain/permission"
	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/hookbeacon"
)

// hooksPermission returns the hooks permission's Apply/Remove closures off the
// real registration the daemon consumes.
func hooksPermission(t *testing.T) (apply, remove func() error) {
	t.Helper()
	for _, p := range Agent().Permissions {
		if p.Key == PermissionKeyHooks {
			if p.Apply == nil || p.Remove == nil {
				t.Fatal("hooks permission declares no Apply/Remove")
			}
			return p.Apply, p.Remove
		}
	}
	t.Fatal("no hooks permission")
	return nil, nil
}

func readConfig(t *testing.T) string {
	t.Helper()
	path, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	b, err := os.ReadFile(path) // #nosec G304 -- the scratch path this test resolved
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(b)
}

func writeConfig(t *testing.T, body string) {
	t.Helper()
	path, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func readApprovals(t *testing.T) []allowlistEntry {
	t.Helper()
	path, err := AllowlistPath()
	if err != nil {
		t.Fatalf("AllowlistPath: %v", err)
	}
	b, err := os.ReadFile(path) // #nosec G304 -- the scratch path this test resolved
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read allowlist: %v", err)
	}
	var f allowlistFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("decode allowlist: %v", err)
	}
	return f.Approvals
}

// TestHooksPermission_IsGated wires the install-type flavour of the #797
// contract: nothing is written while the permission is pending, granting
// installs both files, and denying removes them.
func TestHooksPermission_IsGated(t *testing.T) {
	hermesHome(t)
	apply, remove := hooksPermission(t)

	state := permission.StatePending
	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		Key: PermissionKeyHooks,
		// store is the adapter's only other declared permission and is
		// observe-kind, so it has no closure to drive: the key-isolation arm is
		// INERT here and repeats the revoked arm exactly, the same situation
		// copilot's, geminicli's, vibe's, pi's and opencode's equivalent tests
		// document. Install-type wirings hold their own permission's closures,
		// so a wrong key is not representable — the arm is load-bearing at the
		// live receiver (hooks_test.go), not here.
		OtherKeys: []string{PermissionKeyStore},
		SetState:  contracttesting.OnlyKey(PermissionKeyHooks, func(s permission.State) { state = s }),
		Exercise: func() {
			switch state {
			case permission.StateGranted:
				if err := apply(); err != nil {
					t.Fatalf("apply: %v", err)
				}
			case permission.StateDenied:
				if err := remove(); err != nil {
					t.Fatalf("remove: %v", err)
				}
			}
		},
		Observe: func() bool {
			status, err := VerifyHooksInstalled()
			if err != nil {
				t.Fatalf("VerifyHooksInstalled: %v", err)
			}
			return status.Intact()
		},
	})
}

// TestEnsureHooksInstalled_LeavesTheRestOfTheConfigByteIdentical is the whole
// reason hookyaml exists rather than a YAML round-trip: the file this installer
// edits is the user's ONE hermes config, carrying their model, provider and
// sandbox settings and the comments hermes' own shipped example teaches them to
// keep.
func TestEnsureHooksInstalled_LeavesTheRestOfTheConfigByteIdentical(t *testing.T) {
	hermesHome(t)
	const original = `# Hermes Agent CLI Configuration
model:
  default: "anthropic/claude-opus-4.6"   # keep this comment
  provider: "auto"

terminal:
  backend: local
`
	writeConfig(t, original)

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}

	got := readConfig(t)
	if !strings.HasPrefix(got, original) {
		t.Fatalf("the original config is no longer a byte-identical prefix of the result:\n--- got ---\n%s", got)
	}
	added := strings.TrimPrefix(got, original)
	// The block key is INSIDE the marked region, because this install is what
	// created it — which is what lets a later uninstall take the key away
	// again without touching a `hooks:` the user wrote themselves.
	if !strings.HasPrefix(added, hookyaml.BeginMarker(hookRegionOwner)+"\n"+hookBlockKey+":\n") {
		t.Errorf("the appended region does not open with the marker and the block key:\n%s", added)
	}
	for _, event := range installedHookEvents {
		if !strings.Contains(added, event+":") {
			t.Errorf("event %q is not in the appended block:\n%s", event, added)
		}
	}
}

// TestEnsureHooksInstalled_RefusesAnEventTheUserAlreadyDeclares is the
// collision refusal. A YAML mapping cannot hold a duplicate key and
// yaml.safe_load resolves one silently to the last occurrence, so writing our
// entry beside theirs would DELETE their hook with no error anywhere. Refusing
// surfaces as #1362's "granted but NOT applied, because …" instead.
func TestEnsureHooksInstalled_RefusesAnEventTheUserAlreadyDeclares(t *testing.T) {
	hermesHome(t)
	original := `hooks:
  ` + HookEventOnSessionEnd + `:
    - command: "/usr/local/bin/my-own-hook"
`
	writeConfig(t, original)

	_, err := EnsureHooksInstalled()
	if err == nil {
		t.Fatal("EnsureHooksInstalled succeeded against a config that already declares one of our events")
	}
	if !strings.Contains(err.Error(), HookEventOnSessionEnd) {
		t.Errorf("error %q does not name the colliding event — #1362 renders this text to the user", err)
	}
	if got := readConfig(t); got != original {
		t.Errorf("the config was modified by a refused install:\n%s", got)
	}
	if approvals := readApprovals(t); len(approvals) != 0 {
		t.Errorf("a refused install still recorded %d approval(s) — an approval for a hook that "+
			"is not installed is a consent record for nothing", len(approvals))
	}
}

// TestEnsureHooksInstalled_WritesTheApprovalHermesRequires is the second half
// of the install, and the half that is easy to leave out: hermes will not RUN a
// configured hook whose (event, command) pair is absent from its allowlist. An
// install without it reads `granted` in the wizard, passes every config
// assertion, and delivers nothing forever.
func TestEnsureHooksInstalled_WritesTheApprovalHermesRequires(t *testing.T) {
	hermesHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}

	beacon, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		t.Fatalf("InstalledCommand: %v", err)
	}
	want := hookCommand(beacon)

	approvals := readApprovals(t)
	if len(approvals) != len(installedHookEvents) {
		t.Fatalf("recorded %d approvals, want one per installed event (%d)", len(approvals), len(installedHookEvents))
	}
	for i, event := range installedHookEvents {
		if approvals[i].Event != event {
			t.Errorf("approval %d is for %q, want %q", i, approvals[i].Event, event)
		}
		if approvals[i].Command != want {
			t.Errorf("approval %d command = %q, want the command the config declares %q", i, approvals[i].Command, want)
		}
		if approvals[i].ApprovedAt == "" {
			t.Errorf("approval %d has no approved_at; hermes prints it in `hermes hooks list`", i)
		}
	}
}

// TestEnsureHooksInstalled_PreservesForeignApprovals pins that the allowlist is
// merged, not replaced. It is the user's own consent record for THEIR hooks,
// and clobbering it would silently disable everything else they had approved.
func TestEnsureHooksInstalled_PreservesForeignApprovals(t *testing.T) {
	home := hermesHome(t)
	const foreign = `{
  "approvals": [
    {"event": "pre_tool_call", "command": "/usr/local/bin/their-hook", "approved_at": "2026-01-01T00:00:00Z"}
  ],
  "some_future_hermes_field": 7
}`
	if err := os.WriteFile(filepath.Join(home, allowlistFileName), []byte(foreign), 0o600); err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}

	approvals := readApprovals(t)
	var kept bool
	for _, a := range approvals {
		if a.Command == "/usr/local/bin/their-hook" {
			kept = true
		}
	}
	if !kept {
		t.Errorf("the user's own approval was dropped: %+v", approvals)
	}

	path, _ := AllowlistPath()
	raw, err := os.ReadFile(path) // #nosec G304 -- the scratch path this test resolved
	if err != nil {
		t.Fatalf("read allowlist: %v", err)
	}
	if !strings.Contains(string(raw), "some_future_hermes_field") {
		t.Errorf("an unknown top-level field was dropped on the round trip:\n%s", raw)
	}
}

// TestUninstallHooks_RemovesBothHalvesAndNothingElse pins that revoking leaves
// the user's own config and their own approvals exactly as they were.
func TestUninstallHooks_RemovesBothHalvesAndNothingElse(t *testing.T) {
	home := hermesHome(t)
	const original = `model:
  default: "anthropic/claude-opus-4.6"
`
	writeConfig(t, original)
	const foreign = `{"approvals":[{"event":"pre_tool_call","command":"/usr/local/bin/their-hook","approved_at":"2026-01-01T00:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(home, allowlistFileName), []byte(foreign), 0o600); err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}
	changed, err := UninstallHooks()
	if err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	if !changed {
		t.Error("UninstallHooks reported no change after an install")
	}

	if got := readConfig(t); got != original {
		t.Errorf("config after uninstall is not the original:\n--- got ---\n%s--- want ---\n%s", got, original)
	}
	approvals := readApprovals(t)
	if len(approvals) != 1 || approvals[0].Command != "/usr/local/bin/their-hook" {
		t.Errorf("approvals after uninstall = %+v, want only the user's own", approvals)
	}

	again, err := UninstallHooks()
	if err != nil {
		t.Fatalf("second UninstallHooks: %v", err)
	}
	if again {
		t.Error("a second UninstallHooks reported a change — it is not idempotent")
	}
}

// TestVerifyHooksInstalled_ReportsARevokedApproval is the #1372 case this
// adapter has and the file-writing adapters do not: `hermes hooks revoke`
// removes the approval and leaves the config entry standing, producing an
// install that LOOKS present and fires nothing.
func TestVerifyHooksInstalled_ReportsARevokedApproval(t *testing.T) {
	home := hermesHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}
	status, err := VerifyHooksInstalled()
	if err != nil {
		t.Fatalf("VerifyHooksInstalled: %v", err)
	}
	if !status.Intact() {
		t.Fatalf("a fresh install is not Intact: %+v", status)
	}

	// What `hermes hooks revoke <command>` does.
	if err := os.WriteFile(filepath.Join(home, allowlistFileName), []byte(`{"approvals":[]}`), 0o600); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	status, err = VerifyHooksInstalled()
	if err != nil {
		t.Fatalf("VerifyHooksInstalled: %v", err)
	}
	if status.Intact() {
		t.Fatal("a revoked approval reads as Intact — the entries are in config.yaml but hermes will not run them")
	}
	if len(status.Missing) != len(installedHookEvents) {
		t.Errorf("Missing = %v, want every installed event", status.Missing)
	}
}

// TestEnsureHooksInstalled_IsIdempotent pins that a second run changes nothing
// — including the approval timestamp, which would otherwise churn the user's
// allowlist on every daemon start.
func TestEnsureHooksInstalled_IsIdempotent(t *testing.T) {
	hermesHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("first install: %v", err)
	}
	before, beforeApprovals := readConfig(t), readApprovals(t)

	changed, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if changed {
		t.Error("the second install reported a change")
	}
	if got := readConfig(t); got != before {
		t.Errorf("the config changed on a second install:\n%s", got)
	}
	after := readApprovals(t)
	if len(after) != len(beforeApprovals) {
		t.Fatalf("approval count changed from %d to %d", len(beforeApprovals), len(after))
	}
	for i := range after {
		if after[i] != beforeApprovals[i] {
			t.Errorf("approval %d churned: %+v -> %+v", i, beforeApprovals[i], after[i])
		}
	}
}

// TestInstalledCommandRoundTripsThroughHermesParser is the property hermes'
// side of the contract rests on: hermes runs `shlex.split(command)` with
// shell=False, so the value written into config.yaml must split into exactly
// [`/bin/sh`, `-c`, <the beacon line>].
//
// The Go half is asserted here; the Python half was verified live during this
// issue's audit against Hermes v0.19.0, including a home directory containing a
// space and a single quote.
func TestInstalledCommandRoundTripsThroughHermesParser(t *testing.T) {
	for _, binary := range []string{
		"/usr/local/bin/irrlichd",
		"/Users/o'brien/my apps/irrlichd",
	} {
		beacon, err := hookbeacon.Command(binary, AdapterName)
		if err != nil {
			t.Fatalf("hookbeacon.Command(%q): %v", binary, err)
		}
		got := hookCommand(beacon)
		argv, err := posixSplit(got)
		if err != nil {
			t.Fatalf("splitting %q: %v", got, err)
		}
		want := []string{shellInterpreter, "-c", beacon}
		if len(argv) != len(want) {
			t.Fatalf("argv = %#v, want %#v", argv, want)
		}
		for i := range want {
			if argv[i] != want[i] {
				t.Fatalf("argv = %#v, want %#v", argv, want)
			}
		}
		if !strings.Contains(got, hookbeacon.Sentinel(AdapterName)) {
			t.Errorf("the installed command %q does not carry the beacon sentinel, so nothing "+
				"would recognize it as ours on the way back out", got)
		}
	}
}

// posixSplit is the subset of Python's shlex.split(posix=True) the installed
// command exercises: unquoted words, single-quoted words, and the backslash
// escape OUTSIDE a quote that makes the '\” sequence work at all. Written out
// rather than approximated with strings.Fields because the whole point is that
// a path containing a space or a quote survives — and the backslash branch is
// load-bearing rather than defensive: without it the first draft of this
// helper read `'\”` as three literal characters and reported a correct
// command as broken.
func posixSplit(s string) ([]string, error) {
	var (
		out     []string
		cur     strings.Builder
		inWord  bool
		inQuote bool
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote && c == '\'':
			inQuote = false
		case inQuote:
			cur.WriteByte(c)
		case c == '\\':
			if i+1 >= len(s) {
				return nil, errUnterminatedQuote
			}
			i++
			inWord = true
			cur.WriteByte(s[i])
		case c == '\'':
			inQuote, inWord = true, true
		case c == ' ':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	if inQuote {
		return nil, errUnterminatedQuote
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

var errUnterminatedQuote = &unterminatedQuoteError{}

type unterminatedQuoteError struct{}

func (*unterminatedQuoteError) Error() string { return "unterminated single quote" }

// TestHookRegionMarkersAreYAMLComments is a LOCK on the one thing that keeps a
// refused or half-written install from breaking the user's agent: the markers
// this installer writes into a live config must be comments, so a config
// carrying them still loads.
func TestHookRegionMarkersAreYAMLComments(t *testing.T) {
	for _, m := range []string{hookyaml.BeginMarker(hookRegionOwner), hookyaml.EndMarker(hookRegionOwner)} {
		if !strings.HasPrefix(m, "#") {
			t.Errorf("marker %q is not a YAML comment", m)
		}
		if !strings.Contains(m, hookRegionOwner) {
			t.Errorf("marker %q does not name its owner, so two owners' regions would be indistinguishable", m)
		}
	}
}
