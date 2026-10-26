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
// There are two artifacts since #2108 — plugin.js for opencode 1.x and
// plugin_v2.js for 2.x — and every guard that is about the artifact rather than
// one format's loader runs over both (shippedArtifacts). The two loaders differ,
// so the shape guards do too: 1.x dispatches named hooks off the object the
// plugin function returns, 2.x hands setup a context object.
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

	"irrlicht/core/pkg/hookbeacon"
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

// shippedArtifact is one embedded plugin template and what the Go side
// declares about it.
type shippedArtifact struct {
	name     string
	template *string
	render   func(command string) ([]byte, error)
	// events is the installer's own event list for this artifact.
	events []string
	// eventID is whether this artifact forwards the event's own id — the 2.x
	// one does, because its events arrive once per plugin instance (#2108).
	eventID bool
}

// shippedArtifacts is every artifact this adapter can install, each with the
// event list its installer and its variant declare.
func shippedArtifacts() []shippedArtifact {
	return []shippedArtifact{
		{name: "plugin.js", template: &pluginTemplate, render: renderPlugin, events: installedHookEvents},
		{name: "plugin_v2.js", template: &pluginV2Template, render: renderPluginV2, events: installedHookEventsV2, eventID: true},
	}
}

// TestShippedPluginForwardsExactlyTheInstalledHookEvents binds each artifact to
// the Go-side declaration.
//
// Two failures it catches, and they point in opposite directions: a plugin
// forwarding an event its installer does not list delivers a name the
// receiver's switch answers with IgnoreUnknownEvent forever, and a plugin
// forwarding fewer means the consent copy — which derives its list and its count
// from that same slice — over-promises what is installed.
func TestShippedPluginForwardsExactlyTheInstalledHookEvents(t *testing.T) {
	for _, a := range shippedArtifacts() {
		t.Run(a.name, func(t *testing.T) {
			forwarded := forwardedEvents(t, *a.template)
			want := append([]string(nil), a.events...)
			sort.Strings(want)
			sort.Strings(forwarded)
			if !reflect.DeepEqual(forwarded, want) {
				t.Errorf("%s forwards %v but its installer declares %v — the shipped artifact and the consent copy disagree about what is installed",
					a.name, forwarded, want)
			}
		})
	}
}

