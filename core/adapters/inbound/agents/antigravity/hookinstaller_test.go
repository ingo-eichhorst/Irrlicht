package antigravity

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/permission"
	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/hookbeacon"
)

// foreignBinaryPath is the irrlichd a DIFFERENTLY-situated daemon would have
// named. Deliberately a path that does not exist: an absolute path that has
// stopped existing is exactly the drift beacon delivery newly admits (#1373),
// and it needs no second executable to arrange.
const foreignBinaryPath = "/nonexistent-irrlicht-1723/bin/irrlichd"

// antigravityConfigHome relocates $HOME and returns the hooks.json path inside
// it. Every test that touches the installer must go through this: the real
// ~/.gemini/config/hooks.json is a file the USER writes with Antigravity's own
// /hooks command.
func antigravityConfigHome(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path, err := HooksPath()
	if err != nil {
		t.Fatalf("resolving the hooks path: %v", err)
	}
	if strings.Contains(path, "/.gemini/config/") == false {
		t.Fatalf("HooksPath resolved to %q, which is not under the customization root", path)
	}
	return path
}

// readDoc decodes the installed file the same way the installer does.
func readDoc(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	doc, err := hookjson.ReadSettings(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return doc
}

// ourHandlers returns the handler array under our named hook for one event.
func ourHandlers(t *testing.T, path, event string) []interface{} {
	t.Helper()
	doc := readDoc(t, path)
	ours, ok := doc[hookName].(map[string]interface{})
	if !ok {
		t.Fatalf("no %q object in %s; document is %v", hookName, path, doc)
	}
	handlers, ok := ours[event].([]interface{})
	if !ok {
		t.Fatalf("no %q array under %q; object is %v", event, hookName, ours)
	}
	return handlers
}

// writeDoc lays down a hooks.json with exactly these bytes.
func writeDoc(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// --- the file, the shape, and the inert result ---

// TestHooksPathIsTheOneAntigravityLoads is a LOCK on the measured install
// location. agy 1.1.18 loads named hooks from ~/.gemini/config/hooks.json and
// from nowhere else — the plugin directory `agy plugin install` stages into,
// the workspace .agents/ directory the shipped doc names, and every layout
// reachable via JETSKI_APP_DATA_DIR were each measured to load ZERO.
func TestHooksPathIsTheOneAntigravityLoads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := HooksPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".gemini", "config", "hooks.json"); got != want {
		t.Errorf("HooksPath() = %q, want %q", got, want)
	}
}

// TestInstalledEntryIsFlatAndCarriesTheInertResult pins the three properties
// of the written entry that a reviewer cannot check by reading JSON, each of
// which fails SILENTLY when wrong.
//
//   - FLAT, not grouped. `Stop` takes handler objects directly; a matcher/hooks
//     wrapper is not a validation error, the hook simply never loads.
//   - The command ends in the inert result. See inertResultSuffix.
//   - `timeout` is 5, and the unit is SECONDS. gemini-cli's identically-named
//     field is milliseconds, and a value copied across reads as 5ms there.
func TestInstalledEntryIsFlatAndCarriesTheInertResult(t *testing.T) {
	path := antigravityConfigHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}

	handlers := ourHandlers(t, path, HookEventStop)
	if len(handlers) != 1 {
		t.Fatalf("installed %d handlers, want 1", len(handlers))
	}
	entry, ok := handlers[0].(map[string]interface{})
	if !ok {
		t.Fatalf("handler is %T, want an object — the flat shape puts handler objects in the array directly", handlers[0])
	}
	if _, grouped := entry["hooks"]; grouped {
		t.Error("the handler carries a nested \"hooks\" array — that is the GROUPED shape " +
			"PreToolUse/PostToolUse need. Stop is flat, and a grouped Stop entry never loads")
	}
	if _, matcher := entry["matcher"]; matcher {
		t.Error("the handler carries a \"matcher\" key — Stop's matcher target is N/A")
	}
	if got, _ := entry["type"].(string); got != "command" {
		t.Errorf("type = %q, want \"command\" — the only type antigravity supports", got)
	}
	if !numberIs(entry["timeout"], hookTimeoutSeconds) {
		t.Errorf("timeout = %v, want %d (SECONDS — antigravity's unit, unlike gemini-cli's milliseconds)",
			entry["timeout"], hookTimeoutSeconds)
	}
	command, _ := entry["command"].(string)
	if !strings.HasSuffix(command, inertResultSuffix) {
		t.Errorf("command = %q, which does not end in the inert result %q", command, inertResultSuffix)
	}
	if !strings.Contains(command, hookbeacon.Sentinel(AdapterName)) {
		t.Errorf("command = %q, which does not carry the beacon sentinel", command)
	}
}

