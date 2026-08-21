// hook_endpoint_toml_test.go exercises AssertHookEndpointFollowsBindAddr's
// ReadEntries/EndpointOfRaw seam (issue #1718's audit of #1716's own
// EntriesOf) against a reference TOML-shaped reference installer — the same
// role hook_endpoint_addressfree_test.go's referenceBeaconInstaller and
// hook_endpoint_flat_test.go's referenceFlatInstaller play for their own
// routes: proof the seam works, built and driven BEFORE any real adapter
// declares it, so it is not "exemption with no test behind it" for the
// months between the seam landing and mistral-vibe's own PR (#1733) adopting
// it.
//
// It is scaffolding, and says so exactly as those two files do: once a real
// TOML-writing adapter wires ReadEntries/EndpointOfRaw for its own hooktoml
// package, that adapter's wiring becomes the honest coverage and this file
// should be reduced to whatever it still uniquely proves, or deleted.
//
// Two things this file is deliberately NOT trying to do. It is not a TOML
// parser, and it does not attempt to be spec-compliant: hooktoml itself never
// produces a decoded generic structure (#1718's audit — "works entirely as
// byte-range edits over the original file bytes"), so a reference installer
// exercising the same seam has to be a byte-range scanner too, or it would be
// proving the wrong shape. And it declares exactly ONE event
// (referenceTOMLEvents), matching #1718's audit finding that hooktoml has no
// per-event disambiguation today (a flat `[[hooks]]` sequence, one event
// installed) — see referenceTOMLReadEntries's doc comment for the decision
// this settles: the seam does NOT require per-event lookup, an adapter's
// ReadEntries is free to ignore the event parameter when its format has
// nothing to filter on, and HookInstaller.ReadEntries already documents this
// (the file comment there, added alongside this one).
package contracttesting

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/pkg/daemonaddr"
)

// referenceTOMLAdapter is this fixture's adapter segment, distinct from every
// other reference installer's so a wiring mistake sorts to the wrong fixture
// loudly rather than silently sharing state.
const referenceTOMLAdapter = "reference-toml"

// referenceTOMLHomeEnv relocates the fixture's config home.
const referenceTOMLHomeEnv = "IRRLICHT_TEST_REFERENCE_TOML_HOME"

// referenceTOMLSentinel is the port-independent substring marking our
// [[hooks]] block. Unlike the JSON reference installers, which reuse
// hookbeacon.Sentinel, this one is a plain constant: DeliveryURL mode (this
// fixture's route — see referenceTOMLInstaller) needs no beacon at all, and a
// TOML adapter's own sentinel is exactly this kind of arbitrary marker
// string, matched by bytes.Contains rather than parsed out of a named field
// (#1718's own findOurBlock convention).
const referenceTOMLSentinel = "managed-by-irrlicht-reference-toml-v1"

// referenceTOMLEvents declares exactly one event, deliberately: see the file
// comment for why that is the shape being proven, not a simplification.
var referenceTOMLEvents = []string{"postToolUse"}

// referenceTOMLHome mirrors referenceBeaconHome/referenceFlatHome's own
// reasoning exactly, including the deliberately empty default.
func referenceTOMLHome() (string, error) {
	return agentpaths.AbsRoot(agentpaths.FromEnv(referenceTOMLAdapter, referenceTOMLHomeEnv, ""))
}

func referenceTOMLConfigPath() (string, error) {
	home, err := referenceTOMLHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "hooks.toml"), nil
}

// referenceTOMLDeliveryNow renders the command line this installer would
// write for whatever bind address daemonaddr.EnvBindAddr currently names —
// the same "read the env, render the string" shape deliveryOn drives every
// reference installer through.
//
// The sentinel is embedded IN the delivery string itself (a query
// parameter), not appended as a separate comment line: assertDeliveryIsOurs
// (hook_endpoint.go) requires the string EndpointOf/EndpointOfRaw returns to
// carry Sentinel directly, the same way a real DeliveryURL adapter's url or
// command line carries it — a beacon adapter gets this for free because
// hookbeacon.Sentinel is itself a substring of the rendered command, and a
// URL adapter has to render it into the endpoint the same way.
func referenceTOMLDeliveryNow() string {
	port := daemonaddr.PortOf(os.Getenv(daemonaddr.EnvBindAddr))
	return fmt.Sprintf("curl -sf http://127.0.0.1:%d/api/v1/hooks?agent=%s", port, referenceTOMLSentinel)
}

