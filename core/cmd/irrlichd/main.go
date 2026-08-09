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

// cliAction is what one irrlichd command line selects.
type cliAction int

const (
	actionBeacon cliAction = iota
	actionVersion
	actionUninstallHooks
	actionPrintHookConfigs
	actionUninstallTaskEta
	actionDiagnose
	actionUnknownSubcommand
	actionRunDaemon
)

// selectAction resolves a command line to the one thing irrlichd will do.
//
// The order is part of the contract, not an implementation detail, and two
// positions in it are load-bearing:
//
//   - The beacon is FIRST, ahead of --version. Every installed beacon command
//     line ends in --version as a guard against an older irrlichd starting a
//     daemon instead of posting a hook (see hookbeacon.LegacyGuardToken). Moving
//     the version check back in front would turn every installed beacon into a
//     version banner — silently, since the beacon is not supposed to say
//     anything on a healthy path either.
//   - actionUnknownSubcommand is LAST, and it only fires on a positional token.
//     Unknown FLAGS still fall through to the daemon, which is the behaviour
//     every irrlichd has had; narrowing the change to positionals keeps it from
//     touching any existing invocation while still closing the fall-through that
//     makes a beacon-shaped command line start a daemon on a future binary that
//     renames the verb.
func selectAction(args []string) cliAction {
	switch {
	case hookbeacon.IsInvocation(args):
		return actionBeacon
	case hasFlagIn(args, "--version"), hasFlagIn(args, "-v"):
		return actionVersion
	case hasFlagIn(args, "--uninstall-hooks"):
		return actionUninstallHooks
	case hasFlagIn(args, "--print-hook-configs"):
		return actionPrintHookConfigs
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
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		return arg, true
	}
	return "", false
}

func main() {
	switch selectAction(os.Args[1:]) {
	case actionBeacon:
		// Post always returns 0 — that contract is the whole of #1373; see the
		// hookbeacon package doc for what a non-zero exit does to a tool call.
		os.Exit(hookbeacon.Post(hookbeacon.Options{
			Args:   os.Args[2:],
			Stdin:  os.Stdin,
			Stderr: os.Stderr,
		}))
	case actionVersion:
		fmt.Printf("irrlichd version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	case actionUninstallHooks:
		uninstallHooks()
		os.Exit(0)
	case actionPrintHookConfigs:
		if err := printHookConfigs(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case actionUninstallTaskEta:
		uninstallTaskEtaBlocks()
		os.Exit(0)
	case actionDiagnose:
		runDiagnose()
		os.Exit(0)
	case actionUnknownSubcommand:
		// stderr, never stdout, and the message is the only output: if this is
		// ever reached by a hook-shaped invocation on a future binary that
		// renamed the verb, an empty stdout is what keeps a non-zero exit from
		// reading as a "deny" decision to a fail-closed pre-tool hook.
		name, _ := firstPositional(os.Args[1:])
		fmt.Fprintf(os.Stderr, "irrlichd: unknown subcommand %q\n", name)
		os.Exit(2)
	}
	runDaemon()
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
func uninstallHooks() {
	configs, err := agents.HookConfigs(agents.All())
	if err != nil {
		log.Fatalf("failed to resolve the agent config files to uninstall from: %v", err)
	}
	home, _ := os.UserHomeDir()
	if failed := uninstallHookConfigs(os.Stdout, configs, filesystem.NewPermissionStore(dataDir(home))); failed > 0 {
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
func uninstallHookConfigs(w io.Writer, configs []agents.HookConfig, store outbound.PermissionStore) int {
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
func denyHooksPermissions(w io.Writer, configs []agents.HookConfig, store outbound.PermissionStore) {
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

// printHookConfigs writes the resolved absolute path of every agent config file
// irrlicht installs hooks into, one per line.
//
// It exists for the onboarding recording rig (#1357): a recording daemon runs
// grant-all and installs its hooks into the user's REAL config, so the rig has
// to back those files up before spawning it — and asking the daemon is the only
// way that list cannot drift from the adapters that actually write them. An
// empty result is an error, not an empty list: the rig would read "nothing to
// protect" as success and record over the user's config unprotected.
func printHookConfigs(w io.Writer) error {
	configs, err := agents.HookConfigs(agents.All())
	if err != nil {
		return fmt.Errorf("failed to resolve the agent config files irrlicht installs hooks into: %w", err)
	}
	if len(configs) == 0 {
		return fmt.Errorf("no adapter declares a hooks permission")
	}
	writeHookConfigPaths(w, configs)
	return nil
}

// writeHookConfigPaths prints each distinct config path once. The dedup is
// load-bearing rather than cosmetic: the rig keys its backups by line index, so
// a path listed twice would be backed up under two indices and restored twice.
func writeHookConfigPaths(w io.Writer, configs []agents.HookConfig) {
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

// uninstallTaskEtaBlocks removes irrlicht's managed task-eta and
// task-summary blocks from ~/.claude/CLAUDE.md.
func uninstallTaskEtaBlocks() {
	etaModified, err := claudecode.UninstallTaskEtaBlock()
	if err != nil {
		log.Fatalf("failed to uninstall task-eta block: %v", err)
	}
	summaryModified, err := claudecode.UninstallTaskSummaryBlock()
	if err != nil {
		log.Fatalf("failed to uninstall task-summary block: %v", err)
	}
	if etaModified || summaryModified {
		fmt.Println("Removed irrlicht task-eta/task-summary blocks from ~/.claude/CLAUDE.md")
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