// TestInstalledCommandPrintsNothingButAnInertResult is the defect test for
// hazard 1 of #1723, and it EXECUTES the command rather than reasoning about
// it.
//
// The shipped doc contradicts itself about whether Stop's result is honoured:
// the event table says "N/A (ignored)", §5 documents `decision: "continue"`
// blocking the stop and re-entering the agent loop. Under the binding (§5)
// reading, anything this handler prints on stdout is a control channel into
// the user's agent. So the obligation is not "we intend not to emit a
// decision" — it is that the installed command line, run by a real `sh -c`
// exactly as antigravity runs it, cannot put anything but `{}` there.
//
// It is run against a binary that does NOT exist, which is the hostile case:
// the shell's own 127 must not leak, and the beacon's diagnostics must go to
// stderr. That covers the state a user is left in after uninstalling irrlicht
// without revoking the permission.
func TestInstalledCommandPrintsNothingButAnInertResult(t *testing.T) {
	beacon, err := hookbeacon.Command(foreignBinaryPath, AdapterName)
	if err != nil {
		t.Fatalf("render the beacon command: %v", err)
	}
	command := hookCommand(beacon)

	// #nosec G204 -- command is rendered from a package constant and a literal
	// path, never from external input; executing it is the whole point.
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdin = strings.NewReader(`{"conversationId":"` + contractConversationID + `"}`)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("the installed command exited non-zero (%v); a non-zero exit is what a fail-closed "+
			"hook reads as a refusal. stderr: %s", err, stderr.String())
	}

	if got := string(stdout); got != "{}" {
		t.Fatalf("stdout = %q, want exactly \"{}\". Anything else is read by antigravity as a hook "+
			"result, and under hooks.md §5 a result carrying decision:\"continue\" blocks the stop "+
			"and re-enters the agent loop", got)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(stdout, &decoded); err != nil {
		t.Fatalf("stdout %q is not a JSON object: %v", stdout, err)
	}
	if len(decoded) != 0 {
		t.Errorf("the result carries %d key(s): %v — it must carry none, so that no reading of the "+
			"doc can turn it into a decision", len(decoded), decoded)
	}
}

// --- install / verify / uninstall over a shared document ---

func TestEnsureHooksInstalled_CreatesTheFileAndIsIdempotent(t *testing.T) {
	path := antigravityConfigHome(t)

	changed, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if !changed {
		t.Error("first install reported no change")
	}

	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after install: %v", err)
	}

	changed, err = EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if changed {
		t.Error("second install reported a change — the install is not idempotent")
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("the file changed on a no-op install:\n%s\n---\n%s", first, second)
	}
}

// TestEnsureHooksInstalled_PreservesAUserOwnedDocument is the central merge
// obligation: this file is shared with Antigravity's own /hooks command, so
// every named hook, every key and every comment that was there before must
// still be there after.
func TestEnsureHooksInstalled_PreservesAUserOwnedDocument(t *testing.T) {
	path := antigravityConfigHome(t)
	original := `{
  // the user's own lint gate, written by /hooks
  "lint-checker": {
    "enabled": true,
    "PostToolUse": [
      { "matcher": "run_command", "hooks": [ { "type": "command", "command": "./lint.sh", "timeout": 10 } ] }
    ]
  },
  "safety-gate": {
    "enabled": false,
    "PreToolUse": [ { "matcher": "run_command", "hooks": [ { "command": "./safety.sh" } ] } ]
  }
}
`
	writeDoc(t, path, original)

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(after)
	for _, must := range []string{
		"// the user's own lint gate, written by /hooks",
		`"lint-checker"`,
		`"safety-gate"`,
		`"command": "./lint.sh"`,
		`"command": "./safety.sh"`,
		`"enabled": false`,
	} {
		if !strings.Contains(body, must) {
			t.Errorf("the install removed %q from the user's file:\n%s", must, body)
		}
	}

	// And our own entry landed.
	if h := ourHandlers(t, path, HookEventStop); len(h) != 1 {
		t.Errorf("installed %d handlers, want 1", len(h))
	}
}