// referenceTOMLEntry/referenceTOMLEndpointOf are the contract's in-memory
// Entry/EndpointOf pair — obligation 1 (deliveriesOnBothAddrs) never touches
// the filesystem or ReadEntries/EndpointOfRaw at all, exactly as
// HookInstaller.EndpointOfRaw's own doc comment states: "an adapter can
// honestly answer that question with a map even when what it actually
// persists to disk is raw TOML text."
func referenceTOMLEntry(delivery string) map[string]interface{} {
	return map[string]interface{}{"command": delivery}
}

func referenceTOMLEndpointOf(hook map[string]interface{}) string {
	c, _ := hook["command"].(string)
	return c
}

func referenceTOMLEntryNow() map[string]interface{} {
	return referenceTOMLEntry(referenceTOMLDeliveryNow())
}

// referenceTOMLBlock renders one canonical `[[hooks]]` block for delivery, as
// raw bytes — the unit scanTOMLHookBlocks splits a file into and the unit
// referenceTOMLEnsureInstalled compares whole. The command value is rendered
// with %q, which for these delivery strings (no quotes, no backslashes)
// produces exactly a TOML basic string. No separate sentinel comment line is
// needed: a canonical delivery already carries Sentinel (see
// referenceTOMLDeliveryNow), so bytes.Contains(block, sentinel) — what
// hasOurRawEntry actually checks — finds it inside the command value.
func referenceTOMLBlock(delivery string) []byte {
	return []byte(fmt.Sprintf(
		"[[hooks]]\nevent = %q\ncommand = %q\n",
		referenceTOMLEvents[0], delivery,
	))
}

func referenceTOMLBlockNow() []byte {
	return referenceTOMLBlock(referenceTOMLDeliveryNow())
}

// scanTOMLHookBlocks splits data into its `[[hooks]]` blocks, each block
// running from its own marker line to the line before the next marker or
// EOF. It is a byte-range scanner, not a TOML parser, matching hooktoml's own
// approach (#1718's audit).
//
// It REFUSES rather than silently returning fewer entries than the file
// actually holds when a block's command value is not a well-formed TOML
// basic string — specifically, an opened `command = "` with no closing `"`
// before the block ends. That is the one failure mode a byte-range scanner
// can hit that a generic-structure parser would report as a parse error, and
// it is exactly the state a truncated or corrupted write leaves behind: an
// unclosed string reads, to a scanner with no validation, as "no command
// field" — silently indistinguishable from an entry that was never given
// one. TestReferenceTOMLReadEntries_RejectsUnterminatedCommand is the
// committed mutation evidence for this refusal; see its own comment for what
// it proves and why the alternative (endpointOfRaw quietly returning "") is
// the wrong place to catch it.
func scanTOMLHookBlocks(data []byte) ([][]byte, error) {
	const marker = "[[hooks]]"
	var blocks [][]byte
	lines := bytes.SplitAfter(data, []byte("\n"))
	var current []byte
	inBlock := false
	flush := func() {
		if inBlock {
			blocks = append(blocks, current)
			current = nil
		}
	}
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == len(marker) && string(bytes.TrimSpace(line)) == marker {
			flush()
			inBlock = true
			current = append([]byte{}, line...)
			continue
		}
		if inBlock {
			current = append(current, line...)
		}
	}
	flush()

	for _, b := range blocks {
		if !balancedCommandQuotes(b) {
			return nil, fmt.Errorf("malformed [[hooks]] block: command value has no closing quote: %q", b)
		}
	}
	return blocks, nil
}

// balancedCommandQuotes reports whether block's "command = " value, if any,
// is closed by a matching quote. A block with no "command = " at all is not
// this function's business — an entry legitimately naming no command is a
// different, EndpointOfRaw-visible problem ("" carries no Sentinel, so
// assertDeliveryIsOurs catches it) — this only refuses the case where a value
// was clearly STARTED and never finished.
func balancedCommandQuotes(block []byte) bool {
	const marker = "command = \""
	i := bytes.Index(block, []byte(marker))
	if i < 0 {
		return true
	}
	rest := block[i+len(marker):]
	return bytes.IndexByte(rest, '"') >= 0
}

