// plugin_test.go grades the ARTIFACT this adapter ships — the JavaScript
// irrlicht writes into a user's opencode installation — rather than the Go code
// that writes it.
//
// It exists because that artifact is the one thing in this repository no other
// mechanism can see. `go vet`, `go test`, the architecture tests and every
// contract family reason about Go; plugin.js is an embedded string that
// compiles no matter what it says. The guards below are what stands between
// "the Go side is correct" and "the thing that actually runs inside the user's
// agent is correct", and each one names the specific failure it would catch.
//
// What is NOT covered here, stated rather than implied: nothing in this file
// executes the JavaScript. Running it needs either bun (which `go test
// ./core/...` does not otherwise require, and a gate that could be absent is
// the failure mode docs/testing-philosophy.md is about) or opencode itself
// (which needs a model call). The end-to-end proof was therefore run by hand
// during #1719 and reported in the PR. These guards pin the properties that run
// confirmed, so a later edit that breaks one of them is caught without
// re-running it.
package opencode

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// hookRegistrationRE finds the hook keys the shipped plugin registers on the
// object it returns. opencode dispatches a hook by looking its NAME up on that
// object, so the set of keys IS the set of hooks — anchored on the "<key>:"
// property position rather than searching for the name anywhere in the file, so
// a name inside a comment cannot read as a registration.
//
// The optional quotes are the whole guard, not tidiness. Ten of opencode's
// fifteen hook names contain a dot ("permission.ask", "tool.execute.before",
// …), which JavaScript cannot spell as a bare property key — so a pattern
// matching only bare keys is blind to exactly the registrations that matter,
// and `permission.ask` is the one this test exists to forbid. Measured: the
// first draft of this pattern matched only bare keys, and the committed
// mutation below (adding a `"permission.ask"` handler that writes
// output.status) passed it.
var hookRegistrationRE = regexp.MustCompile(`(?m)^\t\t"?([a-z][A-Za-z0-9.]*)"?:`)

// TestShippedPluginRegistersOnlyTheReadOnlyEventTap is the guard with the
// highest stake in this file.
//
// opencode wraps plugin LOADING in try/catch but NOT hook dispatch:
// Plugin.trigger iterates the registered hooks and awaits each one with no
// catch, and several of them receive a mutable `output` the handler is expected
// to change. `permission.ask`'s output.status is a writable "allow" | "deny" |
// "ask" — a monitoring plugin that registered it could answer a permission
// prompt on the user's behalf, or throw inside the agent loop.
//
// `event` is the one hook that receives an event and returns nothing anyone
// reads. This test fails if the shipped file ever registers a second one.
func TestShippedPluginRegistersOnlyTheReadOnlyEventTap(t *testing.T) {
	var registered []string
	for _, m := range hookRegistrationRE.FindAllStringSubmatch(pluginTemplate, -1) {
		registered = append(registered, m[1])
	}
	if len(registered) == 0 {
		t.Fatal("no hook registration found in plugin.js at all — this guard's detector has stopped matching and would pass vacuously")
	}
	if !reflect.DeepEqual(registered, []string{"event"}) {
		t.Errorf("plugin.js registers %v, want exactly [event]\n"+
			"opencode dispatches named hooks with NO try/catch, and several of them "+
			"(permission.ask above all) hand the handler a mutable output it can use to "+
			"change what opencode does. A monitoring plugin must register only the "+
			"read-only bus tap", registered)
	}
}

// TestShippedPluginForwardsExactlyTheInstalledHookEvents binds the artifact to
// the Go-side declaration.
//
// Two failures it catches, and they point in opposite directions: a plugin
// forwarding an event installedHookEvents does not list delivers a name the
// receiver's switch answers with IgnoreUnknownEvent forever, and a plugin
// forwarding fewer means the consent copy — which derives its list and its count
// from that same slice — over-promises what is installed.
func TestShippedPluginForwardsExactlyTheInstalledHookEvents(t *testing.T) {
	const marker = "const IRRLICHT_EVENTS = new Set(["
	i := strings.Index(pluginTemplate, marker)
	if i < 0 {
		t.Fatalf("plugin.js no longer declares %q — this guard cannot run", marker)
	}
	rest := pluginTemplate[i+len(marker):]
	j := strings.Index(rest, "]")
	if j < 0 {
		t.Fatal("plugin.js's IRRLICHT_EVENTS array is unterminated — this guard cannot run")
	}

	var forwarded []string
	for _, raw := range strings.Split(rest[:j], ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var name string
		if err := json.Unmarshal([]byte(raw), &name); err != nil {
			t.Fatalf("IRRLICHT_EVENTS entry %q is not a JSON string literal: %v", raw, err)
		}
		forwarded = append(forwarded, name)
	}

	want := append([]string(nil), installedHookEvents...)
	sort.Strings(want)
	sort.Strings(forwarded)
	if !reflect.DeepEqual(forwarded, want) {
		t.Errorf("plugin.js forwards %v but installedHookEvents declares %v — the shipped artifact and the consent copy disagree about what is installed",
			forwarded, want)
	}
}

