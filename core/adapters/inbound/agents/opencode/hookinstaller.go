// hookinstaller.go installs irrlicht's opencode plugin — the ONE file this
// adapter writes into a user's opencode installation (issue #1719, epic
// #1355).
//
// # The epic's verdict, and what the source actually says
//
// Epic #1355 cut opencode with the verdict that "a shipped TS plugin is the
// only route: a distribution and supply-chain problem (npm package? vendored
// file?), not a config write". Half of that survives contact with the
// installed package and half does not. Measured against opencode 1.14.50 — the
// bun-compiled binary at ~/.opencode/bin/opencode, read out of the bundle it
// carries, not out of rendered docs:
//
//   - **The config-file route really is closed.** The `experimental` struct in
//     the live config schema carries exactly six keys —
//     disable_paste_summary, batch_tool, openTelemetry, primary_tools,
//     continue_loop_on_deny, mcp_timeout — and no `hook`. That half of #1719
//     is confirmed.
//
//   - **The distribution problem does not exist.** opencode auto-discovers
//     plugins with a plain directory glob:
//     `ConfigPlugin.load(dir)` is `for (const f of await Glob.scan(
//     "{plugin,plugins}/*.{ts,js}", {cwd: dir, absolute: true, dot: true,
//     symlink: true})) out.push(pathToFileURL(f).href)`, and
//     `Config.loadInstanceState` calls it for EVERY directory
//     `ConfigPaths.directories` returns — the first of which is
//     `Global.Path.config`, i.e. `$XDG_CONFIG_HOME/opencode` or
//     `~/.config/opencode`. So there is no registry, no npm resolution, no git,
//     no build step, no toolchain, and no config entry: a `.js` file in
//     `<config>/plugin/` is loaded, globally, for every session.
//     The `plugin: [...]` config array and `opencode plugin install` — the
//     npm-spec route #1355 was reasoning about — are a SECOND, separate
//     mechanism. This installer does not use it, does not touch
//     opencode.json, and does not resolve, fetch, build or version any
//     package. It writes one file.
//     This is exactly the correction #1721 made for pi, arrived at
//     independently and from the other end: pi's `pi install` was the red
//     herring there, `plugin: [...]` is the red herring here.
//
//   - **No toolchain, and no dependency.** The glob accepts `.js` as well as
//     `.ts`, and a plugin whose directory has no package.json resolves to the
//     file itself (`resolveEntry` returns the target unchanged when
//     `readManifest` finds nothing). The shipped file imports only
//     node:child_process and does not import @opencode-ai/plugin, so the
//     package opencode background-installs into each config directory is
//     irrelevant to it.
//
//   - **Uninstall is `os.Remove`.** Nothing else was touched.
//
//   - **Blast radius is bounded by what the plugin registers.** opencode wraps
//     plugin LOADING in try/catch and reports a failing plugin as an error
//     while the agent continues. It does NOT wrap hook DISPATCH: Plugin.trigger
//     iterates `for (const h of hooks) await h[name](input, output)` with no
//     catch, and several of those hooks receive a mutable `output` the handler
//     is expected to change — `permission.ask`'s `output.status` can be set to
//     "allow" or "deny". plugin.js therefore registers exactly one hook,
//     `event`, which is the read-only bus tap: it receives an event and
//     returns nothing anyone reads. See plugin.js for the rest of that
//     reasoning.
//
// The honest residue, stated in the consent copy rather than only here: it is
// still code, running in-process in the user's agent with the user's own
// permissions, and #570's contract is what makes that the user's decision.
//
// # Delivery
//
// Address-free (contracttesting.DeliveryAddressFree): the file names the
// `irrlichd hook-post opencode` beacon (core/pkg/hookbeacon), which reads the
// daemon's published address at fire time. Nothing address-shaped is written
// into the user's opencode installation, so the #1178 stale-port class is
// inexpressible here. It also keeps the shipped artifact small, which is the
// whole argument above: the JavaScript does not speak HTTP and does not know
// what a port is — it spawns one command and writes JSON to its stdin.
package opencode

import (
	// embed backs the //go:embed of plugin.js below: the artifact this
	// installer writes ships inside the irrlichd binary rather than being
	// fetched, which is the claim the consent copy makes.
	_ "embed"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/hookartifact"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/hookbeacon"
)