// referenceTOMLReadEntries is the contract's ReadEntries: it reads the whole
// file and returns every `[[hooks]]` block's raw bytes, IGNORING event.
//
// That is the seam's answer to #1718's per-event disambiguation gap: this
// format has no field the entries could be filtered on (referenceTOMLEvents
// declares exactly one), so there is nothing for the parameter to select
// between, and ReadEntries is free to return everything every time. A format
// that DID need to disambiguate would filter here — the parameter exists for
// exactly that adapter — but the parameter's presence does not obligate every
// implementation to use it.
func referenceTOMLReadEntries(path, _ string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return scanTOMLHookBlocks(data)
}

// referenceTOMLEndpointOfRaw extracts the command value out of one raw
// [[hooks]] block. Entries reaching this function have already passed
// scanTOMLHookBlocks's balancedCommandQuotes check, so the closing quote this
// looks for is guaranteed to exist for a well-formed entry; a block with no
// "command = " field at all yields "", which assertDeliveryIsOurs (in
// hook_endpoint.go) is what turns into a loud failure rather than a silent
// pass — see that function's own comment for why an empty, sentinel-free
// delivery cannot slip through unnoticed.
func referenceTOMLEndpointOfRaw(entry []byte) string {
	const marker = "command = \""
	i := bytes.Index(entry, []byte(marker))
	if i < 0 {
		return ""
	}
	rest := entry[i+len(marker):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return string(rest[:j])
}

func referenceTOMLWriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// referenceTOMLEnsureInstalled rebuilds the file's block set from scratch —
// every non-sentinel block kept verbatim, one fresh canonical block
// appended — and compares the result byte-for-byte against what was already
// on disk before writing, which is what makes a same-address second call
// idempotent (modified=false) without a separate "is this already canonical"
// predicate. That mirrors #1718's own audit of IsCanonical: "works by exact
// []byte comparison against a freshly-rendered canonical block."
func referenceTOMLEnsureInstalled() (bool, error) {
	path, err := referenceTOMLConfigPath()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	blocks, err := scanTOMLHookBlocks(data)
	if err != nil {
		return false, err
	}
	var rest [][]byte
	for _, b := range blocks {
		if !bytes.Contains(b, []byte(referenceTOMLSentinel)) {
			rest = append(rest, b)
		}
	}
	rest = append(rest, referenceTOMLBlockNow())
	newData := bytes.Join(rest, nil)
	if bytes.Equal(newData, data) {
		return false, nil
	}
	if err := referenceTOMLWriteFile(path, newData); err != nil {
		return false, err
	}
	return true, nil
}

// referenceTOMLUninstall drops every sentinel-bearing block, whatever
// delivery it names — not scoped to the currently resolved bind address,
// matching every other reference installer's uninstall.
func referenceTOMLUninstall() (bool, error) {
	path, err := referenceTOMLConfigPath()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	blocks, err := scanTOMLHookBlocks(data)
	if err != nil {
		return false, err
	}
	var rest [][]byte
	removed := false
	for _, b := range blocks {
		if bytes.Contains(b, []byte(referenceTOMLSentinel)) {
			removed = true
			continue
		}
		rest = append(rest, b)
	}
	if !removed {
		return false, nil
	}
	if err := referenceTOMLWriteFile(path, bytes.Join(rest, nil)); err != nil {
		return false, err
	}
	return true, nil
}

// referenceTOMLInstaller is the whole TOML-shape wiring, as one value —
// mirroring referenceBeaconInstaller/referenceFlatInstaller's own role.
// DeliveryURL, not DeliveryAddressFree: this fixture is proving the
// ReadEntries/EndpointOfRaw seam, which is independent of delivery mode, and
// DeliveryURL needs no ForeignInstall/hookbeacon machinery, keeping the two
// concerns separate.
func referenceTOMLInstaller() HookInstaller {
	return HookInstaller{
		Delivery: DeliveryURL,
		SettingsPath: func(t *testing.T) string {
			t.Setenv(referenceTOMLHomeEnv, t.TempDir())
			path, err := referenceTOMLConfigPath()
			if err != nil {
				t.Fatalf("resolving the reference TOML hooks path: %v", err)
			}
			return path
		},
		Sentinel:        referenceTOMLSentinel,
		Events:          referenceTOMLEvents,
		Entry:           referenceTOMLEntryNow,
		EndpointOf:      referenceTOMLEndpointOf,
		EnsureInstalled: referenceTOMLEnsureInstalled,
		Uninstall:       referenceTOMLUninstall,
		ReadEntries:     referenceTOMLReadEntries,
		EndpointOfRaw:   referenceTOMLEndpointOfRaw,
		// EntriesOf is deliberately left nil: this fixture never produces a
		// decoded map[string]interface{} for its on-disk form at all, which
		// is the whole point being proven (see the file comment).
	}
}

// TestTOMLEntryShapeContract is the vacuity guard: the full #1178 contract,
// run against a CORRECT TOML-shape installer, end to end, via
// AssertHookEndpointFollowsBindAddr itself.
func TestTOMLEntryShapeContract(t *testing.T) {
	AssertHookEndpointFollowsBindAddr(t, referenceTOMLInstaller())
}

// TestReferenceTOMLReadEntries_RejectsUnterminatedCommand is the mutation
// evidence HookInstaller.ReadEntries's doc comment promises: a shape this
// scanner cannot parse is refused with an error, not silently read as zero
// (or one empty) entries.
//
// The malformed fixture below opens `command = "` and never closes it before
// the block ends — the state a truncated write leaves behind. Without
// balancedCommandQuotes's check, scanTOMLHookBlocks would return the block
// unmodified (a scanner with no validation has no way to know a field is
// missing versus cut off), referenceTOMLEndpointOfRaw would then find no
// closing quote and answer "", and that "" would reach assertDeliveryIsOurs
// as an ordinary empty delivery — reported as "does not carry Sentinel", the
// SAME message a genuinely blank entry produces. That message is correct for
// an entry with no command at all; it is the wrong diagnosis for a file that
// is corrupted, and it is why the refusal belongs at the READ step rather
// than being left for EndpointOfRaw's caller to notice downstream.
func TestReferenceTOMLReadEntries_RejectsUnterminatedCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	malformed := []byte("[[hooks]]\nevent = \"postToolUse\"\ncommand = \"curl -sf http://127.0.0.1:7837/api/v1/hooks\n# " + referenceTOMLSentinel + "\n")
	if err := os.WriteFile(path, malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := referenceTOMLReadEntries(path, referenceTOMLEvents[0])
	if err == nil {
		t.Fatalf("ReadEntries on a block with an unterminated command string: got %d entries and no error, want a refusal", len(entries))
	}
}

// TestReferenceTOMLReadEntries_EmptyFileIsZeroEntriesNotAnError is
// RejectsUnterminatedCommand's own vacuity guard: a genuinely empty (or
// absent) settings file is the everyday "nothing installed yet" state, not a
// parse failure, and must not trip the refusal above.
func TestReferenceTOMLReadEntries_EmptyFileIsZeroEntriesNotAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")

	entries, err := referenceTOMLReadEntries(path, referenceTOMLEvents[0])
	if err != nil {
		t.Fatalf("ReadEntries on a nonexistent file: %v, want nil error", err)
	}
	if len(entries) != 0 {
		t.Fatalf("ReadEntries on a nonexistent file: got %d entries, want 0", len(entries))
	}

	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err = referenceTOMLReadEntries(path, referenceTOMLEvents[0])
	if err != nil {
		t.Fatalf("ReadEntries on an empty file: %v, want nil error", err)
	}
	if len(entries) != 0 {
		t.Fatalf("ReadEntries on an empty file: got %d entries, want 0", len(entries))
	}
}

// TestReferenceTOMLReadEntries_IgnoresEventArgument is a lock (passes by
// construction) on the per-event-lookup decision documented on
// HookInstaller.ReadEntries and in this file's header: this format has
// nothing to filter an event by, so ReadEntries returns the same blocks
// regardless of what event is asked for, deliberately.
func TestReferenceTOMLReadEntries_IgnoresEventArgument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	if err := os.WriteFile(path, referenceTOMLBlock("curl -sf http://127.0.0.1:7837/api/v1/hooks"), 0o600); err != nil {
		t.Fatal(err)
	}

	declared, err := referenceTOMLReadEntries(path, referenceTOMLEvents[0])
	if err != nil {
		t.Fatalf("ReadEntries(declared event): %v", err)
	}
	other, err := referenceTOMLReadEntries(path, "some-event-name-this-format-never-declared")
	if err != nil {
		t.Fatalf("ReadEntries(unrelated event name): %v", err)
	}
	if len(declared) != 1 || len(other) != 1 {
		t.Fatalf("got %d and %d entries for two different event arguments, want 1 and 1 — this format has no per-event field to filter on", len(declared), len(other))
	}
}