// TestShippedPluginPayloadMatchesTheReceiversFields pins the wire between the
// two halves of this feature. The plugin builds its JSON by hand and the
// receiver decodes it into openCodeHookPayload; nothing in Go's type system
// connects them, so a renamed field would simply arrive as a zero value and the
// receiver would drop every hook in silence.
func TestShippedPluginPayloadMatchesTheReceiversFields(t *testing.T) {
	typ := reflect.TypeOf(openCodeHookPayload{})
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if !strings.Contains(pluginTemplate, name+":") {
			t.Errorf("openCodeHookPayload decodes %q but plugin.js never writes it — the receiver would see a zero value and drop the hook", name)
		}
	}
}

// TestShippedPluginImportsOnlyNodeBuiltins is the supply-chain guard, and it is
// the one this whole adapter's consent copy stakes a claim on: "no npm, no git,
// no build step". A third-party import would make that sentence false while
// changing nothing a Go test would otherwise notice.
//
// It also pins that @opencode-ai/plugin is NOT imported. opencode
// background-installs that package into every config directory, so importing it
// would work — and would make the shipped file depend on a resolution step
// nobody controls, for types a plain .js file cannot use anyway.
func TestShippedPluginImportsOnlyNodeBuiltins(t *testing.T) {
	importRE := regexp.MustCompile(`(?m)^import .* from "([^"]+)"`)
	found := importRE.FindAllStringSubmatch(pluginTemplate, -1)
	if len(found) == 0 {
		t.Fatal("no import found in plugin.js at all — this guard's detector has stopped matching")
	}
	for _, m := range found {
		if !strings.HasPrefix(m[1], "node:") {
			t.Errorf("plugin.js imports %q, which is not a node: builtin — the consent copy promises no package is fetched from anywhere", m[1])
		}
	}
}