// opencode's own wire names for the three bus events this adapter subscribes
// to. They are opencode's, not Claude Code's: session.hookSignalEffects is a
// flat map keyed by the literal wire string with no adapter dimension, and
// none of these three is dispatched through it — hooks.go routes each one to a
// dedicated SessionDetector entry point instead, so no name-keyed lookup is
// involved on this path and no row can leak into another adapter's dispatch.
const (
	// HookEventPermissionAsked is published by Permission.ask when — and only
	// when — a pattern actually needs asking. Permission.ask evaluates every
	// pattern against the ruleset first and returns EARLY, publishing nothing,
	// if all of them already resolve to "allow". That narrowness is what makes
	// it usable as a blocked-on-user assert: it is not a before-every-tool
	// event, and it must never be confused for one. hooks.go's header carries
	// the measurement.
	HookEventPermissionAsked = "permission.asked"

	// HookEventPermissionReplied is published by Permission.reply for EVERY
	// reply, including "reject", and is additionally cascaded to that session's
	// other pending requests when one is rejected. That is what puts opencode
	// out of copilot's position (copilot emits nothing on a denial, so a hold
	// with no release would pin the session waiting) — the release really does
	// arrive on the deny path.
	HookEventPermissionReplied = "permission.replied"

	// HookEventSessionIdle is published by SessionStatus.set when a session's
	// runner reports idle — SessionRunState.runner wires it as the runner's
	// onIdle, and SessionRunState.cancel sets it directly. So it covers the
	// turn ending, being cancelled, and being interrupted, which is the same
	// authoritative turn-end claim Claude Code's Stop makes.
	HookEventSessionIdle = "session.idle"
)

// installedHookEvents is every opencode bus event this adapter subscribes to.
// The exclusions are the finding rather than an omission:
//
//   - `permission.ask` — the HOOK, not the bus event — is excluded on a source
//     fact, not a preference: opencode dispatches named hooks without a
//     try/catch, and this one's `output.status` is a writable "allow" | "deny"
//     | "ask". A monitoring plugin must not be able to answer a permission
//     prompt for the user, or to throw inside the agent loop. The
//     `permission.asked` BUS event above carries the same fact as an
//     observation.
//
//   - `session.status` (busy) was considered as the turn-START signal and
//     rejected for the reason copilot's, gemini-cli's and pi's analogous
//     exclusions cite: the store's own rows already open the turn — the
//     watcher emits EventActivity off new `part` rows — so a hook duplicating
//     it buys nothing irrlicht's lifecycle state model needs, at the cost of a
//     process spawn per turn.
//
//   - `tool.execute.before` / `tool.execute.after` were considered as the
//     assert/release pair gemini-cli's BeforeTool/AfterTool forms, and
//     rejected: they fire for EVERY tool call, so a broad ASSERT would hold
//     `waiting` on every tool call — the exact hazard #1719 named — while a
//     broad RELEASE is only useful paired with a narrow assert on the same
//     signal, which permission.asked/permission.replied already are.
//     They are also hooks rather than bus events, so they carry the dispatch
//     hazard above.
//
//   - `message.part.updated` and the other 29 bus events are excluded because
//     the store carries them already and several fire per streamed chunk.
var installedHookEvents = []string{
	HookEventPermissionAsked,
	HookEventPermissionReplied,
	HookEventSessionIdle,
}

// hookEventSince records the opencode version each installed event is KNOWN to
// exist in. All three are pinned to minOpenCodeVersion because that is the
// version they were read out of — the binary is bun-compiled and carries no
// per-feature changelog this could be bisected against, and inventing an
// earlier number would be a claim nothing here supports. minOpenCodeVersion
// must be >= every value here, which AssertHookVersionGate enforces; equal is
// the honest answer when the floor is "the version this was measured at".
var hookEventSince = map[string]string{
	HookEventPermissionAsked:   minOpenCodeVersion,
	HookEventPermissionReplied: minOpenCodeVersion,
	HookEventSessionIdle:       minOpenCodeVersion,
}