// TestEnsureHooksInstalled_PreservesForeignContentInsideOurNamedHook covers
// the finer half of the same rule: a key or a handler somebody else put under
// OUR name is still not ours to delete. Upstream merges and runs multiple
// named hooks per event sequentially, so this is unusual rather than
// impossible — and `enabled: false` in particular is how a user turns our hook
// off through the /hooks TUI, which the installer must not undo on the next
// daemon start.
func TestEnsureHooksInstalled_PreservesForeignContentInsideOurNamedHook(t *testing.T) {
	path := antigravityConfigHome(t)
	writeDoc(t, path, `{
  "`+hookName+`": {
    "enabled": false,
    "PreInvocation": [ { "type": "command", "command": "./mine.sh" } ],
    "Stop": [ { "type": "command", "command": "./also-mine.sh" } ]
  }
}
`)

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}

	doc := readDoc(t, path)
	ours, ok := doc[hookName].(map[string]interface{})
	if !ok {
		t.Fatalf("our named hook is gone: %v", doc)
	}
	if enabled, present := ours["enabled"]; !present || enabled != false {
		t.Errorf("enabled = %v (present=%v), want false preserved — a user disabling our hook "+
			"through /hooks must not be overridden on the next daemon start", enabled, present)
	}
	if _, present := ours["PreInvocation"]; !present {
		t.Error("the PreInvocation key under our name was deleted")
	}

	handlers := ourHandlers(t, path, HookEventStop)
	if len(handlers) != 2 {
		t.Fatalf("Stop holds %d handlers, want 2 (the foreign one plus ours)", len(handlers))
	}
	first, _ := handlers[0].(map[string]interface{})
	if got, _ := first["command"].(string); got != "./also-mine.sh" {
		t.Errorf("the foreign handler is now %q, want it preserved in place", got)
	}
	second, _ := handlers[1].(map[string]interface{})
	if !isOurs(second) {
		t.Errorf("our handler was not appended after the foreign one: %v", second)
	}
}

// TestEnsureHooksInstalled_RewritesAForeignDaemonsEntryInPlace is the #1178
// obligation in this adapter's own terms: an entry naming a different irrlichd
// is still ours by sentinel, so it is rewritten rather than duplicated.
func TestEnsureHooksInstalled_RewritesAForeignDaemonsEntryInPlace(t *testing.T) {
	path := antigravityConfigHome(t)
	foreign, err := hookbeacon.Command(foreignBinaryPath, AdapterName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensureInstalledWithCommand(foreign); err != nil {
		t.Fatalf("seed the foreign install: %v", err)
	}

	changed, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !changed {
		t.Fatal("a foreign daemon's entry was left as it was")
	}

	handlers := ourHandlers(t, path, HookEventStop)
	if len(handlers) != 1 {
		t.Fatalf("Stop holds %d handlers, want 1 — the foreign entry was duplicated, not rewritten", len(handlers))
	}
	entry, _ := handlers[0].(map[string]interface{})
	if command, _ := entry["command"].(string); strings.Contains(command, foreignBinaryPath) {
		t.Errorf("command still names the foreign binary: %q", command)
	}
}

// TestEnsureHooksInstalled_DropsADuplicateOfOurs pins that two irrlicht
// handlers on one event converge to one: two would mean two beacon spawns per
// turn, forever.
func TestEnsureHooksInstalled_DropsADuplicateOfOurs(t *testing.T) {
	path := antigravityConfigHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}
	handlers := ourHandlers(t, path, HookEventStop)
	doc := readDoc(t, path)
	ours := doc[hookName].(map[string]interface{})
	ours[HookEventStop] = append(handlers, handlers[0])
	if err := hookjson.WriteSettings(path, doc, atomicWriteFile); err != nil {
		t.Fatal(err)
	}

	changed, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("a duplicated entry was left in place")
	}
	if n := len(ourHandlers(t, path, HookEventStop)); n != 1 {
		t.Errorf("Stop holds %d handlers after reconciliation, want 1", n)
	}
}

