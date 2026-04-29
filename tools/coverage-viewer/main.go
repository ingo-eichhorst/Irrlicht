// Coverage-viewer is a small dev-only HTTP server that renders the agent ×
// scenario coverage matrix, a per-cell pipeline drilldown, and a per-session
// swim-lane timeline. It reads canonical files in place — no cache, no DB.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"irrlicht/core/adapters/inbound/agents/aider"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/pi"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/pkg/tailer"
)

// Lane labels used in the timeline swim-lanes — kept as constants so the Go
// emitter and the JS consumer share a single source of truth.
const (
	laneDriver     = "driver"
	laneAgent      = "agent"
	laneToolResult = "tool_result"
	laneHook       = "hook"
	laneDaemon     = "daemon"
	laneSubagent   = "subagent"
)

// safeSegment matches the only shapes adapter and scenario names take in this
// repo: lowercase ascii words with dashes, optionally suffixed with a short hex
// hash. Anything else (slashes, dots, ..) is rejected to prevent path escape.
var safeSegment = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

// withinRoot returns nil iff cleaned candidate stays inside rootAbs. Both
// arguments must already be absolute paths. Note: this does not resolve
// symlinks — fine for a localhost dev tool, but anyone reusing these helpers
// in a network-exposed daemon should `filepath.EvalSymlinks` first.
func withinRoot(rootAbs, candidate string) error {
	rel, err := filepath.Rel(rootAbs, filepath.Clean(candidate))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes root")
	}
	return nil
}

// safeJoin joins segments under root and confirms the cleaned result stays
// inside root. Rejects empty/absolute segments and parent-traversal (..).
// Use for any path built from request-derived input.
func safeJoin(root string, segs ...string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("safeJoin: empty root")
	}
	for _, s := range segs {
		if s == "" {
			return "", fmt.Errorf("safeJoin: empty segment")
		}
		if filepath.IsAbs(s) {
			return "", fmt.Errorf("safeJoin: absolute segment")
		}
		for _, part := range strings.Split(filepath.ToSlash(s), "/") {
			if part == ".." {
				return "", fmt.Errorf("safeJoin: parent traversal")
			}
		}
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cleaned := filepath.Clean(filepath.Join(append([]string{rootAbs}, segs...)...))
	if err := withinRoot(rootAbs, cleaned); err != nil {
		return "", fmt.Errorf("safeJoin: %w", err)
	}
	return cleaned, nil
}

// ensureUnderRoot is a defense-in-depth gate at file-open sinks: even if a
// caller built the path through safeJoin, helpers re-verify that the input
// path stays inside *rootDir before touching the filesystem.
func ensureUnderRoot(path string) error {
	if *rootDir == "" {
		return fmt.Errorf("ensureUnderRoot: empty root")
	}
	rootAbs, err := filepath.Abs(*rootDir)
	if err != nil {
		return err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := withinRoot(rootAbs, pathAbs); err != nil {
		return fmt.Errorf("ensureUnderRoot: %w", err)
	}
	return nil
}

const (
	githubRepoURL  = "https://github.com/ingo-eichhorst/Irrlicht"
	scenariosJSON  = ".claude/skills/ir:onboard-agent/scenarios.json"
	featuresJSON   = "replaydata/agents/features.json"
	replayAgentDir = "replaydata/agents"
)

var (
	addr    = flag.String("addr", "127.0.0.1:7838", "HTTP listen address")
	rootDir = flag.String("root", "", "repo root (auto-detected if empty)")
)

func main() {
	flag.Parse()
	if *rootDir == "" {
		root, err := findRepoRoot()
		if err != nil {
			log.Fatalf("find repo root: %v", err)
		}
		*rootDir = root
	}
	log.Printf("repo root: %s", *rootDir)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/matrix", handleMatrix)
	mux.HandleFunc("/api/scenario/", handleScenario)
	mux.HandleFunc("/api/timeline/", handleTimeline)
	mux.Handle("/", http.FileServer(http.Dir(filepath.Join(execDir(), "ui"))))

	log.Printf("coverage-viewer listening on http://%s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// findRepoRoot walks up from CWD looking for go.work or .git.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir, nil
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.work or .git found above %s", dir)
		}
		dir = parent
	}
}

// execDir returns the directory containing this binary's source/ui assets.
// When run via `go run .` it's the source dir; for a built binary, fall back
// to <rootDir>/tools/coverage-viewer for serving ui/.
func execDir() string {
	if wd, err := os.Getwd(); err == nil {
		// `go run .` in tools/coverage-viewer → cwd contains ui/
		if _, err := os.Stat(filepath.Join(wd, "ui", "index.html")); err == nil {
			return wd
		}
	}
	return filepath.Join(*rootDir, "tools", "coverage-viewer")
}