// minOpenCodeVersion is the lowest opencode version this adapter's install
// requires.
//
// Pinned to the version everything here was measured against — 1.14.50, read
// out of the installed binary's own bundle — rather than bisected backward.
// The floor is not a claim about one event: it covers `.js` auto-discovery
// (ConfigPlugin.load's glob), the `{plugin,plugins}` directory layout, the
// plugin export shape the loader accepts, and the three bus event names and
// their payload fields — none of which was bisected. core/pkg/cliversion fails
// OPEN on an unknown or unparseable version, so overshooting costs a missing
// install on an old CLI rather than a wrong one on a new CLI, which is the
// safe direction (the same reasoning pi's, kiro-cli's and gemini-cli's floors
// document).
const minOpenCodeVersion = "1.14.50"

// HookEndpointPath is the daemon's opencode hook route. Exported so
// registerHookRoutes (core/cmd/irrlichd/startup.go) and beaconroute_test.go's
// pinned map both derive from the same segment this installer renders into the
// beacon command it writes.
//
// A var, not a const, because hookbeacon.EndpointPath composes the route from
// its own unexported prefix — deriving it here keeps this constant from
// drifting away from the beacon's own routing convention. A mismatch is a
// permanent silent 404: the beacon exits 0 by design, so nothing anywhere
// reports it.
var HookEndpointPath = hookbeacon.EndpointPath(AdapterName)

// pluginDirName is the config-dir-relative directory opencode auto-discovers
// plugins from. opencode's glob accepts BOTH "plugin" and "plugins"
// ("{plugin,plugins}/*.{ts,js}"); this installer owns exactly one of them, so
// a user's own file in the other is untouched.
const pluginDirName = "plugin"

// pluginFilename is the file this adapter owns inside that directory. The .js
// suffix is load-bearing twice over: opencode's glob accepts only .ts and .js,
// and .js is the spelling that involves no transform between the bytes written
// here and the code opencode runs.
const pluginFilename = "irrlicht.js"

// displayPluginPath is the consent copy's home-relative spelling of the file
// PluginPath resolves absolutely.
const displayPluginPath = "~/.config/opencode/plugin/irrlicht.js"

//go:embed plugin.js
var pluginTemplate string // mutated by plugin_test.go's loud-failure guard, restored in t.Cleanup

// beaconCommandPlaceholder is the token renderPlugin substitutes. It appears
// exactly once in plugin.js, inside a JavaScript string literal, and
// plugin_test.go fails if that stops being true — a template whose placeholder
// silently stopped matching would render a file that runs the literal
// placeholder as a shell command.
const beaconCommandPlaceholder = `"__IRRLICHT_BEACON_COMMAND__"`

// beaconLineRE reads the rendered command back out of an installed file. It
// anchors on the whole assignment line rather than searching for the command
// text, so a command that happens to appear in a comment cannot be mistaken
// for the live one.
var beaconLineRE = regexp.MustCompile(`(?m)^const IRRLICHT_BEACON = (".*");$`)

// renderPlugin produces the exact bytes to install for a given, already-
// resolved beacon command.
//
// strconv.Quote, not fmt.Sprintf("%q") by habit and not string concatenation:
// the command embeds an absolute filesystem path the user chose, and a path
// containing a quote or a backslash must not be able to terminate the
// JavaScript string literal it lands in. Go's quoted-string escapes are a
// subset of JavaScript's for every byte Quote can emit.
func renderPlugin(command string) ([]byte, error) {
	if command == "" {
		return nil, errors.New("opencode: refusing to render a plugin with an empty beacon command")
	}
	if !strings.Contains(pluginTemplate, beaconCommandPlaceholder) {
		// The embedded template stopped carrying the token this function
		// substitutes. Failing loudly here is the difference between "no
		// install" and "an install that shells out to the placeholder".
		return nil, fmt.Errorf("opencode: embedded plugin template no longer contains %s", beaconCommandPlaceholder)
	}
	return []byte(strings.Replace(pluginTemplate, beaconCommandPlaceholder, strconv.Quote(command), 1)), nil
}

// beaconCommandOf extracts the beacon command an installed file carries.
// Reports false for a file that is not ours, or whose assignment line has been
// edited into something that is not a quoted string.
func beaconCommandOf(src []byte) (string, bool) {
	m := beaconLineRE.FindSubmatch(src)
	if m == nil {
		return "", false
	}
	command, err := strconv.Unquote(string(m[1]))
	if err != nil {
		return "", false
	}
	return command, true
}

// --- path resolution ---

