package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/agentwiring"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/adapters/outbound/gtbin"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/relay"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/permission"
	"irrlicht/core/pkg/capacity"
	"irrlicht/core/pkg/daemonaddr"
	"irrlicht/core/pkg/hookbeacon"
	"irrlicht/core/ports/outbound"
)

// Version is injected at build time via -ldflags "-X main.Version=x.y.z".
var Version = "dev"

// lazyControl adapts the daemon's InputService to relay.ControlHandler with a
// late binding: the publish controller is constructed during relay setup, which
// precedes the consent stack that builds InputService. resolve returns nil
// until then; a control frame that races startup is rejected, not panicked.
type lazyControl struct{ resolve func() relay.ControlHandler }

func (l lazyControl) SendInput(id string, d []byte) error {
	if h := l.resolve(); h != nil {
		return h.SendInput(id, d)
	}
	return fmt.Errorf("relay control: input service not ready")
}

func (l lazyControl) Interrupt(id string) error {
	if h := l.resolve(); h != nil {
		return h.Interrupt(id)
	}
	return fmt.Errorf("relay control: input service not ready")
}

// The daemon's bind address and default port live in core/pkg/daemonaddr, so
// the agent hook installers resolve the same endpoint the daemon actually
// listens on rather than agreeing with it by convention (#1178).
const envUIDir = "IRRLICHT_UI_DIR"

func hasFlag(name string) bool {
	return hasFlagIn(os.Args[1:], name)
}

// hasFlagIn is hasFlag over an explicit argument list, so selectAction is a pure
// function of argv and its ORDER can be tested without building a binary.
func hasFlagIn(args []string, name string) bool {
	for _, arg := range args {
		if arg == name {
			return true
		}
	}
	return false
}

// knownFlags is the complete set of flags irrlichd accepts, and it IS the
// parser: selectAction rejects any flag-shaped argument absent from this list
// rather than falling through to the daemon, so a typo fails loudly instead of
// starting a daemon in a subtly wrong configuration (#1417). `irrlichd --recrod`
// used to start a daemon with recording off and say nothing.
//
// Two entries do not appear in selectAction's dispatch switch: --record is read
// much later, by runDaemon via hasFlag, and -v is an alias of --version. Any
// future flag read by a bare hasFlag() call must be added here too, or it will
// be rejected before its reader ever runs —
// TestKnownFlagsCoversEveryFlagTheDaemonReads is the tripwire for that, and it
// reads main.go's source rather than trusting a second hand-kept list.
var knownFlags = []string{
	"--record",
	"--version",
	"-v",
	"--diagnose",
	"--uninstall-hooks",
	"--print-managed-files",
	"--uninstall-task-eta",
}

// isFlagShaped classifies one argument, and it is deliberately the ONLY place
// that classification is written.
//
// firstUnknownFlag and firstPositional are exact complements over this
// predicate: every argument is empty (skipped by both), flag-shaped, or a
// positional. Two hand-kept copies of the test could drift, and the failure
// would be silent in the worst direction — an argument that is neither, and so
// reaches the daemon unexamined, which is the whole of what #1417 closed.
func isFlagShaped(arg string) bool {
	return strings.HasPrefix(arg, "-")
}

// firstUnknownFlag returns the first flag-shaped argument absent from knownFlags.
//
// This is what makes --record=1 an unknown FLAG rather than a silently ignored
// one: no irrlichd flag takes a value, so the = form has never done anything.
func firstUnknownFlag(args []string) (string, bool) {
	for _, arg := range args {
		if arg == "" || !isFlagShaped(arg) {
			continue
		}
		if !hasFlagIn(knownFlags, arg) {
			return arg, true
		}
	}
	return "", false
}

// unknownFlagMessage is the whole of what an unknown flag prints. Pure, so the
// wording — which must NAME the offending flag, the point of #1417 — is testable
// without building a binary.
func unknownFlagMessage(name string) string {
	return fmt.Sprintf("irrlichd: unknown flag %q\nknown flags: %s\n", name, strings.Join(knownFlags, " "))
}

// unknownSubcommandMessage is its counterpart for a positional token (#1373).
func unknownSubcommandMessage(name string) string {
	return fmt.Sprintf("irrlichd: unknown subcommand %q\n", name)
}

