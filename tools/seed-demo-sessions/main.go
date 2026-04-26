// seed-demo-sessions writes hand-crafted SessionState JSON files into the
// Irrlicht daemon's instances directory so the macOS menu bar can be
// screenshotted in a known, controlled state — without staging real
// concurrent agents across Claude Code, Codex, and Pi.
//
// Demo data lives in scenarios/*.json (one scenario per file). Each session
// uses *_offset_seconds fields relative to "now" instead of absolute
// timestamps, so the screenshot always shows recent activity.
//
// Default invocation prompts the user to pick a scenario, backs up
// existing sessions, writes the chosen scenario, and restarts the
// Irrlicht macOS app. `--restore` reverses it.
package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"irrlicht/core/domain/session"
)

//go:embed scenarios/*.json
var embeddedScenarios embed.FS

// scenarioFile is the on-disk shape of a scenario JSON file.
type scenarioFile struct {
	Description string             `json:"description"`
	Sessions    []*scenarioSession `json:"sessions"`
}

// scenarioSession embeds SessionState and adds *_offset_seconds fields that
// are resolved against "now" at seed time. The embedded SessionState's
// FirstSeen/UpdatedAt/WaitingStartTime are ignored on read.
type scenarioSession struct {
	session.SessionState
	FirstSeenOffsetSeconds        int64  `json:"first_seen_offset_seconds"`
	UpdatedAtOffsetSeconds        int64  `json:"updated_at_offset_seconds"`
	WaitingStartTimeOffsetSeconds *int64 `json:"waiting_start_time_offset_seconds,omitempty"`
}

func (s *scenarioSession) materialize(now time.Time) *session.SessionState {
	out := s.SessionState // value copy
	out.FirstSeen = now.Unix() + s.FirstSeenOffsetSeconds
	out.UpdatedAt = now.Unix() + s.UpdatedAtOffsetSeconds
	if s.WaitingStartTimeOffsetSeconds != nil {
		v := now.Unix() + *s.WaitingStartTimeOffsetSeconds
		out.WaitingStartTime = &v
	} else {
		out.WaitingStartTime = nil
	}
	return &out
}

type scenarioInfo struct {
	Name        string
	Description string
	File        scenarioFile
}

func main() {
	var (
		outDir       string
		clean        bool
		restart      bool
		appPath      string
		restore      bool
		force        bool
		skipRestart  bool
		scenarioName string
		scenariosDir string
		listOnly     bool
	)

	defaultOut, _ := defaultInstancesDir()
	flag.StringVar(&outDir, "out", defaultOut, "instances directory")
	flag.BoolVar(&clean, "clean", true, "move existing instance files to instances.bak.<ts>/ before writing")
	flag.BoolVar(&restart, "restart", true, "kill the bundled daemon + Irrlicht app and relaunch after seeding")
	flag.BoolVar(&skipRestart, "no-restart", false, "alias for --restart=false")
	flag.StringVar(&appPath, "app", "", "Irrlicht.app bundle to relaunch (auto-detected if empty)")
	flag.BoolVar(&restore, "restore", false, "delete demo-*.json and restore the most recent backup")
	flag.BoolVar(&force, "force", false, "also kill standalone irrlichd processes outside the app bundle")
	flag.StringVar(&scenarioName, "scenario", "", "scenario to seed (e.g. multi-agent). If empty, prompt interactively")
	flag.StringVar(&scenariosDir, "scenarios-dir", "", "load scenarios from this dir instead of the embedded set")
	flag.BoolVar(&listOnly, "list", false, "list available scenarios and exit")
	flag.Parse()

	if skipRestart {
		restart = false
	}
	if outDir == "" {
		fail("could not resolve instances dir; pass --out")
	}

	scenarios, err := loadScenarios(scenariosDir)
	if err != nil {
		fail("load scenarios: %v", err)
	}
	if len(scenarios) == 0 {
		fail("no scenarios found")
	}

	if listOnly {
		printScenarios(scenarios)
		return
	}

	if appPath == "" {
		appPath = autodetectApp()
	}

	if restore {
		if err := runRestore(outDir, appPath, restart, force); err != nil {
			fail("restore: %v", err)
		}
		return
	}

	chosen, err := chooseScenario(scenarios, scenarioName)
	if err != nil {
		fail("%v", err)
	}

	if err := runSeed(outDir, appPath, clean, restart, force, chosen); err != nil {
		fail("seed: %v", err)
	}
}