// ---------- /api/matrix ----------

type cell struct {
	State  string `json:"state"`            // "covered" | "staged-only" | "missing-prompt" | "n/a"
	Reason string `json:"reason,omitempty"` // human-readable detail
}

type scenarioMeta struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Requires    []string `json:"requires"`
}

type featureMeta struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Category    string `json:"category,omitempty"`
	Description string `json:"description"`
}

type categoryMeta struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type matrixResp struct {
	Adapters     []string                   `json:"adapters"`
	Scenarios    []scenarioMeta             `json:"scenarios"`
	Cells        map[string]map[string]cell `json:"cells"` // adapter → scenario → cell
	Features     []featureMeta              `json:"features"`
	Categories   []categoryMeta             `json:"categories,omitempty"`
	Capabilities map[string]map[string]any  `json:"capabilities"` // adapter → feature → true|false|"unknown"
}

func handleMatrix(w http.ResponseWriter, r *http.Request) {
	resp, err := buildMatrix()
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, resp)
}

func buildMatrix() (*matrixResp, error) {
	features, categories, err := loadFeatures()
	if err != nil {
		return nil, fmt.Errorf("features: %w", err)
	}
	scenarios, err := loadScenarios()
	if err != nil {
		return nil, fmt.Errorf("scenarios: %w", err)
	}
	adapters, err := discoverAdapters()
	if err != nil {
		return nil, fmt.Errorf("adapters: %w", err)
	}

	caps := make(map[string]map[string]any)
	exts := make(map[string]string)
	for _, a := range adapters {
		c, ext, err := loadCapabilities(a)
		if err != nil {
			return nil, fmt.Errorf("caps %s: %w", a, err)
		}
		caps[a] = c
		exts[a] = ext
	}

	cells := make(map[string]map[string]cell, len(adapters))
	for _, a := range adapters {
		cells[a] = make(map[string]cell, len(scenarios))
		for _, s := range scenarios {
			cells[a][s.Name] = deriveCell(a, s, caps[a], exts[a])
		}
	}

	metas := make([]scenarioMeta, len(scenarios))
	for i, s := range scenarios {
		metas[i] = scenarioMeta{Name: s.Name, Description: s.Description, Requires: s.Requires}
	}
	return &matrixResp{
		Adapters:     adapters,
		Scenarios:    metas,
		Cells:        cells,
		Features:     features,
		Categories:   categories,
		Capabilities: caps,
	}, nil
}

// deriveCell mirrors .claude/skills/ir:onboard-agent/skill.md step 2.
func deriveCell(adapter string, s scenario, caps map[string]any, transcriptExt string) cell {
	for _, req := range s.Requires {
		v, ok := caps[req]
		if !ok || v != true { // both false and "unknown" block
			return cell{State: "n/a", Reason: "missing capability: " + req}
		}
	}
	if _, ok := s.ByAdapter[adapter]; !ok {
		return cell{State: "missing-prompt", Reason: "no by_adapter." + adapter + " entry in scenarios.json"}
	}
	fixture, err := safeJoin(*rootDir, replayAgentDir, adapter, "scenarios", s.Name, "transcript."+transcriptExt)
	if err != nil {
		return cell{State: "n/a", Reason: "invalid path"}
	}
	if _, err := os.Stat(fixture); err == nil {
		return cell{State: "covered"}
	}
	return cell{State: "staged-only", Reason: "by_adapter present but no committed fixture"}
}

// ---------- /api/scenario/{adapter}/{scenario} ----------

type drilldownStep struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Link        string `json:"link"`
}

type drilldownResp struct {
	Adapter        string          `json:"adapter"`
	Scenario       string          `json:"scenario"`
	Description    string          `json:"description"`
	Requires       []string        `json:"requires"`
	State          string          `json:"state"`
	Reason         string          `json:"reason,omitempty"`
	Prompt         string          `json:"prompt,omitempty"`
	Settings       any             `json:"settings,omitempty"`
	TimeoutSeconds int             `json:"timeout_seconds,omitempty"`
	Verify         map[string]any  `json:"verify,omitempty"`
	Steps          []drilldownStep `json:"steps"`
	HasFixture     bool            `json:"has_fixture"`
}