// TestEnsureHooksInstalled_RefusesANonObjectAtOurKey pins the one case the
// installer will not repair. A top-level "irrlicht" holding something we
// cannot model is a user's content; refusing surfaces as #1362's "granted but
// NOT applied, because <reason>" rather than destroying it.
func TestEnsureHooksInstalled_RefusesANonObjectAtOurKey(t *testing.T) {
	path := antigravityConfigHome(t)
	writeDoc(t, path, `{"`+hookName+`": "not a hook object"}`)

	changed, err := EnsureHooksInstalled()
	if err == nil {
		t.Fatal("install succeeded against a non-object at our key")
	}
	if changed {
		t.Error("install reported a change while erroring")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("error = %v, want one that says it refused rather than one that reads like a bug", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(after), "not a hook object") {
		t.Errorf("the user's content was overwritten: %s", after)
	}
}

// TestEnsureHooksInstalled_NeverOverwritesAMalformedFile pins hookjson's rule
// at this adapter's boundary: a file that does not parse is an error, and the
// bytes are left exactly as they were.
func TestEnsureHooksInstalled_NeverOverwritesAMalformedFile(t *testing.T) {
	path := antigravityConfigHome(t)
	const broken = `{ this is not json`
	writeDoc(t, path, broken)

	if _, err := EnsureHooksInstalled(); err == nil {
		t.Fatal("install succeeded against a malformed file")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != broken {
		t.Errorf("the malformed file was rewritten:\n%s", after)
	}
}

func TestUninstallHooks_RemovesOnlyOurs(t *testing.T) {
	path := antigravityConfigHome(t)
	writeDoc(t, path, `{
  "lint-checker": { "PostToolUse": [ { "matcher": "*", "hooks": [ { "command": "./lint.sh" } ] } ] }
}
`)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}

	changed, err := UninstallHooks()
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !changed {
		t.Error("uninstall reported no change")
	}

	doc := readDoc(t, path)
	if _, present := doc[hookName]; present {
		t.Errorf("our named hook survived uninstall: %v", doc)
	}
	if _, present := doc["lint-checker"]; !present {
		t.Errorf("uninstall removed the user's own named hook: %v", doc)
	}

	changed, err = UninstallHooks()
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("a second uninstall reported a change")
	}
}

// TestUninstallHooks_KeepsForeignContentUnderOurName pins that emptying our
// event does not take a foreign key or handler with it — and that our named
// hook is only dropped once nothing of anyone's is left in it.
func TestUninstallHooks_KeepsForeignContentUnderOurName(t *testing.T) {
	path := antigravityConfigHome(t)
	writeDoc(t, path, `{
  "`+hookName+`": {
    "enabled": true,
    "Stop": [ { "type": "command", "command": "./theirs.sh" } ]
  }
}
`)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallHooks(); err != nil {
		t.Fatal(err)
	}

	doc := readDoc(t, path)
	ours, ok := doc[hookName].(map[string]interface{})
	if !ok {
		t.Fatalf("our named hook was deleted along with content that was not ours: %v", doc)
	}
	if ours["enabled"] != true {
		t.Errorf("enabled = %v, want true preserved", ours["enabled"])
	}
	handlers, _ := ours[HookEventStop].([]interface{})
	if len(handlers) != 1 {
		t.Fatalf("Stop holds %d handlers, want the 1 that is not ours", len(handlers))
	}
	entry, _ := handlers[0].(map[string]interface{})
	if got, _ := entry["command"].(string); got != "./theirs.sh" {
		t.Errorf("surviving handler is %q, want ./theirs.sh", got)
	}
}

// TestUninstallHooks_LeavesTheFileBehind pins the deliberate difference from
// #1371's "give the user their file back": this document is shared with
// Antigravity's own /hooks command, so removing the FILE is not irrlicht's to
// do even when our removal empties it.
func TestUninstallHooks_LeavesTheFileBehind(t *testing.T) {
	path := antigravityConfigHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallHooks(); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the hooks file was deleted: %v", err)
	}
	doc := readDoc(t, path)
	if len(doc) != 0 {
		t.Errorf("document = %v, want an empty object", doc)
	}
	if strings.Contains(string(body), hookName) {
		t.Errorf("the file still names %q: %s", hookName, body)
	}
}

// TestUninstallHooks_IsNotScopedToTheInstallingDaemon is #1178's fourth
// obligation: `irrlichd --uninstall-hooks` run from one daemon removes what
// another wrote. Identification is by sentinel, which names no binary path.
func TestUninstallHooks_IsNotScopedToTheInstallingDaemon(t *testing.T) {
	path := antigravityConfigHome(t)
	foreign, err := hookbeacon.Command(foreignBinaryPath, AdapterName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensureInstalledWithCommand(foreign); err != nil {
		t.Fatal(err)
	}

	changed, err := UninstallHooks()
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("uninstall left another daemon's entry behind")
	}
	if doc := readDoc(t, path); len(doc) != 0 {
		t.Errorf("document = %v, want empty", doc)
	}
}

// --- verify ---