// TestShippedPluginExportsOnlyAFunction pins a loader fact rather than a
// preference. opencode's loadExternal walks Object.values(module) and throws
// TypeError("Plugin export is not a function") for any export that is neither a
// function nor an object carrying server(). A second export of any other shape
// would make the whole plugin fail to load — and opencode reports that as a
// logged error while the agent continues, so the symptom would be silence.
func TestShippedPluginExportsOnlyAFunction(t *testing.T) {
	exportRE := regexp.MustCompile(`(?m)^export\b.*$`)
	lines := exportRE.FindAllString(pluginTemplate, -1)
	if len(lines) != 1 {
		t.Fatalf("plugin.js has %d top-level export lines, want exactly 1: %v", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "export default async function") {
		t.Errorf("plugin.js's only export is %q, want a default async function — opencode calls the export with (input, options) and awaits the Hooks object it returns", lines[0])
	}
}

// TestRenderedPluginCarriesTheCommandAndNothingAddressShaped is the
// DeliveryAddressFree property at the artifact level: the rendered file names
// the beacon command and contains nothing a daemon on a different port would
// have to rewrite.
func TestRenderedPluginCarriesTheCommandAndNothingAddressShaped(t *testing.T) {
	const command = `/opt/irrlicht/irrlichd --version >/dev/null && /opt/irrlicht/irrlichd hook-post opencode >/dev/null || true`
	rendered, err := renderPlugin(command)
	if err != nil {
		t.Fatalf("renderPlugin: %v", err)
	}
	got, ok := beaconCommandOf(rendered)
	if !ok {
		t.Fatal("the rendered plugin's beacon line does not read back — an installed copy could never be recognized as current")
	}
	if got != command {
		t.Errorf("read back %q, want %q", got, command)
	}
	if strings.Contains(string(rendered), beaconCommandPlaceholder) {
		t.Error("the rendered plugin still contains the placeholder — substitution did not happen")
	}
	for _, addressShaped := range []string{"127.0.0.1", "localhost", "http://", ":7837"} {
		if strings.Contains(string(rendered), addressShaped) {
			t.Errorf("the rendered plugin contains %q — a DeliveryAddressFree artifact carries no address, which is what makes the #1178 stale-port class inexpressible", addressShaped)
		}
	}
}

// TestRenderedPluginSurvivesAHostilePath is the property the substitution's use
// of strconv.Quote exists for: the command embeds an absolute path the user
// chose, and a path containing a quote or a backslash must not be able to
// terminate the JavaScript string literal it lands in.
func TestRenderedPluginSurvivesAHostilePath(t *testing.T) {
	for _, command := range []string{
		`/tmp/a"b/irrlichd hook-post opencode`,
		`/tmp/a\b/irrlichd hook-post opencode`,
		"/tmp/a\nb/irrlichd hook-post opencode",
		"/tmp/a\\\"; process.exit(1); //b/irrlichd hook-post opencode",
	} {
		rendered, err := renderPlugin(command)
		if err != nil {
			t.Fatalf("renderPlugin(%q): %v", command, err)
		}
		got, ok := beaconCommandOf(rendered)
		if !ok {
			t.Errorf("renderPlugin(%q): the beacon line does not read back", command)
			continue
		}
		if got != command {
			t.Errorf("renderPlugin(%q) read back %q — the quoting did not round-trip, which means the literal was terminated early", command, got)
		}
	}
}

// TestRenderPluginRefusesWhenTheTemplateLostItsPlaceholder is the loud-failure
// guard. A template that stopped carrying the token renderPlugin substitutes
// would otherwise be installed verbatim — and the file would then shell out to
// the literal string "__IRRLICHT_BEACON_COMMAND__" on every event.
func TestRenderPluginRefusesWhenTheTemplateLostItsPlaceholder(t *testing.T) {
	original := pluginTemplate
	t.Cleanup(func() { pluginTemplate = original })
	pluginTemplate = strings.Replace(pluginTemplate, beaconCommandPlaceholder, `"gone"`, 1)

	if _, err := renderPlugin("irrlichd hook-post opencode"); err == nil {
		t.Fatal("renderPlugin succeeded against a template with no placeholder — it would have installed a file that runs the placeholder as a command")
	}
}

// TestRenderPluginRefusesAnEmptyCommand is the second half of the same
// argument: an empty command renders a file that spawns `/bin/sh -c ""` on
// every event, forever, silently.
func TestRenderPluginRefusesAnEmptyCommand(t *testing.T) {
	if _, err := renderPlugin(""); err == nil {
		t.Fatal("renderPlugin(\"\") succeeded — the installed file would spawn an empty shell command on every opencode event")
	}
}

// TestPlaceholderAppearsExactlyOnce pins what renderPlugin's single-replacement
// substitution assumes. A second occurrence would be left in the installed file
// verbatim.
func TestPlaceholderAppearsExactlyOnce(t *testing.T) {
	if n := strings.Count(pluginTemplate, beaconCommandPlaceholder); n != 1 {
		t.Errorf("the placeholder appears %d times in plugin.js, want exactly 1 — renderPlugin substitutes only the first", n)
	}
}

// TestHookRegistrationDetectorNamesEveryShape is the committed corpus for
// hookRegistrationRE, in the shape core/architecture_hookbody_shapes_test.go
// uses: one source per spelling, pinned to the verdict the detector must
// return.
//
// It exists because this detector's first draft shipped with its worst hole,
// and the hole came with an approved spelling. The pattern matched only BARE
// property keys — which reads as complete until you notice that ten of
// opencode's fifteen hook names contain a dot and therefore cannot be spelled
// bare. Adding a `"permission.ask"` handler to the shipped file passed the
// guard whose entire purpose is to forbid exactly that.
//
// The want:none rows carry most of the remaining value. A hook NAME inside a
// comment and a key nested one level deeper are both things a looser rule would
// call a registration, which is how a guard starts producing failures nobody
// believes.
func TestHookRegistrationDetectorNamesEveryShape(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			// The vacuity guard: without it, a detector that matched nothing
			// would satisfy every want:none row below.
			name: "a bare key is a registration",
			src:  "\treturn {\n\t\tevent: async () => {},\n\t}",
			want: []string{"event"},
		},
		{
			// The row this corpus exists for.
			name: "a QUOTED dotted key is a registration",
			src:  "\treturn {\n\t\t\"permission.ask\": async (_i, o) => { o.status = \"deny\" },\n\t}",
			want: []string{"permission.ask"},
		},
		{
			name: "both spellings in one object",
			src:  "\treturn {\n\t\t\"tool.execute.before\": async () => {},\n\t\tevent: async () => {},\n\t}",
			want: []string{"tool.execute.before", "event"},
		},
		{
			name: "a hook name inside a comment is not a registration",
			src:  "\treturn {\n\t\t// never register permission.ask: it can answer for the user\n\t\tevent: async () => {},\n\t}",
			want: []string{"event"},
		},
		{
			name: "a key nested deeper than the hooks object is not a registration",
			src:  "\treturn {\n\t\tevent: async () => {\n\t\t\tconst payload = {\n\t\t\t\tsession_id: id,\n\t\t\t}\n\t\t},\n\t}",
			want: []string{"event"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, m := range hookRegistrationRE.FindAllStringSubmatch(c.src, -1) {
				got = append(got, m[1])
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("detector returned %v, want %v\nsource:\n%s", got, c.want, c.src)
			}
		})
	}
}
