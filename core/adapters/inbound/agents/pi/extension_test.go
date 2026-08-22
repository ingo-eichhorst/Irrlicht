// extension_test.go grades the ARTIFACT this adapter ships — the JavaScript
// irrlicht writes into a user's pi installation — rather than the Go code
// that writes it.
//
// It exists because that artifact is the one thing in this repository that
// no other mechanism can see. `go vet`, `go test`, the architecture tests
// and every contract family reason about Go; extension.js is an embedded
// string that compiles no matter what it says. The guards below are what
// stands between "the Go side is correct" and "the thing that actually runs
// inside the user's agent is correct", and each one names the specific
// failure it would catch.
//
// What is NOT covered here, stated rather than implied: nothing in this file
// executes the JavaScript. Running it needs either node (which `go test
// ./core/...` does not otherwise require, and a gate that could be absent is
// the failure mode docs/testing-philosophy.md is about) or pi itself (which
// needs a model call). The end-to-end proof was therefore run by hand during
// #1721 and reported in the PR: the rendered extension, loaded by pi 0.83.0
// from an isolated PI_CODING_AGENT_DIR, delivered exactly one payload to a
// stub beacon at agent_settled. These guards pin the properties that
// live run confirmed, so a later edit that breaks one of them is caught
// without re-running it.
package pi

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"irrlicht/core/pkg/hookbeacon"
)

// piOnCallRE finds every event subscription in the shipped extension.
var piOnCallRE = regexp.MustCompile(`pi\.on\(\s*([A-Za-z_][A-Za-z0-9_]*|"[^"]*")`)

// jsPayloadKeyRE finds the keys of the object literal extension.js hands to
// JSON.stringify — the wire contract between the shipped JavaScript and
// piHookPayload's json tags.
var jsPayloadKeyRE = regexp.MustCompile(`(?m)^\s*post\(\{ (.*) \}\);$`)

// importRE finds every module specifier the extension imports.
var importRE = regexp.MustCompile(`from "([^"]+)"`)

// TestShippedExtensionSubscribesToNoBlockingEvent is the guard with the
// largest blast radius, and it is not a style rule.
//
// pi's runner dispatches every extension event inside a try/catch and logs a
// throwing handler — every event EXCEPT tool_call. Read
// dist/core/extensions/runner.js: emitToolCall's loop has no catch, and a
// handler that returns {block: true} blocks the user's tool call outright.
// A monitoring extension that could throw there, or that returned the wrong
// shape, would break coding sessions rather than merely fail to report
// them. So the shipped file must not mention that event at all.
func TestShippedExtensionSubscribesToNoBlockingEvent(t *testing.T) {
	for _, blocking := range []string{"tool_call", "project_trust", "session_before_switch",
		"session_before_fork", "session_before_compact", "session_before_tree",
		"user_bash", "input", "before_agent_start", "message_end", "tool_result", "context"} {
		if strings.Contains(shippedExtensionCode(t), blocking) {
			t.Errorf("extension.js references pi's %q event, which can block, cancel, "+
				"replace or rewrite what the agent is doing. A monitoring extension must "+
				"only observe.", blocking)
		}
	}
}

// TestShippedExtensionSubscribesExactlyToInstalledHookEvents binds the
// JavaScript to the Go declaration. Without it the two are independent
// lists: installedHookEvents drives the consent copy, the version floor and
// every contract family, while the file that actually runs could subscribe
// to something else entirely and nothing would notice — the disclosure would
// be a promise about an event nobody subscribed to.
func TestShippedExtensionSubscribesExactlyToInstalledHookEvents(t *testing.T) {
	src := shippedExtensionCode(t)

	consts := jsStringConsts(src)
	var subscribed []string
	for _, m := range piOnCallRE.FindAllStringSubmatch(src, -1) {
		arg := m[1]
		if strings.HasPrefix(arg, `"`) {
			subscribed = append(subscribed, strings.Trim(arg, `"`))
			continue
		}
		v, ok := consts[arg]
		if !ok {
			t.Fatalf("extension.js subscribes via identifier %q, which is not a "+
				"top-level string constant this test can resolve — the binding to "+
				"installedHookEvents cannot be checked, which is worse than it failing", arg)
		}
		subscribed = append(subscribed, v)
	}
	if len(subscribed) == 0 {
		t.Fatal("found no pi.on(...) call in extension.js; the guard read nothing rather " +
			"than finding nothing wrong")
	}

	want := append([]string(nil), installedHookEvents...)
	sort.Strings(want)
	sort.Strings(subscribed)
	if !reflect.DeepEqual(subscribed, want) {
		t.Errorf("extension.js subscribes to %v, installedHookEvents declares %v — the "+
			"consent copy, the version floor and every contract family are written "+
			"against the second list", subscribed, want)
	}
}