// reject is the shared tail of the two branches that refuse a command line.
//
// The message goes to stderr and NOTHING goes to stdout, which is a contract
// rather than a style choice. Either branch can be reached by a hook-shaped
// invocation on a future binary that renamed the verb or the flag out from
// under an installed hook line, and there an empty stdout is what keeps the
// non-zero exit from reading as a "deny" decision to a fail-closed pre-tool
// hook. Stated once here so the two callers cannot drift apart on it.
func reject(msg string) int {
	fmt.Fprint(os.Stderr, msg)
	return 2
}

// cliAction is what one irrlichd command line selects.
type cliAction int

const (
	actionBeacon cliAction = iota
	actionVersion
	actionUninstallHooks
	actionPrintManagedFiles
	actionUninstallTaskEta
	actionDiagnose
	actionUnknownFlag
	actionUnknownSubcommand
	actionRunDaemon
)

// selectAction resolves a command line to the one thing irrlichd will do.
//
// The order is part of the contract, not an implementation detail, and two
// positions in it are load-bearing:
//
//   - The beacon is FIRST, ahead of --version. Every installed beacon command
//     line LEADS with --version as a guard against an older irrlichd starting a
//     daemon instead of posting a hook (see hookbeacon.LegacyGuardToken, which
//     records why the guard has to lead rather than trail). Moving the version
//     check back in front would turn every installed beacon into a version
//     banner — silently, since the beacon is not supposed to say anything on a
//     healthy path either.
//   - actionUnknownFlag is checked AFTER the beacon and BEFORE every other
//     branch. After the beacon because a beacon command line carries tokens no
//     allow-list should judge — the installed form ends in a literal
//     `>/dev/null`, and the adapter segment is arbitrary. Before the rest so a
//     typo is reported even alongside a flag that would otherwise have won:
//     `irrlichd --diagnose --recrod` names --recrod rather than quietly
//     diagnosing. #1412 left this fall-through open deliberately, as out of
//     scope; #1417 closed it.
//   - actionUnknownSubcommand is LAST, and it only fires on a positional token.
//     Between the two, every argument irrlichd receives is now classified: empty,
//     a known flag, an unknown flag, or a positional. Nothing reaches the daemon
//     unexamined, which is the property #1417 asked for.
func selectAction(args []string) cliAction {
	if hookbeacon.IsInvocation(args) {
		return actionBeacon
	}
	if _, ok := firstUnknownFlag(args); ok {
		return actionUnknownFlag
	}
	switch {
	case hasFlagIn(args, "--version"), hasFlagIn(args, "-v"):
		return actionVersion
	case hasFlagIn(args, "--uninstall-hooks"):
		return actionUninstallHooks
	case hasFlagIn(args, "--print-managed-files"):
		return actionPrintManagedFiles
	case hasFlagIn(args, "--uninstall-task-eta"):
		return actionUninstallTaskEta
	case hasFlagIn(args, "--diagnose"):
		return actionDiagnose
	}
	if _, ok := firstPositional(args); ok {
		return actionUnknownSubcommand
	}
	return actionRunDaemon
}

// firstPositional returns the first argument that is not flag-shaped.
func firstPositional(args []string) (string, bool) {
	for _, arg := range args {
		if arg == "" || isFlagShaped(arg) {
			continue
		}
		return arg, true
	}
	return "", false
}

func main() {
	args := os.Args[1:]
	if action := selectAction(args); action != actionRunDaemon {
		os.Exit(runCLIAction(action, args))
	}
	runDaemon()
}

