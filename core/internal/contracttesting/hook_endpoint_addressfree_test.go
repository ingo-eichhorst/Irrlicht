// hook_endpoint_addressfree_test.go exercises AssertHookEndpointFollowsBindAddr's
// DeliveryAddressFree route against a reference beacon installer, and is the
// wiring the first beacon-delivered adapter copies (#1453).
//
// It lives here rather than in an adapter because no adapter installs the beacon
// yet: #1373 built the mechanism and adoption is a separate change per adapter,
// so a route with no call site would be a branch nothing ever runs — precisely
// the "exemption with no test behind it" the contract-family convention exists
// to prevent. It is the same split tools/lib's linter suites draw: assertions
// are pinned against a purpose-built fixture, so they do not move when a real
// adapter is edited.
//
// It is scaffolding, and it says so: once an adapter declares this route, that
// adapter's wiring becomes the honest coverage and this file should be reduced
// to whatever it still uniquely proves, or deleted.
//
// The installer below is deliberately the SHAPE an adapter should have rather
// than the smallest thing that passes, and that shape is copilot's: hookConfig
// takes the already-resolved fallible inputs as parameters and returns a
// hookjson.Config, NOT a (Config, error), so each exported entry point owns its
// own resolution. copilot parameterises on the settings path; a beacon adapter
// parameterises on the path AND the rendered command, which is why
// hookbeacon.InstalledCommand exists and is called once per entry point.
// Resolving the binary per-config instead is what measured +12 lines of error
// branching when copilot evaluated beacon delivery.
//
// One field is NOT the shape to copy: WriteFile. Every real installer passes an
// atomic temp-file+rename writer (claudecode, codex, copilot each have one);
// this one is a plain write, because hookjson deliberately leaves WriteFile
// adapter-supplied and there is no shared writer to call.
package contracttesting

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/pkg/hookbeacon"
)

// referenceBeaconAdapter is the route segment the beacon posts to. A real
// adapter exports this as one constant shared with registerHookRoutes.
const referenceBeaconAdapter = "reference-beacon"

// referenceBeaconHomeEnv relocates the fixture's config home, the way
// CODEX_HOME and COPILOT_HOME relocate the real ones.
const referenceBeaconHomeEnv = "IRRLICHT_TEST_REFERENCE_BEACON_HOME"

// referenceForeignBinary is the irrlichd a differently-situated daemon would
// have named. It is absolute (Command refuses a relative path) and does not
// exist, which is the drift the beacon newly admits: an absolute path that has
// stopped existing fails exactly as quietly as a curl that was never on PATH.
const referenceForeignBinary = "/nonexistent-irrlicht-1453/bin/irrlichd"

var referenceBeaconEvents = []string{"BeforeTool", "Stop"}

// referenceBeaconHome composes agentpaths rather than re-deciding how an agent
// home override is read — the same composition copilotHome uses, and for the
// reason its doc records: the hand-rolled version silently dropped a RELATIVE
// override, so the watcher warned while the installer wrote somewhere else.
//
// The default dir is deliberately EMPTY, which is the one place this fixture
// must NOT copy a real adapter. A real adapter passes a $HOME-relative default;
// AbsRoot refuses an empty root, so here an unset override fails loudly instead
// of resolving into the developer's own agent config. A fixture must never be
// one missing env var away from writing to a real $HOME.
func referenceBeaconHome() (string, error) {
	return agentpaths.AbsRoot(agentpaths.FromEnv(referenceBeaconAdapter, referenceBeaconHomeEnv, ""))
}

func referenceBeaconHooksPath() (string, error) {
	home, err := referenceBeaconHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "hooks.json"), nil
}

func referenceBeaconEntry(command string) map[string]interface{} {
	return map[string]interface{}{
		"type":    "command",
		"command": command,
	}
}

// referenceBeaconEntryNow is the contract's Entry: the inner hook object the
// adapter would install right now. A resolution failure yields an empty
// command, which assertDeliveryIsOurs reports rather than grading silently.
func referenceBeaconEntryNow() map[string]interface{} {
	command, _ := hookbeacon.InstalledCommand(referenceBeaconAdapter)
	return referenceBeaconEntry(command)
}