// forwardedEvents reads the IRRLICHT_EVENTS set out of a template.
func forwardedEvents(t *testing.T, template string) []string {
	t.Helper()
	const marker = "const IRRLICHT_EVENTS = new Set(["
	i := strings.Index(template, marker)
	if i < 0 {
		t.Fatalf("the template no longer declares %q — this guard cannot run", marker)
	}
	rest := template[i+len(marker):]
	j := strings.Index(rest, "]")
	if j < 0 {
		t.Fatal("the template's IRRLICHT_EVENTS array is unterminated — this guard cannot run")
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
	if len(forwarded) == 0 {
		t.Fatal("IRRLICHT_EVENTS parsed to nothing — this guard would pass any declaration")
	}
	return forwarded
}

// TestShippedPluginPayloadMatchesTheReceiversFields pins the wire between the
// two halves of this feature. The plugin builds its JSON by hand and the
// receiver decodes it into openCodeHookPayload; nothing in Go's type system
// connects them, so a renamed field would simply arrive as a zero value and the
// receiver would drop every hook in silence.
//
// Every receiver field must be written by every artifact, except event_id by
// the 1.x one: 1.x delivers each event once and forwards no id, and the field
// is omitempty so a 1.x payload's bytes are what they were (#2108). The 2.x
// artifact must write it — without it every 2.x event is dispatched once per
// plugin instance.
func TestShippedPluginPayloadMatchesTheReceiversFields(t *testing.T) {
	typ := reflect.TypeOf(openCodeHookPayload{})
	for _, a := range shippedArtifacts() {
		t.Run(a.name, func(t *testing.T) {
			checked := 0
			for i := 0; i < typ.NumField(); i++ {
				tag := typ.Field(i).Tag.Get("json")
				if tag == "" || tag == "-" {
					continue
				}
				name := strings.Split(tag, ",")[0]
				writes := strings.Contains(*a.template, name+":")
				switch {
				case name == "event_id" && !a.eventID:
					if writes {
						t.Errorf("%s writes event_id, which only the 2.x artifact forwards", a.name)
					}
				case !writes:
					t.Errorf("openCodeHookPayload decodes %q but %s never writes it — the receiver would see a zero value and drop the hook", name, a.name)
				}
				checked++
			}
			if checked == 0 {
				t.Fatal("openCodeHookPayload has no json-tagged field — this guard checked nothing")
			}
		})
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
	for _, a := range shippedArtifacts() {
		t.Run(a.name, func(t *testing.T) {
			found := importRE.FindAllStringSubmatch(*a.template, -1)
			if len(found) == 0 {
				t.Fatalf("no import found in %s at all — this guard's detector has stopped matching", a.name)
			}
			for _, m := range found {
				if !strings.HasPrefix(m[1], "node:") {
					t.Errorf("%s imports %q, which is not a node: builtin — the consent copy promises no package is fetched from anywhere", a.name, m[1])
				}
			}
		})
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
// DeliveryAddressFree property at the artifact level: each rendered file names
// the beacon command and contains nothing a daemon on a different port would
// have to rewrite.
func TestRenderedPluginCarriesTheCommandAndNothingAddressShaped(t *testing.T) {
	const command = `/opt/irrlicht/irrlichd --version >/dev/null && /opt/irrlicht/irrlichd hook-post opencode >/dev/null || true`
	for _, a := range shippedArtifacts() {
		t.Run(a.name, func(t *testing.T) {
			rendered, err := a.render(command)
			if err != nil {
				t.Fatalf("render: %v", err)
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
		})
	}
}

// TestRenderedPluginsAreOneInstall pins what makes the 1.x and 2.x artifacts
// ONE install rather than two (#2108): each installer reads the other's
// rendered file as ours, so an upgrade or a downgrade rewrites it in place and
// Uninstall removes either. A 2.x file without the sentinel would read as a
// user's own to the 1.x installer, which would then refuse to overwrite it.
func TestRenderedPluginsAreOneInstall(t *testing.T) {
	command, err := hookbeacon.Command("/opt/irrlicht/irrlichd", AdapterName)
	if err != nil {
		t.Fatalf("beacon command: %v", err)
	}
	for _, a := range shippedArtifacts() {
		rendered, err := a.render(command)
		if err != nil {
			t.Fatalf("render %s: %v", a.name, err)
		}
		for name, in := range map[string]bool{"installer": installer.IsOurs(rendered), "installerV2": installerV2.IsOurs(rendered)} {
			if !in {
				t.Errorf("%s does not recognize the rendered %s as ours — it would refuse to rewrite it and Uninstall would leave it behind", name, a.name)
			}
		}
	}
}

// TestRenderedPluginSurvivesAHostilePath is the property the substitution's use
// of strconv.Quote exists for: the command embeds an absolute path the user
// chose, and a path containing a quote or a backslash must not be able to
// terminate the JavaScript string literal it lands in.
func TestRenderedPluginSurvivesAHostilePath(t *testing.T) {
	for _, a := range shippedArtifacts() {
		for _, command := range []string{
			`/tmp/a"b/irrlichd hook-post opencode`,
			`/tmp/a\b/irrlichd hook-post opencode`,
			"/tmp/a\nb/irrlichd hook-post opencode",
			"/tmp/a\\\"; process.exit(1); //b/irrlichd hook-post opencode",
		} {
			rendered, err := a.render(command)
			if err != nil {
				t.Fatalf("%s render(%q): %v", a.name, command, err)
			}
			got, ok := beaconCommandOf(rendered)
			if !ok {
				t.Errorf("%s render(%q): the beacon line does not read back", a.name, command)
				continue
			}
			if got != command {
				t.Errorf("%s render(%q) read back %q — the quoting did not round-trip, which means the literal was terminated early", a.name, command, got)
			}
		}
	}
}

// TestRenderPluginRefusesWhenTheTemplateLostItsPlaceholder is the loud-failure
// guard. A template that stopped carrying the token the renderer substitutes
// would otherwise be installed verbatim — and the file would then shell out to
// the literal string "__IRRLICHT_BEACON_COMMAND__" on every event.
func TestRenderPluginRefusesWhenTheTemplateLostItsPlaceholder(t *testing.T) {
	for _, a := range shippedArtifacts() {
		t.Run(a.name, func(t *testing.T) {
			original := *a.template
			t.Cleanup(func() { *a.template = original })
			*a.template = strings.Replace(*a.template, beaconCommandPlaceholder, `"gone"`, 1)

			if _, err := a.render("irrlichd hook-post opencode"); err == nil {
				t.Fatal("render succeeded against a template with no placeholder — it would have installed a file that runs the placeholder as a command")
			}
		})
	}
}

// TestRenderPluginRefusesAnEmptyCommand is the second half of the same
// argument: an empty command renders a file that spawns `/bin/sh -c ""` on
// every event, forever, silently.
func TestRenderPluginRefusesAnEmptyCommand(t *testing.T) {
	for _, a := range shippedArtifacts() {
		if _, err := a.render(""); err == nil {
			t.Errorf("%s render(\"\") succeeded — the installed file would spawn an empty shell command on every opencode event", a.name)
		}
	}
}

// TestPlaceholderAppearsExactlyOnce pins what the single-replacement
// substitution assumes. A second occurrence would be left in the installed file
// verbatim.
func TestPlaceholderAppearsExactlyOnce(t *testing.T) {
	for _, a := range shippedArtifacts() {
		if n := strings.Count(*a.template, beaconCommandPlaceholder); n != 1 {
			t.Errorf("the placeholder appears %d times in %s, want exactly 1 — the renderer substitutes only the first", n, a.name)
		}
	}
}

// --- the 2.x artifact's shape (#2108) ---

// v2ContextUseRE finds every use of the context opencode 2.x hands setup,
// capturing the member read off it. A bare use — the context passed on, or
// indexed — captures nothing and is checked against the allowed hand-offs.
var v2ContextUseRE = regexp.MustCompile(`\bctx\b(?:\s*\.\s*([A-Za-z_$][A-Za-z0-9_$]*))?`)

// v2AllowedContextHandOffs are the only places the 2.x plugin may name the
// context without reading a member off it: the two parameters that receive it
// and the one call that passes it from setup to the subscription loop.
var v2AllowedContextHandOffs = []string{"function irrlichtSetup(ctx)", "function forward(ctx)", "forward(ctx);"}

// jsLineCommentRE strips whole-line // comments, so prose naming a context
// member is not read as a use of it. Only WHOLE-LINE comments: a "//" after
// code could be inside a string literal, and stripping from there would hide
// the rest of the line — so the guard below refuses a template that has one.
var jsLineCommentRE = regexp.MustCompile(`(?m)^\s*//.*$`)

// jsTrailingSlashesRE finds a "//" after code on the same line.
var jsTrailingSlashesRE = regexp.MustCompile(`(?m)^.*\S.*//.*$`)

// TestShippedV2PluginUsesOnlyTheEventBus is the 2.x counterpart of
// TestShippedPluginRegistersOnlyTheReadOnlyEventTap, and it carries the same
// stake. 2.x has no returned hooks object to read keys off: the capabilities
// are members of the context setup receives, and #2104's probe plugin logged
// their names — permission, tool, command, provider, credential and shell
// among them. The 2.x artifact may read exactly one member, `event`, and call
// exactly one method on that, `subscribe`. Anything else is a capability a
// monitoring plugin has no business holding.
func TestShippedV2PluginUsesOnlyTheEventBus(t *testing.T) {
	code := jsLineCommentRE.ReplaceAllString(pluginV2Template, "")
	if trailing := jsTrailingSlashesRE.FindAllString(code, -1); len(trailing) > 0 {
		t.Fatalf("plugin_v2.js has \"//\" after code on %q — keep comments on their own lines so this guard reads every use of the context", trailing)
	}
	members := map[string]bool{}
	bare := 0
	for _, loc := range v2ContextUseRE.FindAllStringSubmatchIndex(code, -1) {
		if loc[2] >= 0 {
			members[code[loc[2]:loc[3]]] = true
			continue
		}
		allowed := false
		for _, handOff := range v2AllowedContextHandOffs {
			start := loc[0] - strings.Index(handOff, "ctx")
			if start >= 0 && strings.HasPrefix(code[start:], handOff) {
				allowed = true
				break
			}
		}
		if !allowed {
			t.Errorf("plugin_v2.js names the context outside the allowed hand-offs at %q — a context passed on or indexed is one this guard cannot follow",
				code[loc[0]:min(len(code), loc[1]+24)])
		}
		bare++
	}
	if len(members) == 0 || bare == 0 {
		t.Fatalf("found members %v and %d bare uses of the context — this guard's detector has stopped matching and would pass vacuously", members, bare)
	}
	if !reflect.DeepEqual(members, map[string]bool{"event": true}) {
		t.Errorf("plugin_v2.js reads %v off the context, want exactly [event] — the context is where a 2.x plugin registers tools and answers permissions", members)
	}
	calls := regexp.MustCompile(`\bctx\.event\.([A-Za-z_$][A-Za-z0-9_$]*)`).FindAllStringSubmatch(code, -1)
	if len(calls) == 0 {
		t.Fatal("plugin_v2.js never calls anything on ctx.event — this guard cannot run")
	}
	for _, c := range calls {
		if c[1] != "subscribe" {
			t.Errorf("plugin_v2.js calls ctx.event.%s, want only subscribe — the read-only stream", c[1])
		}
	}
}

// TestShippedV2PluginExportsAnIDAndASetup pins the 2.x loader fact the 1.x
// artifact fails: "Plugin must export a default definition with an id and an
// effect or setup function" (#2104 item 10). The 2.x artifact's only export is
// a default object literal whose keys are exactly id and setup.
func TestShippedV2PluginExportsAnIDAndASetup(t *testing.T) {
	exports := regexp.MustCompile(`(?m)^export\b.*$`).FindAllString(pluginV2Template, -1)
	if len(exports) != 1 || exports[0] != "export default {" {
		t.Fatalf("plugin_v2.js has top-level exports %q, want exactly one: %q", exports, "export default {")
	}
	body := pluginV2Template[strings.Index(pluginV2Template, "export default {"):]
	end := strings.Index(body, "\n};")
	if end < 0 {
		t.Fatal("plugin_v2.js's default export is not closed by a top-level \"};\" — this guard cannot read its keys")
	}
	var keys []string
	for _, m := range regexp.MustCompile(`(?m)^\t([A-Za-z_$][A-Za-z0-9_$]*)\s*:`).FindAllStringSubmatch(body[:end], -1) {
		keys = append(keys, m[1])
	}
	if !reflect.DeepEqual(keys, []string{"id", "setup"}) {
		t.Errorf("plugin_v2.js's default export has keys %v, want exactly [id setup]", keys)
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
