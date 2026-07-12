package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/logging"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
)

// dataDir returns the irrlichd state directory (~/.local/share/irrlicht).
// home should come from os.UserHomeDir(); pass "" only when the lookup
// failed.
//
// IRRLICHT_HOME relocates this tree — socket, addr file, history rollups,
// the on-disk web fallback, and recordings all live beneath it. The session
// store, per-session ledgers, and cost store live under different roots
// (Application Support); stateStoreDir routes those through IRRLICHT_HOME too,
// so a dev/test daemon is fully isolated from the production install (and from
// other worktrees) without touching ~/.local/share/irrlicht/ or
// ~/Library/Application Support/Irrlicht/. Recordings still honor the narrower
// IRRLICHT_RECORDINGS_DIR override when set.
func dataDir(home string) string {
	if v := os.Getenv("IRRLICHT_HOME"); v != "" {
		return v
	}
	if home == "" {
		return "/tmp/irrlicht"
	}
	return filepath.Join(home, ".local", "share", "irrlicht")
}

// stateStoreDir returns the directory for a named state store (e.g. "instances",
// "cost", "sessions") when IRRLICHT_HOME is set, so every store nests beneath it
// for full isolation. Returns "" when IRRLICHT_HOME is unset, signaling the
// caller to keep its production-default location unchanged. Needed because the
// session repo and cost store root under ~/Library/Application Support/Irrlicht
// and ledgers under ~/.local/share/irrlicht/sessions — none of which flow
// through dataDir.
func stateStoreDir(sub string) string {
	if v := os.Getenv("IRRLICHT_HOME"); v != "" {
		return filepath.Join(v, sub)
	}
	return ""
}

// resolveRecordingsDir returns where lifecycle recordings live: the
// IRRLICHT_RECORDINGS_DIR override when set, else <dir-of-socket>/recordings
// (IRRLICHT_HOME-relative via sockPath). Shared by the recorder writer and the
// History tab's read-side concurrency reader (#751) so the two never drift.
func resolveRecordingsDir(sockPath string) string {
	if d := os.Getenv("IRRLICHT_RECORDINGS_DIR"); d != "" {
		return d
	}
	return filepath.Join(filepath.Dir(sockPath), "recordings")
}

// resolveSessionRepo resolves the on-disk session store, honoring IRRLICHT_HOME
// for both the instances dir and the per-session ledger dir. It performs no
// pruning or sweeps — the daemon layers those on at startup; --diagnose must
// not mutate state. Shared so a diagnostics snapshot reads exactly the stores
// the daemon writes (no IRRLICHT_HOME drift between the two paths).
func resolveSessionRepo() (*filesystem.SessionRepository, error) {
	if dir := stateStoreDir("sessions"); dir != "" {
		metrics.SetLedgerDir(dir)
	}
	if dir := stateStoreDir("instances"); dir != "" {
		return filesystem.NewWithDir(dir), nil
	}
	return filesystem.New()
}

// buildDiagnostics constructs the diagnostics bundle service (issue #736) from
// the daemon's resolved stores. Shared by the GET /debug/bundle handler and the
// --diagnose CLI so both snapshot the exact same locations. fsRepo supplies both
// the (uncached) session list and the instances dir; ledger and log dirs honor
// IRRLICHT_HOME via the same accessors the daemon writes through.
func buildDiagnostics(fsRepo *filesystem.SessionRepository, allAgents []agent.Agent, cfg config.Config) *services.DiagnosticsService {
	home, _ := os.UserHomeDir()
	ledgerDir, _ := metrics.LedgerDir()
	logsDir, _ := logging.LogDir()
	return services.NewDiagnosticsService(services.DiagnosticsServiceDeps{
		Repo:           fsRepo,
		Obs:            processlifecycle.Observer(),
		IsAlive:        processlifecycle.IsAlive,
		Agents:         allAgents,
		DefaultAdapter: claudecode.AdapterName,
		Cfg:            cfg,
		Version:        Version,
		Paths: services.DiagnosticsPaths{
			Home:            home,
			InstancesDir:    fsRepo.InstancesDir(),
			LedgerDir:       ledgerDir,
			LogsDir:         logsDir,
			PermissionsFile: filepath.Join(dataDir(home), "permissions.json"),
		},
	})
}