// TestVerifyAgreesWithEnsureInstalled is the equivalence hookjson's own
// entryIsStale/matcherIsStale pair documents, held for this adapter's merge:
// Verify must report damage exactly when EnsureHooksInstalled would repair it.
//
// Stricter than the repair is a verifier loop that writes on every pass and
// never converges; laxer is a clobbered install reading healthy in the
// diagnostics bundle. Both are worse than not checking, so the corpus grades
// the two answers against each other rather than against a hand-written
// expectation.
// mustInstall runs the install and fails the test if it errors.
func mustInstall(t *testing.T) {
	t.Helper()
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}
}

// mustSeedForeignInstall writes the entry a DIFFERENTLY-situated daemon would
// have installed — the address-free counterpart of seeding a stale port.
func mustSeedForeignInstall(t *testing.T) {
	t.Helper()
	foreign, err := hookbeacon.Command(foreignBinaryPath, AdapterName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensureInstalledWithCommand(foreign); err != nil {
		t.Fatal(err)
	}
}

// mustWriteDoc persists a decoded document through the same writer the
// installer uses, so a seeded corpus row cannot accidentally produce bytes the
// installer would never have written.
func mustWriteDoc(t *testing.T, path string, doc map[string]interface{}) {
	t.Helper()
	if err := hookjson.WriteSettings(path, doc, atomicWriteFile); err != nil {
		t.Fatal(err)
	}
}

// tamperWithInstalledEntry installs, then lets break mutate OUR handler object
// in place, then writes it back. Three corpus rows differ only in that one
// closure, and spelling the install/read/write dance out per row hid that.
func tamperWithInstalledEntry(t *testing.T, path string, break_ func(entry map[string]interface{})) {
	t.Helper()
	mustInstall(t)
	doc := readDoc(t, path)
	ours, ok := doc[hookName].(map[string]interface{})
	if !ok {
		t.Fatalf("no %q object to tamper with: %v", hookName, doc)
	}
	handlers, ok := ours[HookEventStop].([]interface{})
	if !ok || len(handlers) == 0 {
		t.Fatalf("no %q handlers to tamper with: %v", HookEventStop, ours)
	}
	entry, ok := handlers[0].(map[string]interface{})
	if !ok {
		t.Fatalf("handler is %T, not an object", handlers[0])
	}
	break_(entry)
	mustWriteDoc(t, path, doc)
}

func TestVerifyAgreesWithEnsureInstalled(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(t *testing.T, path string)
	}{
		{"no file at all", func(*testing.T, string) {}},
		{"empty file", func(t *testing.T, path string) { writeDoc(t, path, "") }},
		{"empty object", func(t *testing.T, path string) { writeDoc(t, path, "{}") }},
		{"only the user's hooks", func(t *testing.T, path string) {
			writeDoc(t, path, `{"lint":{"Stop":[{"command":"./x.sh"}]}}`)
		}},
		{"our name, no events", func(t *testing.T, path string) {
			writeDoc(t, path, `{"`+hookName+`":{}}`)
		}},
		{"our name, empty Stop array", func(t *testing.T, path string) {
			writeDoc(t, path, `{"`+hookName+`":{"Stop":[]}}`)
		}},
		{"our name, Stop is not an array", func(t *testing.T, path string) {
			writeDoc(t, path, `{"`+hookName+`":{"Stop":"nope"}}`)
		}},
		{"a fresh, correct install", func(t *testing.T, _ string) {
			mustInstall(t)
		}},
		{"a foreign daemon's entry", func(t *testing.T, _ string) {
			mustSeedForeignInstall(t)
		}},
		{"our entry with the wrong timeout", func(t *testing.T, path string) {
			tamperWithInstalledEntry(t, path, func(entry map[string]interface{}) {
				entry["timeout"] = 60
			})
		}},
		{"our entry with the inert result stripped", func(t *testing.T, path string) {
			tamperWithInstalledEntry(t, path, func(entry map[string]interface{}) {
				command, _ := entry["command"].(string)
				entry["command"] = strings.TrimSuffix(command, inertResultSuffix)
			})
		}},
		{"our entry with a stray extra key", func(t *testing.T, path string) {
			tamperWithInstalledEntry(t, path, func(entry map[string]interface{}) {
				entry["matcher"] = "*"
			})
		}},
		{"two copies of ours", func(t *testing.T, path string) {
			mustInstall(t)
			doc := readDoc(t, path)
			ours := doc[hookName].(map[string]interface{})
			arr := ours[HookEventStop].([]interface{})
			ours[HookEventStop] = append(arr, arr[0])
			mustWriteDoc(t, path, doc)
		}},
		{"ours plus a foreign handler", func(t *testing.T, path string) {
			writeDoc(t, path, `{"`+hookName+`":{"Stop":[{"command":"./theirs.sh"}]}}`)
			mustInstall(t)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := antigravityConfigHome(t)
			tc.seed(t, path)

			status, verifyErr := VerifyHooksInstalled()
			damaged := verifyErr == nil && (len(status.Missing) > 0 || len(status.Stale) > 0)

			wouldWrite, ensureErr := EnsureHooksInstalled()

			switch {
			case verifyErr != nil && ensureErr == nil:
				t.Fatalf("Verify errored (%v) where EnsureInstalled succeeded", verifyErr)
			case verifyErr == nil && ensureErr != nil:
				t.Fatalf("EnsureInstalled errored (%v) where Verify reported %+v", ensureErr, status)
			case verifyErr != nil:
				return // both refused; nothing to compare
			}

			if damaged != wouldWrite {
				t.Errorf("Verify says damaged=%v (%+v) but EnsureInstalled would write=%v — the two "+
					"definitions of \"not what we would install today\" have drifted",
					damaged, status, wouldWrite)
			}
		})
	}
}