// runCLIAction performs every action other than starting the daemon and returns
// the process exit code, so main() stays a two-line dispatch and the actions
// themselves are one flat switch.
func runCLIAction(action cliAction, args []string) int {
	switch action {
	case actionBeacon:
		// Post always returns 0 — that contract is the whole of #1373; see the
		// hookbeacon package doc for what a non-zero exit does to a tool call.
		return hookbeacon.Post(hookbeacon.Options{
			Args:   hookbeacon.InvocationArgs(args),
			Stdin:  os.Stdin,
			Stderr: os.Stderr,
		})
	case actionVersion:
		fmt.Printf("irrlichd version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	case actionUninstallHooks:
		uninstallHooks()
	case actionPrintManagedFiles:
		if err := printManagedFiles(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case actionUninstallTaskEta:
		uninstallTaskEtaBlocks()
	case actionDiagnose:
		runDiagnose()
	case actionUnknownFlag:
		name, _ := firstUnknownFlag(args)
		return reject(unknownFlagMessage(name))
	case actionUnknownSubcommand:
		name, _ := firstPositional(args)
		return reject(unknownSubcommandMessage(name))
	}
	return 0
}

// uninstallHooks removes irrlicht's hooks from every agent config file the
// adapter registry declares and, for each adapter whose hooks permission was
// previously granted, records the explicit opt-out in the consent store (#570)
// — a persisted "granted" would otherwise re-install the hooks on the next
// daemon start (via the Apply closure), silently reverting this decision.
//
// The set comes from agents.HookConfigs rather than a literal list: an adapter
// missing from that list would keep its entries in the user's real config
// permanently after an uninstall, firing on every turn at a dead port, with
// nothing to tell the user where they came from (#1357).
//
// HookConfigs is the HOOKS slice of the managed-file projection #1383 widened.
// The narrowing is what keeps this command's name true: the catalog also
// declares the CLAUDE.md instruction blocks and the kitty remote-control block,
// and running their uninstallers from here would revoke capabilities the user
// asked nothing about. Revoking those is the permission wizard's job — this
// command is the escape hatch for the one case the wizard cannot reach, hook
// entries left in an agent config pointing at a daemon that is gone.
func uninstallHooks() {
	configs, err := agents.HookConfigs(declaredConsentCatalog())
	if err != nil {
		log.Fatalf("failed to resolve the agent config files to uninstall from: %v", err)
	}
	home, _ := os.UserHomeDir()
	failed := uninstallHookConfigs(os.Stdout, configs, filesystem.NewPermissionStore(dataDir(home)))
	// Tell a daemon that is already running to re-read the store (#1425).
	// Without it the store says "denied" while the live daemon's in-memory
	// consent still says "granted", and #1372's re-verification loop puts the
	// entries back within one interval. Runs even when an uninstaller failed:
	// denyHooksPermissions has already written whatever it was going to write,
	// and a partially-uninstalled config is exactly the state in which a stale
	// daemon re-installing the rest is most confusing.
	notifyDaemonConsentChanged(os.Stdout)
	if failed > 0 {
		os.Exit(1)
	}
}

// uninstallHookConfigs runs every declared uninstaller and then records the
// matching hooks permissions as denied. It returns how many uninstallers
// failed.
//
// One adapter's failure must not end the run. `hookjson` deliberately refuses
// to rewrite a config file it cannot parse, so a single hand-edited
// ~/.claude/settings.json used to abort the whole command — leaving every LATER
// adapter's entries firing at a dead port, and (because the consent block never
// ran) leaving their hooks permissions still granted, so the next daemon start
// re-installed them via the Apply closure. That is precisely the #570
// regression this function exists to prevent, and the registry projection only
// widens the window as adapters are added.
func uninstallHookConfigs(w io.Writer, configs []agents.ManagedUserFile, store outbound.PermissionStore) int {
	failed := 0
	for _, c := range configs {
		if !reportUninstall(w, c.Path, c.Uninstall) {
			failed++
		}
	}

	denyHooksPermissions(w, configs, store)
	return failed
}

// denyHooksPermissions records every hooks permission that had been granted as
// explicitly denied. Without it a persisted "granted" re-installs the hooks on
// the next daemon start via the Apply closure, silently undoing the uninstall
// (#570). A permission that was never granted is left alone — turning a pending
// answer into a denial would stop the wizard ever asking about it.
func denyHooksPermissions(w io.Writer, configs []agents.ManagedUserFile, store outbound.PermissionStore) {
	set, err := store.Load()
	if err != nil {
		return
	}
	denied := false
	for _, c := range configs {
		if set.Get(c.Adapter, c.Key) == permission.StateGranted {
			set.Put(c.Adapter, c.Key, permission.StateDenied)
			denied = true
		}
	}
	if !denied {
		return
	}
	if err := store.Save(set); err != nil {
		fmt.Fprintf(w, "warning: failed to record hooks permission(s) as denied: %v\n", err)
		return
	}
	fmt.Fprintln(w, "Recorded the hooks permission(s) as denied (re-grant via the permission wizard)")
}

// printManagedFiles writes the resolved absolute path of every shared,
// user-owned file irrlicht writes, one per line.
//
// It exists for the onboarding recording rig (#1357, widened by #1383): a
// recording daemon runs IRRLICHT_PERMISSION_MODE=grant-all against the user's
// REAL $HOME, which exercises EVERY modify permission's Apply closure — the
// hook installers, the CLAUDE.md instruction blocks, the kitty remote-control
// patch. Those paths follow $HOME, not IRRLICHT_HOME, so IRRLICHT_ONBOARD_HOME
// does not isolate them and the rig has to back them up before spawning the
// daemon. Asking the daemon is the only way that list cannot drift from the
// permissions that actually write them.
//
// It projects the full consent catalog rather than agents.All(): the kitty
// patch is one of three daemon-wide declarations appended outside the adapter
// registry, and projecting only the registry is how it stayed unprotected.
//
// An empty result is an error, not an empty list: the rig would read "nothing
// to protect" as success and record over the user's files unprotected.
//
// Renamed from --print-hook-configs with the concept (#1383). The rig is its
// only consumer, and precheck.sh rebuilds the daemon before every recording
// run, so the two travel together.
func printManagedFiles(w io.Writer) error {
	files, err := agents.ManagedUserFiles(declaredConsentCatalog())
	if err != nil {
		return fmt.Errorf("failed to resolve the shared user files irrlicht writes: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no permission declares a managed user file")
	}
	writeManagedFilePaths(w, files)
	return nil
}

// writeManagedFilePaths prints each distinct path once. The dedup is
// load-bearing rather than cosmetic: the rig keys its backups by line index, so
// a path listed twice would be backed up under two indices and restored twice.
// Since #1383 the shipped catalog actually produces a duplicate — claudecode's
// hooks and statusline permissions both declare ~/.claude/settings.json — so
// this is exercised by the real registry rather than only by a synthetic case.
func writeManagedFilePaths(w io.Writer, configs []agents.ManagedUserFile) {
	seen := make(map[string]bool, len(configs))
	for _, c := range configs {
		if seen[c.Path] {
			continue
		}
		seen[c.Path] = true
		fmt.Fprintln(w, c.Path)
	}
}

// reportUninstall runs one adapter's hook uninstaller and prints whether it
// removed anything. path is the file the adapter itself resolved, so a
// CODEX_HOME user is told which file was actually cleaned rather than the
// default one.
//
// It reports whether the uninstall succeeded; a failure is printed and returned
// rather than fatal, so the caller can still clean up every other adapter.
func reportUninstall(w io.Writer, path string, uninstall func() (bool, error)) bool {
	modified, err := uninstall()
	if err != nil {
		fmt.Fprintf(w, "warning: failed to uninstall hooks from %s: %v\n", path, err)
		return false
	}
	if modified {
		fmt.Fprintf(w, "Removed irrlicht hooks from %s\n", path)
	} else {
		fmt.Fprintf(w, "No irrlicht hooks found in %s\n", path)
	}
	return true
}

// uninstallTaskEtaBlocks removes every irrlicht-managed instruction block from
// ~/.claude/CLAUDE.md. The set is the adapter's, not restated here: this used to
// name task-eta and task-summary by hand and so silently left the task-question
// block behind (#1377).
func uninstallTaskEtaBlocks() {
	modified, err := claudecode.UninstallInstructionBlocks()
	if err != nil {
		log.Fatalf("failed to uninstall irrlicht instruction blocks: %v", err)
	}
	if modified {
		fmt.Println("Removed irrlicht managed blocks from ~/.claude/CLAUDE.md")
	} else {
		fmt.Println("No irrlicht managed blocks found in ~/.claude/CLAUDE.md")
	}
}

// loadConfig builds the daemon's runtime config, applying the
// IRRLICHT_MAX_SESSION_AGE override when set and valid.
func loadConfig(logger outbound.Logger) config.Config {
	cfg := config.Default()
	if v := os.Getenv("IRRLICHT_MAX_SESSION_AGE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.MaxSessionAge = d
		} else {
			logger.LogError("startup", "", fmt.Sprintf("invalid IRRLICHT_MAX_SESSION_AGE %q, using default %s", v, cfg.MaxSessionAge))
		}
	}
	logger.LogInfo("startup", "", fmt.Sprintf("max session age: %s", cfg.MaxSessionAge))
	return cfg
}

// runDaemon brings up the full daemon: config, core services, HTTP routes,
// the consent-gated detection/permission/backchannel stack, then serves
// until SIGTERM/SIGINT and shuts down gracefully. Each phase is wired by a
// dedicated setup function (see startup.go); this is the sequencing.
func runDaemon() {
	recordEnabled := hasFlag("--record") || os.Getenv("IRRLICHT_RECORD") == "1"

	logger, err := logging.New()
	if err != nil {
		log.Fatalf("failed to initialise logger: %v", err)
	}
	defer logger.Close()

	// Hook + statusline installation is consent-gated (issue #570): the
	// PermissionService applies them when (and only when) the user grants
	// the corresponding permission — see startBackgroundLoops below.
	// Nothing under the user's home is modified before that.
	cfg := loadConfig(logger)

	go runCapacityRefreshLoop(context.Background(), logger, 30*time.Second, 256*time.Minute, 24*time.Hour)

	// Resolve the gt binary path (GT_BIN env → common paths → which gt).
	gtResolver := gtbin.New()
	if p := gtResolver.Path(); p != "" {
		logger.LogInfo("startup", "", fmt.Sprintf("gt binary: %s", p))
	} else {
		logger.LogError("startup", "", "gt binary not found (set GT_BIN or add gt to PATH)")
	}

	fsRepo, cachedRepo := initSessionStorage(logger, cfg)
	costTracker := initCostTracker(logger, fsRepo)
	historyTracker, historyCancel := startHistoryTracker(logger)
	defer historyCancel()

	// Push broadcaster for WebSocket fan-out.
	push := services.NewPushService()

	// Stream history events (snapshots, ticks, upgrades) over the same
	// WebSocket envelope as session-state messages.
	historyTracker.SetEmitFunc(historyEventBroadcaster(push))

	// Unified registration for every inbound agent adapter. Wiring below
	// (fswatchers, process scanners, metrics parser map, PID discovery map)
	// derives from this single slice — the only place new agents need to be
	// listed. The slice itself lives in core/adapters/inbound/agents/all.go
	// so the agent-onboarding viewer can build the same metrics Registry
	// during replay without duplicating the construction.
	allAgents := agents.All()

	// Outbound relay publishing (#722) + remote control (#724) — see
	// setupRelay's doc comment for the full rationale.
	rel := setupRelay(logger, push, cachedRepo, allAgents)
	defer rel.cancel()

	// Shared adapters for SessionDetector.
	gitResolver := git.New()
	// The metrics collector (parser map + aider/opencode overrides +
	// Claude Code fallback) is wired by agentwiring.BuildMetricsCollector,
	// the single source of truth shared with the agent-onboarding viewer.
	metricsCollector := agentwiring.BuildMetricsCollector(allAgents)

	// --- File-based SessionDetector (primary detection path) ---
	// Forward-reference: detector is assigned before any callbacks can fire,
	// because ProcessWatcher only invokes callbacks after
	// SessionDetector.Run() subscribes to Watcher events.
	var detector *services.SessionDetector

	// IRRLICHT_DEMO_MODE=1 disables ProcessWatcher and per-adapter Watchers
	// so the daemon serves only what's already on disk in instances/. Used by
	// tools/seed-demo-sessions to take controlled screenshots without live
	// agent processes leaking into the dropdown.
	demoMode := os.Getenv("IRRLICHT_DEMO_MODE") == "1"
	if demoMode {
		logger.LogInfo("startup", "", "IRRLICHT_DEMO_MODE=1 — process + agent watchers disabled")
	}

	pwPort, pwCleanup := setupProcessWatcher(demoMode, &detector, logger)
	if pwCleanup != nil {
		defer pwCleanup()
	}

	// Hook-liveness watchdog (#1368). Built here because it has two consumers
	// on opposite sides of startup — the diagnostics bundle registered a few
	// lines below, and the detector built further down — and neither exists
	// yet. Its two collaborators are injected as they appear; until both are,
	// it reports every channel disarmed, which is honest: no hook can have been
	// served before the receivers are registered either.
	hookLiveness := services.NewHookLivenessWatchdog(services.HookLivenessConfig{
		SilentTurns:    cfg.HookSilentTurns,
		DefaultAdapter: claudecode.AdapterName,
		Adapters:       hookAdapterNames(allAgents),
		Receipts:       hookjson.HookReceiptsFor,
	})

	// Hook-entry re-verification loop (#1372). Same construction timing and the
	// same reason as the watchdog above: the diagnostics bundle registered a few
	// lines below reads its snapshot, and its consent collaborators do not exist
	// yet. Until SetConsent runs it reports every target unwatched and writes
	// nothing — which is honest, since nothing has been consented to either.
	//
	// Deliberately a SEPARATE object from the watchdog: that one answers "is
	// anything arriving" from receipts and turns, this one answers "are the
	// entries still there" by re-reading the file, and collapsing them would
	// merge two diagnoses whose whole value is being distinguishable.
	hookVerifier := services.NewHookEntryVerifier(services.HookEntryReverifyConfig{
		Agents:   allAgents,
		Log:      logger,
		Interval: cfg.HookReverifyInterval,
	})

	mux := http.NewServeMux()
	registerCoreRoutes(mux, registerCoreRoutesDeps{
		FSRepo:            fsRepo,
		CachedRepo:        cachedRepo,
		HistoryTracker:    historyTracker,
		Push:              push,
		AllAgents:         allAgents,
		Version:           Version,
		PublishController: rel.publishController,
		Cfg:               cfg,
		HookLiveness:      hookLiveness,
		HookVerifier:      hookVerifier,
	})

	// Static web UI: served from disk so the dashboard ships as three files
	// (index.html, irrlicht.css, irrlicht.js) under platforms/web/. API routes
	// registered above take precedence over the catch-all "/".
	registerUIRoutes(mux, logger)

	srv := newHTTPServer(mux)

	sockPath, unixL := setupUnixSocket(logger)

	tcpL, resolvedAddr := setupTCPListener(logger)
	// addrPath is where the "daemon is up" signal is published — see
	// publishAddrFile, called once every route below is registered. The path
	// comes from daemonaddr because client CLIs read it back from there; the
	// two must not drift (it sits next to sockPath either way).
	addrPath := daemonaddr.AddrFilePath()

	mdnsAdv := setupMDNS(resolvedAddr, logger)

	// Orchestrator adapters: detect and watch multi-agent orchestration
	// systems. Gas Town is consent-gated (#570) via the start/stop effects
	// below; the monitor itself runs regardless (idle until a watcher
	// registers) so /api/v1/sessions can always consult it.
	orchMonitor, orchCtx, orchCancel := setupOrchestratorMonitor(push, logger)
	defer orchCancel()
	startGastown, stopGastown := gastownEffects(orchCtx, orchMonitor, gtResolver.Path(), cachedRepo, logger)

	// Register API endpoints that need orchMonitor. inputService is
	// resolved at request time via rel.inputService (published once
	// setupBackchannel runs), so a session reports controllable only once
	// that wiring completes.
	registerSessionRoutes(mux, registerSessionRoutesDeps{
		CachedRepo:   cachedRepo,
		OrchMonitor:  orchMonitor,
		CostTracker:  costTracker,
		InputService: &rel.inputService,
		SockPath:     sockPath,
		Push:         push,
		Logger:       logger,
		GitResolver:  gitResolver,
	})

	var watcherFactories map[string]services.WatcherFactory
	detector, watcherFactories = buildDetector(buildDetectorDeps{
		DemoMode:         demoMode,
		PWPort:           pwPort,
		CachedRepo:       cachedRepo,
		Logger:           logger,
		GitResolver:      gitResolver,
		MetricsCollector: metricsCollector,
		Push:             push,
		Version:          Version,
		Cfg:              cfg,
		AllAgents:        allAgents,
		CostTracker:      costTracker,
		HistoryTracker:   historyTracker,
	})

	home, _ := os.UserHomeDir()
	permService := setupPermissionService(mux, setupPermissionServiceDeps{
		Detector:         detector,
		Push:             push,
		Logger:           logger,
		Cfg:              cfg,
		AllAgents:        allAgents,
		WatcherFactories: watcherFactories,
		DemoMode:         demoMode,
		Home:             home,
		StartGastown:     startGastown,
		StopGastown:      stopGastown,
	})

	// The watchdog's second collaborator, available only now: "is this channel
	// expected to deliver" is a consent question, and answering it before the
	// permission service exists would arm the watchdog against channels nobody
	// has agreed to install yet.
	hookLiveness.SetChannelReady(permService.HookChannelReady)
	detector.SetHookLivenessWatchdog(hookLiveness)

	// The verifier's collaborators, available for the same reason and at the
	// same moment. Both come from the permission service on purpose: the loop
	// gets no write path of its own, only the right to ask (#1372/#570).
	hookVerifier.SetConsent(permService.Granted, permService.RepairGrantedHookInstall)

	backchannelEngine, terminalObserver := setupBackchannel(mux, setupBackchannelDeps{
		CachedRepo:        cachedRepo,
		Push:              push,
		PermService:       permService,
		Detector:          detector,
		Logger:            logger,
		Home:              home,
		InputService:      &rel.inputService,
		RelayControlStore: rel.controlStore,
		AllAgents:         allAgents,
	})

	registerHookRoutes(mux, detector, metricsCollector, permService, logger)

	publishAddrFile(addrPath, resolvedAddr, logger)

	go func() { _ = srv.Serve(unixL) }()
	go func() { _ = srv.Serve(tcpL) }()

	// Lifecycle recording: opt-in via --record flag or IRRLICHT_RECORD=1.
	// Recordings default to <dataDir>/recordings, so IRRLICHT_HOME already
	// isolates them. IRRLICHT_RECORDINGS_DIR is the narrower override that
	// wins even when IRRLICHT_HOME is set, so test harnesses (e.g. the
	// onboarding factory's record path) can pin recordings somewhere specific.
	if recordEnabled {
		defer setupRecording(detector, sockPath, logger)()
	}

	sweepZombies(demoMode, detector, logger)

	defer startBackgroundLoops(startBackgroundLoopsDeps{
		Detector:          detector,
		BackchannelEngine: backchannelEngine,
		CachedRepo:        cachedRepo,
		GitResolver:       gitResolver,
		TerminalObserver:  terminalObserver,
		PermService:       permService,
		HookVerifier:      hookVerifier,
		Cfg:               cfg,
		DemoMode:          demoMode,
		Logger:            logger,
	})()

	logger.LogInfo("startup", "", fmt.Sprintf("irrlichd %s listening on unix:%s and tcp:%s", Version, sockPath, resolvedAddr))

	// Wait for SIGTERM or SIGINT.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig

	logger.LogInfo("shutdown", "", "signal received, shutting down")

	// Remove the addr file so it can't outlive the daemon and mislead tooling
	// into connecting to a dead port.
	os.Remove(addrPath)

	shutdown(srv, mdnsAdv, logger)
}

// runCapacityRefreshLoop keeps the LiteLLM model-capacity cache current,
// retrying failed fetches with exponential backoff so a daemon started
// offline recovers as soon as connectivity returns (rather than waiting
// the full successInterval for the next attempt).
func runCapacityRefreshLoop(ctx context.Context, logger outbound.Logger, initialBackoff, maxBackoff, successInterval time.Duration) {
	backoff := initialBackoff
	for {
		if !capacity.IsCacheStale() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(successInterval):
			}
			continue
		}

		config, err := capacity.FetchAndCacheLiteLLMData()
		if err != nil {
			logger.LogError("capacity", "", fmt.Sprintf("remote refresh failed (retry in %s): %v", backoff, err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		logger.LogInfo("capacity", "", fmt.Sprintf("cached %d remote models from LiteLLM", len(config.Models)))
		backoff = initialBackoff
		select {
		case <-ctx.Done():
			return
		case <-time.After(successInterval):
		}
	}
}