// referenceBeaconConfig returns a Config, not a (Config, error) — see the file
// comment.
func referenceBeaconConfig(path, command string) hookjson.Config {
	return hookjson.Config{
		Path:       path,
		Sentinel:   hookbeacon.Sentinel(referenceBeaconAdapter),
		Events:     referenceBeaconEvents,
		MatcherFor: func(string) string { return "" },
		Entry:      func() map[string]interface{} { return referenceBeaconEntry(command) },
		IsCanonical: func(hook map[string]interface{}) bool {
			c, _ := hook["command"].(string)
			return hookbeacon.IsCanonical(c, referenceBeaconAdapter)
		},
		WriteFile: func(path string, data []byte) error {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, data, 0o600)
		},
	}
}

// referenceBeaconRun resolves the settings path and hands the built config to
// op, so the three entry points below state that branch once between them.
func referenceBeaconRun(command string, op func(hookjson.Config) (bool, error)) (bool, error) {
	path, err := referenceBeaconHooksPath()
	if err != nil {
		return false, err
	}
	return op(referenceBeaconConfig(path, command))
}

func referenceBeaconEnsureInstalled() (bool, error) {
	command, err := hookbeacon.InstalledCommand(referenceBeaconAdapter)
	if err != nil {
		return false, err
	}
	return referenceBeaconRun(command, hookjson.EnsureInstalled)
}

// referenceBeaconUninstall removes our entries whatever binary they name. It
// needs no special casing to do that: Sentinel names neither the binary path
// nor an address, so an entry installed by a since-deleted irrlichd is still
// matched — which matters because `site/install.sh --uninstall` removes the
// binary without calling --uninstall-hooks. uninstall_is_not_binary_scoped is
// what holds that property, rather than this comment.
func referenceBeaconUninstall() (bool, error) {
	command, err := hookbeacon.InstalledCommand(referenceBeaconAdapter)
	if err != nil {
		return false, err
	}
	return referenceBeaconRun(command, hookjson.Uninstall)
}

// referenceBeaconForeignInstall is the address-free counterpart of seeding a
// default-port install: the same installer, situated on a different irrlichd.
func referenceBeaconForeignInstall() (bool, error) {
	command, err := hookbeacon.Command(referenceForeignBinary, referenceBeaconAdapter)
	if err != nil {
		return false, err
	}
	return referenceBeaconRun(command, hookjson.EnsureInstalled)
}

func TestAddressFreeDeliveryContract(t *testing.T) {
	// Checked up front so a resolution failure reads as itself rather than as
	// the empty-delivery wiring error assertDeliveryIsOurs would report.
	if _, err := hookbeacon.InstalledCommand(referenceBeaconAdapter); err != nil {
		t.Fatalf("resolving the beacon command: %v", err)
	}
	AssertHookEndpointFollowsBindAddr(t, HookInstaller{
		Delivery: DeliveryAddressFree,
		SettingsPath: func(t *testing.T) string {
			t.Setenv(referenceBeaconHomeEnv, t.TempDir())
			path, err := referenceBeaconHooksPath()
			if err != nil {
				t.Fatalf("resolving the reference hooks path: %v", err)
			}
			return path
		},
		Sentinel: hookbeacon.Sentinel(referenceBeaconAdapter),
		Events:   referenceBeaconEvents,
		Entry:    referenceBeaconEntryNow,
		// Beacon delivery is a `type: command` entry, so the delivery string is
		// the whole command — there is no url field and nothing address-shaped
		// inside it.
		EndpointOf: func(hook map[string]interface{}) string {
			c, _ := hook["command"].(string)
			return c
		},
		EnsureInstalled: referenceBeaconEnsureInstalled,
		Uninstall:       referenceBeaconUninstall,
		ForeignInstall:  referenceBeaconForeignInstall,
	})
}