// TestVerifyHooksInstalled_ReportsMissingAndStaleSeparately pins the two
// buckets, which the diagnostics bundle prints side by side and which name
// different diagnoses: entries GONE (#1372) versus entries in the wrong shape.
func TestVerifyHooksInstalled_ReportsMissingAndStaleSeparately(t *testing.T) {
	path := antigravityConfigHome(t)

	status, err := VerifyHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Missing) != 1 || status.Missing[0] != HookEventStop {
		t.Errorf("Missing = %v on a machine with no hooks file, want [%q]", status.Missing, HookEventStop)
	}
	if len(status.Stale) != 0 {
		t.Errorf("Stale = %v, want none — a missing event cannot also be stale", status.Stale)
	}

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}
	status, err = VerifyHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Missing) != 0 || len(status.Stale) != 0 {
		t.Errorf("a fresh install verifies as %+v, want clean", status)
	}

	doc := readDoc(t, path)
	ours := doc[hookName].(map[string]interface{})
	entry := ours[HookEventStop].([]interface{})[0].(map[string]interface{})
	entry["timeout"] = 99
	if err := hookjson.WriteSettings(path, doc, atomicWriteFile); err != nil {
		t.Fatal(err)
	}
	status, err = VerifyHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Stale) != 1 || status.Stale[0] != HookEventStop {
		t.Errorf("Stale = %v after a tampered entry, want [%q]", status.Stale, HookEventStop)
	}
	if len(status.Missing) != 0 {
		t.Errorf("Missing = %v, want none", status.Missing)
	}
}

// --- consent ---

// hooksPermission returns the real declared hooks permission's closures.
func hooksPermission(t *testing.T) (apply, remove func() error) {
	t.Helper()
	for _, p := range Agent().Permissions {
		if p.Key == PermissionKeyHooks {
			return p.Apply, p.Remove
		}
	}
	t.Fatal("no hooks permission declared")
	return nil, nil
}

// TestHooksPermission_IsGated wires the install-type flavour of the #797
// contract: nothing is written while the permission is pending, granting
// installs our named hook, and denying removes it.
//
// The stake is specific to this adapter and worth naming. What Apply writes is
// not a file irrlicht owns — it is a merge into a document the user also
// writes with Antigravity's own /hooks command. A pre-consent daemon that ran
// Apply would have edited a user's hand-maintained config before the wizard
// was ever answered, which is the shape of #1449's incident with the blast
// radius pointed at a shared file.
func TestHooksPermission_IsGated(t *testing.T) {
	antigravityConfigHome(t)
	apply, remove := hooksPermission(t)

	state := permission.StatePending
	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		Key: PermissionKeyHooks,
		// transcripts is the adapter's only other declared permission and is
		// observe-kind, so it has no closure to drive: the key-isolation arm
		// is INERT here and repeats the revoked arm exactly, the same
		// situation pi's, copilot's, geminicli's and vibe's equivalent tests
		// document. Install-type wirings hold their own permission's closures,
		// so a wrong key is not representable — the arm is load-bearing at the
		// live receiver (hooks_test.go), not here.
		OtherKeys: []string{PermissionKeyTranscripts},
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