// configHomeEnvVar is the XDG variable opencode resolves its config directory
// from. opencode composes Global.Path.config as
// join(process.env.XDG_CONFIG_HOME || join(homedir(), ".config"), "opencode").
const configHomeEnvVar = "XDG_CONFIG_HOME"

// defaultConfigDirName is the $HOME-relative opencode config directory used
// when XDG_CONFIG_HOME is unset.
const defaultConfigDirName = ".config/opencode"

// opencodeConfigDir resolves the absolute opencode config directory the way
// opencode's own Global.Path does.
//
// $OPENCODE_CONFIG_DIR is deliberately NOT consulted. It ADDS a directory to
// ConfigPaths.directories rather than replacing Global.Path.config, which is
// scanned unconditionally — so installing here is discovered whether or not
// the user has set it, and honouring it would mean choosing between two
// directories opencode reads from both.
func opencodeConfigDir() (string, error) {
	return agentpaths.AbsRoot(agentpaths.FromEnv(AdapterName, configHomeEnvVar, defaultConfigDirName, AdapterName))
}

// PluginPath is the single file this permission's Writes.Path resolves — the
// plugin opencode auto-discovers and runs.
func PluginPath() (string, error) {
	dir, err := opencodeConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, pluginDirName, pluginFilename), nil
}

// --- install / verify / uninstall ---
//
// The install/verify/uninstall lifecycle itself is shared with pi (issue
// #1764) — both adapters install a single executable artifact that their
// upstream agent auto-discovers from a directory glob, and the four
// operations that shape (resolve path, write atomically, recognize "is this
// ours", remove on uninstall) requires are identical modulo the artifact's
// own content. See hookartifact's package doc for the shared mechanism and
// why "is this ours" is the property that matters most to keep in one place.

// installer is the opencode plugin's hookartifact.Installer. A package-level
// var, not constructed per call, so every entry point below shares the same
// wiring — but every field it holds is itself a function, so nothing here is
// resolved (a path, an environment override, the running binary) until the
// moment an operation actually runs.
var installer = hookartifact.Installer{
	AdapterName: AdapterName,
	Sentinel:    hookbeacon.Sentinel(AdapterName),
	ResolveCommand: func() (string, error) {
		return hookbeacon.InstalledCommand(AdapterName)
	},
	Path:   PluginPath,
	Render: renderPlugin,
	Events: installedHookEvents,
}

// EnsureHooksInstalled writes irrlicht's opencode plugin for the irrlichd that
// is running now, creating opencode's plugin directory if it does not exist.
// Returns true if anything changed.
//
// Idempotent, and self-healing across a moved binary: an already-installed
// copy whose beacon command names a different irrlichd is rewritten IN PLACE
// (same path, one file), never duplicated.
func EnsureHooksInstalled() (bool, error) {
	return installer.EnsureInstalled()
}

// ensureInstalledWithCommand is EnsureHooksInstalled with the beacon command
// resolved by the caller — the seam the #1178 contract's ForeignInstall uses to
// seed the install a DIFFERENTLY-situated daemon would have written.
func ensureInstalledWithCommand(command string) (bool, error) {
	return installer.EnsureInstalledWithCommand(command)
}

// VerifyHooksInstalled reports whether the installed plugin is still present
// and current, without writing anything (issue #1372). This is what
// services.HookEntryVerifier re-checks a granted install with on a timer —
// which matters more here than for a config-entry adapter, because opencode's
// own `opencode plugin install`, any manual tidy of the plugin directory, and
// the background `@opencode-ai/plugin` dependency install opencode runs against
// every config directory all operate in the same tree.
func VerifyHooksInstalled() (agent.HookEntryStatus, error) {
	return installer.VerifyInstalled()
}

// UninstallHooks removes irrlicht's opencode plugin. Returns true if anything
// changed.
//
// Removes the FILE, because the file is the entire install — nothing was added
// to opencode.json, no package was installed, and opencode's own
// auto-discovery is what loaded it. A file at our path that is not ours is
// left alone, the same "leave a foreign entry alone" rule every hook uninstall
// in this codebase follows.
//
// opencode's plugin directory is deliberately NOT removed when it empties out,
// even if this install is what created it: an empty plugin/ is an ordinary
// state for an opencode installation, and removing a directory a user's editor
// or shell may have open is a worse surprise than leaving one behind.
func UninstallHooks() (bool, error) {
	return installer.Uninstall()
}