func handleScenario(w http.ResponseWriter, r *http.Request) {
	adapter, scenarioName, ok := splitAdapterScenario(r.URL.Path, "/api/scenario/")
	if !ok {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	scenarios, err := loadScenarios()
	if err != nil {
		httpError(w, err)
		return
	}
	var s *scenario
	for i := range scenarios {
		if scenarios[i].Name == scenarioName {
			s = &scenarios[i]
			break
		}
	}
	if s == nil {
		http.Error(w, "scenario not found", http.StatusNotFound)
		return
	}
	caps, transcriptExt, err := loadCapabilities(adapter)
	if err != nil {
		httpError(w, err)
		return
	}
	c := deriveCell(adapter, *s, caps, transcriptExt)

	resp := drilldownResp{
		Adapter:     adapter,
		Scenario:    scenarioName,
		Description: s.Description,
		Requires:    s.Requires,
		State:       c.State,
		Reason:      c.Reason,
		Verify:      s.Verify,
		HasFixture:  c.State == "covered",
	}
	if ba, ok := s.ByAdapter[adapter]; ok {
		resp.Prompt = ba.Prompt
		resp.Settings = ba.Settings
		resp.TimeoutSeconds = ba.TimeoutSeconds
	}

	sha := headSHA()
	pl := func(rel string) string {
		return fmt.Sprintf("%s/blob/%s/%s", githubRepoURL, sha, rel)
	}
	driverScript := fmt.Sprintf(".claude/skills/ir:onboard-agent/scripts/drive-%s.sh", adapter)
	resp.Steps = []drilldownStep{
		{Title: "Precheck", Description: "Validate adapter, port 7837 free, daemon binary present, working tree clean.", Link: pl(".claude/skills/ir:onboard-agent/scripts/precheck.sh")},
		{Title: "Daemon spawn", Description: "irrlichd --record on 127.0.0.1:7837 with isolated IRRLICHT_RECORDINGS_DIR. SIGINT → 6s grace → SIGTERM → SIGKILL teardown.", Link: pl(".claude/skills/ir:onboard-agent/scripts/run-cell.sh")},
		{Title: "Driver", Description: "Drives the " + adapter + " CLI with the prompt + settings shown above.", Link: pl(driverScript)},
		{Title: "Curate fixture", Description: "Bundle the recording + per-session transcripts (and any subagents) into replaydata/agents/" + adapter + "/scenarios/" + scenarioName + "/.", Link: pl("tools/curate-lifecycle-fixture.sh")},
		{Title: "Replay", Description: "replay --quiet --out <report> runs the simulator against the staged + committed transcripts and emits per-state-transition diffs.", Link: pl("tools/replay-fixtures.sh")},
		{Title: "Verify", Description: "Assert the verify block (above) holds: transitions topology, tool-call presence, hook firings, final state, etc.", Link: pl(scenariosJSON)},
	}
	writeJSON(w, resp)
}

// ---------- /api/timeline/{adapter}/{scenario} ----------

type timelineEntry struct {
	TS        time.Time `json:"ts"`
	Lane      string    `json:"lane"`              // driver | agent | tool_result | hook | daemon | subagent
	Kind      string    `json:"kind"`              // narrower (state_transition, tool_call, …)
	SessionID string    `json:"session_id,omitempty"`
	ParentID  string    `json:"parent_id,omitempty"`
	Title     string    `json:"title"`
	Payload   any       `json:"payload"` // lifecycle.Event for daemon events, raw map[string]any for transcripts
}

type timelineResp struct {
	Adapter   string          `json:"adapter"`
	Scenario  string          `json:"scenario"`
	Note      string          `json:"note,omitempty"`
	Entries   []timelineEntry `json:"entries"`
}

func handleTimeline(w http.ResponseWriter, r *http.Request) {
	adapter, scenarioName, ok := splitAdapterScenario(r.URL.Path, "/api/timeline/")
	if !ok {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	scenarioDir, err := safeJoin(*rootDir, replayAgentDir, adapter, "scenarios", scenarioName)
	if err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	resp := timelineResp{Adapter: adapter, Scenario: scenarioName}

	// scenarioDir is safeJoin-validated; loadEvents/loadTranscript re-gate via
	// ensureUnderRoot at their os.Open sinks, and os.ReadDir below has its own
	// inline gate. Leaf paths can therefore use plain filepath.Join.
	eventEntries, parents, err := loadEvents(filepath.Join(scenarioDir, "events.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		httpError(w, err)
		return
	}
	resp.Entries = append(resp.Entries, eventEntries...)

	parser := newParserFor(adapter)
	if parser == nil {
		resp.Note = "transcript not parsed for adapter: " + adapter + " (e.g. aider markdown)"
	} else {
		txEntries, err := loadTranscript(filepath.Join(scenarioDir, "transcript.jsonl"), parser, laneAgent, primarySessionID(eventEntries))
		if err != nil && !os.IsNotExist(err) {
			httpError(w, err)
			return
		}
		resp.Entries = append(resp.Entries, txEntries...)
	}

	// If neither events.jsonl nor transcript.jsonl produced anything, the
	// (adapter, scenario) directory likely doesn't exist — surface that.
	if len(resp.Entries) == 0 {
		http.Error(w, "no fixture", http.StatusNotFound)
		return
	}

	subDir := filepath.Join(scenarioDir, "subagents")
	if err := ensureUnderRoot(subDir); err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	if entries, _ := os.ReadDir(subDir); len(entries) > 0 && parser != nil {
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".jsonl") {
				continue
			}
			childID := strings.TrimSuffix(ent.Name(), ".jsonl")
			parent := parents[childID]
			// Fresh parser per subagent file — claudecode.Parser is stateful
			// (lastRequestID / pendingContrib) and would carry state across files otherwise.
			subEntries, err := loadTranscript(filepath.Join(subDir, ent.Name()), newParserFor(adapter), laneSubagent, childID)
			if err != nil {
				continue
			}
			if parent != "" {
				for i := range subEntries {
					subEntries[i].ParentID = parent
				}
			}
			resp.Entries = append(resp.Entries, subEntries...)
		}
	}

	sort.SliceStable(resp.Entries, func(i, j int) bool {
		return resp.Entries[i].TS.Before(resp.Entries[j].TS)
	})
	writeJSON(w, resp)
}

func loadEvents(path string) ([]timelineEntry, map[string]string, error) {
	if err := ensureUnderRoot(path); err != nil {
		return nil, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	parents := map[string]string{} // child_session_id → parent_session_id
	var out []timelineEntry
	dec := json.NewDecoder(f)
	for dec.More() {
		var ev lifecycle.Event
		if err := dec.Decode(&ev); err != nil {
			break
		}
		entry := timelineEntry{
			TS:        ev.Timestamp,
			Kind:      string(ev.Kind),
			SessionID: ev.SessionID,
			Lane:      laneDaemon,
			Title:     string(ev.Kind),
			Payload:   ev,
		}
		switch ev.Kind {
		case lifecycle.KindStateTransition:
			if ev.PrevState == "" {
				entry.Title = "→ " + ev.NewState
			} else {
				entry.Title = ev.PrevState + " → " + ev.NewState
			}
		case lifecycle.KindHookReceived:
			entry.Lane = laneHook
			entry.Title = "hook: " + ev.HookName
		case lifecycle.KindParentLinked:
			entry.Lane = laneSubagent
			entry.ParentID = ev.ParentSessionID
			entry.Title = "subagent linked"
			if ev.SessionID != "" && ev.ParentSessionID != "" {
				parents[ev.SessionID] = ev.ParentSessionID
			}
		}
		out = append(out, entry)
	}
	return out, parents, nil
}

func loadTranscript(path string, parser tailer.TranscriptParser, lane, sessionID string) ([]timelineEntry, error) {
	if err := ensureUnderRoot(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []timelineEntry
	dec := json.NewDecoder(f)
	for dec.More() {
		var raw map[string]any
		if err := dec.Decode(&raw); err != nil {
			break
		}
		ev := parser.ParseLine(raw)
		if ev == nil || ev.Skip {
			// Surface queue-operation enqueue as a Driver-lane prompt event.
			if t, _ := raw["type"].(string); t == "queue-operation" && raw["operation"] == "enqueue" {
				out = append(out, timelineEntry{
					TS:        parseTime(raw["timestamp"]),
					Lane:      laneDriver,
					Kind:      "prompt_send",
					SessionID: sessionID,
					Title:     truncate(stringField(raw, "content"), 80),
					Payload:   raw,
				})
			}
			continue
		}
		entry := timelineEntry{
			TS:        ev.Timestamp,
			Lane:      lane,
			Kind:      ev.EventType,
			SessionID: sessionID,
			Payload:   raw,
		}
		switch {
		case len(ev.ToolUses) > 0:
			names := make([]string, len(ev.ToolUses))
			for i, t := range ev.ToolUses {
				names[i] = t.Name
			}
			entry.Kind = "tool_call"
			entry.Title = "tool: " + strings.Join(names, ", ")
		case len(ev.ToolResultIDs) > 0:
			entry.Lane = laneToolResult
			entry.Kind = "tool_result"
			if ev.IsError {
				entry.Title = "tool result (error)"
			} else {
				entry.Title = "tool result"
			}
		case ev.EventType == "user_message":
			entry.Lane = laneDriver
			entry.Title = "user message"
		case ev.EventType == "assistant_message", ev.EventType == "assistant", ev.EventType == "turn_done":
			entry.Title = describeAssistant(raw, ev)
		default:
			entry.Title = ev.EventType
		}
		out = append(out, entry)
	}
	return out, nil
}

func describeAssistant(raw map[string]any, ev *tailer.ParsedEvent) string {
	text := tailer.ExtractAssistantText(raw)
	if text != "" {
		return truncate(text, 80)
	}
	if ev.EventType != "" {
		return ev.EventType
	}
	return "assistant"
}

// primarySessionID picks the agent session ID from the lifecycle stream — the
// first transcript_new whose session_id isn't a "proc-<pid>" pre-session
// sentinel emitted by the daemon's process scanner.
func primarySessionID(entries []timelineEntry) string {
	for _, e := range entries {
		if e.Kind == string(lifecycle.KindTranscriptNew) && e.SessionID != "" && !strings.HasPrefix(e.SessionID, "proc-") {
			return e.SessionID
		}
	}
	return ""
}

// ---------- adapters / parsers ----------

func newParserFor(adapter string) tailer.TranscriptParser {
	switch adapter {
	case "claudecode":
		return &claudecode.Parser{}
	case "codex":
		return &codex.Parser{}
	case "pi":
		return &pi.Parser{}
	case "aider":
		return &aider.Parser{}
	default:
		return nil
	}
}

func discoverAdapters() ([]string, error) {
	dir := filepath.Join(*rootDir, replayAgentDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "capabilities.json")); err != nil {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// ---------- file loaders ----------

func loadFeatures() ([]featureMeta, []categoryMeta, error) {
	b, err := os.ReadFile(filepath.Join(*rootDir, featuresJSON))
	if err != nil {
		return nil, nil, err
	}
	var doc struct {
		Features   []featureMeta  `json:"features"`
		Categories []categoryMeta `json:"categories"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, nil, err
	}
	return doc.Features, doc.Categories, nil
}

type byAdapterEntry struct {
	Prompt         string `json:"prompt"`
	Settings       any    `json:"settings"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type scenario struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Requires    []string                  `json:"requires"`
	Verify      map[string]any            `json:"verify"`
	ByAdapter   map[string]byAdapterEntry `json:"by_adapter"`
}

func loadScenarios() ([]scenario, error) {
	b, err := os.ReadFile(filepath.Join(*rootDir, scenariosJSON))
	if err != nil {
		return nil, err
	}
	var doc struct {
		Scenarios []scenario `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return doc.Scenarios, nil
}

// loadCapabilities returns the adapter's feature map and its curated
// transcript extension (defaulting to "jsonl" when the field is absent).
func loadCapabilities(adapter string) (map[string]any, string, error) {
	path, err := safeJoin(*rootDir, replayAgentDir, adapter, "capabilities.json")
	if err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var doc struct {
		Features            map[string]any `json:"features"`
		TranscriptExtension string         `json:"transcript_extension"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, "", err
	}
	ext := doc.TranscriptExtension
	if ext == "" {
		ext = "jsonl"
	}
	return doc.Features, ext, nil
}

// ---------- helpers ----------

var (
	headSHAOnce  sync.Once
	cachedHeadSHA string
)

// headSHA returns the repo HEAD SHA, cached once at first call. The viewer
// doesn't watch for new commits, so per-request `git rev-parse` would just
// fork a process to return the same answer.
func headSHA() string {
	headSHAOnce.Do(func() {
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = *rootDir
		out, err := cmd.Output()
		if err != nil {
			cachedHeadSHA = "main"
			return
		}
		cachedHeadSHA = strings.TrimSpace(string(out))
	})
	return cachedHeadSHA
}

// splitAdapterScenario parses /<prefix>/<adapter>/<scenario> and rejects any
// segment that isn't a plain lowercase identifier — guards against path
// escape (../, absolute paths, encoded slashes, etc.).
func splitAdapterScenario(urlPath, prefix string) (string, string, bool) {
	rest := strings.TrimPrefix(urlPath, prefix)
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	if !safeSegment.MatchString(parts[0]) || !safeSegment.MatchString(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func parseTime(v any) time.Time {
	s, _ := v.(string)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		log.Printf("encode: %v", err)
	}
}

func httpError(w http.ResponseWriter, err error) {
	log.Printf("error: %v", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func stringField(raw map[string]any, key string) string {
	v, _ := raw[key].(string)
	return v
}