func runSeed(outDir, appPath string, clean, restart, force bool, scenario scenarioInfo) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}

	if restart {
		if err := stopApp(appPath, force); err != nil {
			return err
		}
	}

	if clean {
		backup, err := backupExisting(outDir)
		if err != nil {
			return err
		}
		if backup != "" {
			fmt.Printf("backed up existing sessions → %s\n", backup)
		}
	}

	now := time.Now()
	for _, src := range scenario.File.Sessions {
		s := src.materialize(now)
		path := filepath.Join(outDir, s.SessionID+".json")
		data, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal %s: %w", s.SessionID, err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	fmt.Printf("wrote scenario %q (%d sessions) to %s\n", scenario.Name, len(scenario.File.Sessions), outDir)

	if restart {
		if err := launchApp(appPath); err != nil {
			return err
		}
		fmt.Printf("relaunched %s — open the menu bar to screenshot\n", appPath)
	} else {
		fmt.Println("skipped restart; quit and relaunch the app to see demo sessions")
	}
	return nil
}

func runRestore(outDir, appPath string, restart, force bool) error {
	if restart {
		if err := stopApp(appPath, force); err != nil {
			return err
		}
	}

	entries, _ := os.ReadDir(outDir)
	demos := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "demo-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if err := os.Remove(filepath.Join(outDir, e.Name())); err != nil {
			return fmt.Errorf("remove %s: %w", e.Name(), err)
		}
		demos++
	}
	fmt.Printf("removed %d demo session files\n", demos)

	parent := filepath.Dir(outDir)
	parentEntries, err := os.ReadDir(parent)
	if err != nil {
		return fmt.Errorf("read %s: %w", parent, err)
	}
	var backups []string
	for _, e := range parentEntries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "instances.bak.") {
			backups = append(backups, e.Name())
		}
	}
	if len(backups) == 0 {
		fmt.Println("no backups found; nothing to restore")
	} else {
		sort.Strings(backups)
		latest := backups[len(backups)-1]
		src := filepath.Join(parent, latest)
		moved, err := moveAllJSON(src, outDir)
		if err != nil {
			return err
		}
		fmt.Printf("restored %d files from %s\n", moved, src)
		if rest, _ := os.ReadDir(src); len(rest) == 0 {
			_ = os.Remove(src)
		}
	}

	if restart {
		if err := launchApp(appPath); err != nil {
			return err
		}
		fmt.Printf("relaunched %s\n", appPath)
	}
	return nil
}

// loadScenarios reads scenarios from `dir` if non-empty, otherwise from the
// embedded scenarios/*.json bundled at build time.
func loadScenarios(dir string) ([]scenarioInfo, error) {
	type entry struct {
		name string
		read func() ([]byte, error)
	}
	var entries []entry

	if dir != "" {
		dirEntries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read scenarios dir %s: %w", dir, err)
		}
		for _, e := range dirEntries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			entries = append(entries, entry{
				name: strings.TrimSuffix(e.Name(), ".json"),
				read: func() ([]byte, error) { return os.ReadFile(path) },
			})
		}
	} else {
		dirEntries, err := fs.ReadDir(embeddedScenarios, "scenarios")
		if err != nil {
			return nil, fmt.Errorf("read embedded scenarios: %w", err)
		}
		for _, e := range dirEntries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".json")
			path := filepath.Join("scenarios", e.Name())
			entries = append(entries, entry{
				name: name,
				read: func() ([]byte, error) { return embeddedScenarios.ReadFile(path) },
			})
		}
	}

	var out []scenarioInfo
	for _, e := range entries {
		data, err := e.read()
		if err != nil {
			return nil, fmt.Errorf("read scenario %s: %w", e.name, err)
		}
		var sf scenarioFile
		if err := json.Unmarshal(data, &sf); err != nil {
			return nil, fmt.Errorf("parse scenario %s: %w", e.name, err)
		}
		out = append(out, scenarioInfo{
			Name:        e.name,
			Description: sf.Description,
			File:        sf,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func chooseScenario(scenarios []scenarioInfo, name string) (scenarioInfo, error) {
	if name != "" {
		for _, s := range scenarios {
			if s.Name == name {
				return s, nil
			}
		}
		return scenarioInfo{}, fmt.Errorf("unknown scenario %q (available: %s)", name, scenarioNames(scenarios))
	}
	if !isInteractive() {
		if len(scenarios) == 1 {
			return scenarios[0], nil
		}
		return scenarioInfo{}, fmt.Errorf("non-interactive run with multiple scenarios — pass --scenario NAME (available: %s)", scenarioNames(scenarios))
	}
	return promptScenario(scenarios)
}

func promptScenario(scenarios []scenarioInfo) (scenarioInfo, error) {
	fmt.Println("Available scenarios:")
	for i, s := range scenarios {
		fmt.Printf("  [%d] %-20s %d sessions — %s\n", i+1, s.Name, len(s.File.Sessions), s.Description)
	}
	fmt.Print("\nPick one (number or name): ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return scenarioInfo{}, fmt.Errorf("read selection: %w", err)
	}
	input := strings.TrimSpace(line)
	if input == "" {
		return scenarioInfo{}, fmt.Errorf("no scenario selected")
	}
	if n, err := strconv.Atoi(input); err == nil {
		if n < 1 || n > len(scenarios) {
			return scenarioInfo{}, fmt.Errorf("selection %d out of range (1..%d)", n, len(scenarios))
		}
		return scenarios[n-1], nil
	}
	for _, s := range scenarios {
		if s.Name == input {
			return s, nil
		}
	}
	return scenarioInfo{}, fmt.Errorf("unknown scenario %q (available: %s)", input, scenarioNames(scenarios))
}

func printScenarios(scenarios []scenarioInfo) {
	fmt.Println("Available scenarios:")
	for _, s := range scenarios {
		fmt.Printf("  %-20s %d sessions — %s\n", s.Name, len(s.File.Sessions), s.Description)
	}
}

func scenarioNames(scenarios []scenarioInfo) string {
	names := make([]string, 0, len(scenarios))
	for _, s := range scenarios {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}

func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func backupExisting(outDir string) (string, error) {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", outDir, err)
	}
	var jsonFiles []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			jsonFiles = append(jsonFiles, e)
		}
	}
	if len(jsonFiles) == 0 {
		return "", nil
	}
	parent := filepath.Dir(outDir)
	stamp := time.Now().Format("20060102-150405")
	backup := filepath.Join(parent, "instances.bak."+stamp)
	if err := os.MkdirAll(backup, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", backup, err)
	}
	for _, e := range jsonFiles {
		src := filepath.Join(outDir, e.Name())
		dst := filepath.Join(backup, e.Name())
		if err := os.Rename(src, dst); err != nil {
			return "", fmt.Errorf("move %s → %s: %w", src, dst, err)
		}
	}
	return backup, nil
}

func moveAllJSON(src, dst string) (int, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", src, err)
	}
	moved := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		if err := os.Rename(from, to); err != nil {
			return moved, fmt.Errorf("move %s → %s: %w", from, to, err)
		}
		moved++
	}
	return moved, nil
}

func defaultInstancesDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "Irrlicht", "instances"), nil
}

func autodetectApp() string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, "Applications", "Irrlicht-dev.app"),
		filepath.Join(home, "Applications", "Irrlicht.app"),
		"/Applications/Irrlicht.app",
		"/Applications/Irrlicht-dev.app",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func stopApp(appPath string, force bool) error {
	if appPath == "" {
		fmt.Println("no app bundle found; skipping app stop (will only stop daemons matching the app)")
	}

	stopProcessByPath(appPath, "Irrlicht app")

	if appPath != "" {
		bundleDaemon := filepath.Join(appPath, "Contents", "MacOS", "irrlichd")
		stopProcessByPath(bundleDaemon, "bundled irrlichd")
	}

	if force {
		stopProcessByName("irrlichd", "all irrlichd")
	} else {
		listOtherDaemons(appPath)
	}

	time.Sleep(400 * time.Millisecond)
	return nil
}

func launchApp(appPath string) error {
	if appPath == "" {
		return fmt.Errorf("no app bundle found; pass --app PATH")
	}
	cmd := exec.Command("open", "-g", appPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("open %s: %w (%s)", appPath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func stopProcessByPath(path, label string) {
	if path == "" {
		return
	}
	pids := pgrepFull(path)
	if len(pids) == 0 {
		return
	}
	fmt.Printf("stopping %s (%d pid%s)\n", label, len(pids), pluralS(len(pids)))
	for _, pid := range pids {
		_ = exec.Command("kill", pid).Run()
	}
}

func stopProcessByName(name, label string) {
	pids := pgrepFull(name)
	if len(pids) == 0 {
		return
	}
	fmt.Printf("stopping %s (%d pid%s) — --force\n", label, len(pids), pluralS(len(pids)))
	for _, pid := range pids {
		_ = exec.Command("kill", pid).Run()
	}
}

func listOtherDaemons(appPath string) {
	pids := pgrepFull("irrlichd")
	if len(pids) == 0 {
		return
	}
	bundleDaemon := ""
	if appPath != "" {
		bundleDaemon = filepath.Join(appPath, "Contents", "MacOS", "irrlichd")
	}
	var others []string
	for _, pid := range pids {
		out, err := exec.Command("ps", "-o", "command=", "-p", pid).Output()
		if err != nil {
			continue
		}
		cmd := strings.TrimSpace(string(out))
		if bundleDaemon != "" && strings.HasPrefix(cmd, bundleDaemon) {
			continue
		}
		others = append(others, fmt.Sprintf("  pid %s: %s", pid, cmd))
	}
	if len(others) > 0 {
		fmt.Printf("note: %d standalone irrlichd process%s left running (pass --force to also kill):\n%s\n",
			len(others), pluralES(len(others)), strings.Join(others, "\n"))
	}
}

func pgrepFull(needle string) []string {
	if needle == "" {
		return nil
	}
	out, err := exec.Command("pgrep", "-f", needle).Output()
	if err != nil {
		return nil
	}
	var pids []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			pids = append(pids, line)
		}
	}
	return pids
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func pluralES(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "seed-demo-sessions: "+format+"\n", a...)
	os.Exit(1)
}