// TestShippedExtensionPayloadMatchesTheReceiversFields pins the wire
// contract across the language boundary: every key the JavaScript sends is a
// field piHookPayload decodes, and every field piHookPayload decodes is a
// key the JavaScript sends.
//
// This is the defect class no Go test can otherwise see. A renamed json tag
// on the Go side compiles, passes every contract family (they all build
// their bodies by marshaling piHookPayload itself, so they rename in
// lockstep), and silently stops decoding the only body that exists in
// production.
func TestShippedExtensionPayloadMatchesTheReceiversFields(t *testing.T) {
	src := shippedExtensionCode(t)
	m := jsPayloadKeyRE.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("could not find extension.js's post({...}) call; the guard read nothing " +
			"rather than finding nothing wrong")
	}
	var sent []string
	for _, pair := range strings.Split(m[1], ",") {
		key := strings.TrimSpace(strings.SplitN(pair, ":", 2)[0])
		if key != "" {
			sent = append(sent, key)
		}
	}

	var decoded []string
	rt := reflect.TypeOf(piHookPayload{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		name := strings.SplitN(tag, ",", 2)[0]
		if name == "" || name == "-" {
			continue
		}
		decoded = append(decoded, name)
	}

	sort.Strings(sent)
	sort.Strings(decoded)
	if !reflect.DeepEqual(sent, decoded) {
		t.Errorf("extension.js sends %v, piHookPayload decodes %v — a field on either "+
			"side that the other does not have is a silently dropped hook", sent, decoded)
	}
}

// TestShippedExtensionImportsOnlyNodeBuiltins is the supply-chain guard.
// The whole argument for shipping code into a user's agent (hookinstaller.go's
// header) rests on the artifact having no dependencies: an import of anything
// resolvable from node_modules would make this an install with a dependency
// graph, which is the shape epic #1355 cut opencode over.
func TestShippedExtensionImportsOnlyNodeBuiltins(t *testing.T) {
	found := importRE.FindAllStringSubmatch(shippedExtensionCode(t), -1)
	if len(found) == 0 {
		t.Fatal("found no import in extension.js; the guard read nothing rather than " +
			"finding nothing wrong (it imports node:child_process)")
	}
	for _, m := range found {
		if !strings.HasPrefix(m[1], "node:") {
			t.Errorf("extension.js imports %q — only node: builtins may be imported, or "+
				"this install acquires a dependency graph", m[1])
		}
	}
}

// TestRenderedExtensionCarriesTheCommandAndNothingAddressShaped is the
// address-free property applied to the artifact itself rather than to a
// config entry: the whole rendered file must contain nothing that looks like
// a host or a port, which is a strictly stronger claim than the #1178
// contract's per-entry one.
func TestRenderedExtensionCarriesTheCommandAndNothingAddressShaped(t *testing.T) {
	command, err := hookbeacon.Command("/opt/irrlicht/bin/irrlichd", AdapterName)
	if err != nil {
		t.Fatalf("rendering the beacon command: %v", err)
	}
	rendered, err := renderExtension(command)
	if err != nil {
		t.Fatalf("renderExtension: %v", err)
	}

	got, ok := beaconCommandOf(rendered)
	if !ok {
		t.Fatal("the rendered extension does not carry a readable beacon command")
	}
	if got != command {
		t.Errorf("round-trip: rendered %q, read back %q", command, got)
	}
	if !strings.Contains(string(rendered), hookbeacon.Sentinel(AdapterName)) {
		t.Error("the rendered extension does not carry the beacon sentinel, so uninstall " +
			"would not recognize it as ours")
	}
	if m := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+|:\d{4,5}\b`).FindString(string(rendered)); m != "" {
		t.Errorf("the rendered extension carries %q, which is address-shaped; beacon "+
			"delivery exists so nothing address-shaped is ever written into a user's agent", m)
	}
}

// TestRenderedExtensionSurvivesAHostilePath is the property the substitution
// exists for. The beacon command embeds an absolute filesystem path the user
// chose, and it lands inside a JavaScript string literal — a path containing
// a quote, a backslash or a newline must not be able to terminate that
// literal and turn the rest of the file into code.
func TestRenderedExtensionSurvivesAHostilePath(t *testing.T) {
	for _, name := range []string{
		`/tmp/irr"licht/irrlichd`,
		`/tmp/irr\licht/irrlichd`,
		"/tmp/irr\nlicht/irrlichd",
		"/tmp/irr'licht/irrlichd",
		"/tmp/irr`licht/irrlichd",
		"/tmp/irrlicht${x}/irrlichd",
	} {
		command, err := hookbeacon.Command(name, AdapterName)
		if err != nil {
			t.Fatalf("hookbeacon.Command(%q): %v", name, err)
		}
		rendered, err := renderExtension(command)
		if err != nil {
			t.Fatalf("renderExtension for %q: %v", name, err)
		}
		got, ok := beaconCommandOf(rendered)
		if !ok || got != command {
			t.Errorf("path %q: round-trip failed (ok=%v, got %q, want %q)", name, ok, got, command)
		}
		// The assignment line must still be exactly one line: a raw newline
		// escaping the literal is the failure this case exists for.
		if n := strings.Count(beaconLineOf(t, rendered), "\n"); n != 0 {
			t.Errorf("path %q: the assignment line spans %d newlines", name, n)
		}
		// And the literal must be valid JSON-ish, i.e. decodable as a JSON
		// string — the same grammar a JavaScript string literal follows for
		// every byte strconv.Quote can emit.
		var decoded string
		if err := json.Unmarshal([]byte(beaconLiteralOf(t, rendered)), &decoded); err != nil {
			t.Errorf("path %q: the rendered literal is not a valid string literal: %v", name, err)
		}
	}
}