// runDiagnose writes a diagnostics bundle to the current directory and exits.
// For headless / curl-only installs that can't hit GET /debug/bundle. It
// resolves the same stores the daemon would (honoring IRRLICHT_HOME) without
// starting the daemon.
func runDiagnose() {
	fsRepo, err := resolveSessionRepo()
	if err != nil {
		log.Fatalf("diagnose: init session repo: %v", err)
	}
	cfg := config.Default()
	if v := os.Getenv("IRRLICHT_MAX_SESSION_AGE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.MaxSessionAge = d
		}
	}
	const out = "irrlicht-diag.tar.gz"
	f, err := os.Create(out)
	if err != nil {
		log.Fatalf("diagnose: create %s: %v", out, err)
	}
	defer f.Close()
	if err := buildDiagnostics(fsRepo, agents.All(), cfg).WriteBundle(f); err != nil {
		log.Fatalf("diagnose: write bundle: %v", err)
	}
	abs, _ := filepath.Abs(out)
	fmt.Printf("Wrote diagnostics bundle to %s\n", abs)
}

// socketPath returns the Unix socket path for irrlichd. It routes through
// dataDir so the IRRLICHT_HOME override is honored even when os.UserHomeDir()
// fails (dataDir maps an empty home to /tmp/irrlicht when no override is set).
func socketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(dataDir(home), "irrlichd.sock")
}

// resolveUIDir locates the directory containing the dashboard's index.html.
// See resolveUIDirFor for the search order.
func resolveUIDir() string {
	exe, _ := os.Executable()
	home, _ := os.UserHomeDir()
	return resolveUIDirFor(os.Getenv(envUIDir), exe, home)
}

// resolveUIDirFor is the pure variant of resolveUIDir for testing. Search
// order: env → <exe>/../Resources/web (production .app bundle) → <home>/
// .local/share/irrlicht/web (daemon-only curl install) → walk up from <exe>
// to the enclosing repo root (a directory containing .git) and check for
// platforms/web/index.html (dev checkout). Returns "" on miss.
//
// The dev walk-up is bounded by .git so it can't escape a git worktree
// into a parent repo's platforms/web/ — that bug would silently serve the
// wrong dashboard during dev.
func resolveUIDirFor(env, exe, home string) string {
	if hasIndexHTML(env) {
		return env
	}
	if cand := uiDirFromExeResources(exe); cand != "" {
		return cand
	}
	if cand := uiDirFromHome(home); cand != "" {
		return cand
	}
	return uiDirFromRepoRoot(exe)
}

// hasIndexHTML reports whether dir contains index.html — i.e. the dashboard is
// installed there. Empty dir is always a miss.
func hasIndexHTML(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "index.html"))
	return err == nil
}

// uiDirFromExeResources checks <exe>/../Resources/web, the production .app
// bundle layout. Returns "" when exe is unknown or the candidate has no UI.
func uiDirFromExeResources(exe string) string {
	if exe == "" {
		return ""
	}
	if cand := filepath.Join(filepath.Dir(exe), "..", "Resources", "web"); hasIndexHTML(cand) {
		return cand
	}
	return ""
}

// uiDirFromHome checks <home>/.local/share/irrlicht/web, the daemon-only curl
// install layout. Returns "" when home is unknown or the candidate has no UI.
func uiDirFromHome(home string) string {
	if home == "" {
		return ""
	}
	if cand := filepath.Join(dataDir(home), "web"); hasIndexHTML(cand) {
		return cand
	}
	return ""
}

// uiDirFromRepoRoot walks up from <exe> to the enclosing repo root (a
// directory containing .git) and checks for platforms/web/index.html (dev
// checkout). Returns "" on miss.
//
// The walk-up is bounded by .git so it can't escape a git worktree into a
// parent repo's platforms/web/ — that bug would silently serve the wrong
// dashboard during dev.
func uiDirFromRepoRoot(exe string) string {
	if exe == "" {
		return ""
	}
	dir := filepath.Dir(exe)
	for range 8 {
		if isGitRepoRoot(dir) {
			if cand := filepath.Join(dir, "platforms", "web"); hasIndexHTML(cand) {
				return cand
			}
			return "" // repo root found, no UI inside — don't escape
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// isGitRepoRoot reports whether dir contains a .git entry, marking it as the
// top of a git checkout or worktree.
func isGitRepoRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