// TestRenderExtensionRefusesWhenTheTemplateLostItsPlaceholder is the
// "fail loudly when it cannot run" guard for the substitution itself. If the
// embedded template stopped carrying the placeholder, a Replace that silently
// did nothing would install a file whose beacon command IS the literal
// placeholder — pi would then run `__IRRLICHT_BEACON_COMMAND__` as a shell
// command on every settled turn, forever, with the permission reading
// granted.
//
// The mutation is committed rather than described: the template is swapped
// for one that lost the token, and restored.
func TestRenderExtensionRefusesWhenTheTemplateLostItsPlaceholder(t *testing.T) {
	original := extensionTemplate
	t.Cleanup(func() { extensionTemplate = original })

	extensionTemplate = strings.Replace(original, beaconCommandPlaceholder, `"already-substituted"`, 1)
	if extensionTemplate == original {
		t.Fatal("the mutation changed nothing — the template no longer contains the " +
			"placeholder this test mutates, so it proves nothing")
	}
	if _, err := renderExtension("cmd"); err == nil {
		t.Error("renderExtension accepted a template with no placeholder; it would have " +
			"installed a file that shells out to the placeholder text")
	}
}

// TestRenderExtensionRefusesAnEmptyCommand is the second half of the same
// rule: an empty command would render a file that runs `sh -c ""` on every
// turn — silent, harmless-looking, and never reporting anything.
func TestRenderExtensionRefusesAnEmptyCommand(t *testing.T) {
	if _, err := renderExtension(""); err == nil {
		t.Error("renderExtension accepted an empty beacon command")
	}
}

// --- helpers ---

// shippedExtensionCode returns the embedded template with its comments
// stripped, so a guard cannot be satisfied — or defeated — by prose. The
// comments in extension.js legitimately DISCUSS tool_call; the code must not
// mention it.
func shippedExtensionCode(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, line := range strings.Split(extensionTemplate, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	out := b.String()
	if strings.TrimSpace(out) == "" {
		t.Fatal("stripping comments left no code; the guards below would read nothing")
	}
	return out
}

// jsStringConsts collects top-level `const NAME = "value";` bindings.
func jsStringConsts(src string) map[string]string {
	out := map[string]string{}
	re := regexp.MustCompile(`(?m)^const ([A-Za-z_][A-Za-z0-9_]*) = "((?:[^"\\]|\\.)*)";$`)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		var v string
		if err := json.Unmarshal([]byte(`"`+m[2]+`"`), &v); err == nil {
			out[m[1]] = v
		}
	}
	return out
}

func beaconLineOf(t *testing.T, rendered []byte) string {
	t.Helper()
	m := beaconLineRE.FindString(string(rendered))
	if m == "" {
		t.Fatal("no beacon assignment line in the rendered extension")
	}
	return m
}

func beaconLiteralOf(t *testing.T, rendered []byte) string {
	t.Helper()
	m := beaconLineRE.FindSubmatch(rendered)
	if m == nil {
		t.Fatal("no beacon assignment line in the rendered extension")
	}
	return string(m[1])
}
