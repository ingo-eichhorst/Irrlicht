package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"irrlicht/tools/onboarding-factory/internal/shard"
	"irrlicht/tools/onboarding-factory/internal/validate"
)

const (
	// repoRootFlagName is the shared --repo-root flag name used across the
	// of subcommands.
	repoRootFlagName = "repo-root"
	// repoRootFlagUsage is the shared --repo-root flag usage string.
	repoRootFlagUsage = "repository root"
)

// flagPassed reports whether name was explicitly set on the command line (so an
// update can tell "--description ”" (clear it) from "not passed" (leave it)).
func flagPassed(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// writeCatalog is the writable shape of replaydata/agents/scenarios.json. Meta
// is kept as a raw blob so it round-trips byte-for-byte (we never touch
// min_versions/transcript_extensions from a scenario write).
type writeCatalog struct {
	Meta      json.RawMessage `json:"meta"`
	Scenarios []shard.Shard   `json:"scenarios"`
}

// writeBytesAtomic replaces path with b via a temp file + rename, so a crashed
// write never leaves a half file. Parent dirs are created as needed.
func writeBytesAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // don't leave a stray .tmp for directory scans to trip on
		return err
	}
	return nil
}

// writeJSONFileAtomic marshals v (2-space indent) and replaces path atomically.
// HTML escaping is disabled so `<`, `>`, `&` stay literal — these are data files
// (assessment markdown bodies are full of them), never served as HTML, and
// literal is both readable and the format the committed corpus already uses.
func writeJSONFileAtomic(path string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil { // Encode appends a trailing newline
		return err
	}
	return writeBytesAtomic(path, buf.Bytes())
}

// resolveCellFolder returns the on-disk folder for one (agent, scenario) cell:
// the override when set, else the agent's existing folder for the scenario
// (preferring a variant folder where its recordings already live), else the
// canonical <dashed-id>_<name> for a brand-new cell. Routing write + spec
// through the same resolver keeps a cell's metadata.json and expected.jsonl in
// the SAME folder as its recordings.
func resolveCellFolder(repoRoot, agent string, sh shard.Shard, override string) string {
	if override != "" {
		return override
	}
	return shard.AgentFolderForScenario(repoRoot, agent, sh.Name)
}

func loadWriteCatalog(repoRoot string) (*writeCatalog, error) {
	b, err := os.ReadFile(shard.File(repoRoot))
	if err != nil {
		return nil, err
	}
	var c writeCatalog
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("catalog is not valid JSON: %w", err)
	}
	return &c, nil
}

func (c *writeCatalog) sortByID() {
	sort.SliceStable(c.Scenarios, func(i, j int) bool {
		ai, ax, aok := shard.SplitID(c.Scenarios[i].ID)
		bi, bx, bok := shard.SplitID(c.Scenarios[j].ID)
		if !aok || !bok {
			return c.Scenarios[i].ID < c.Scenarios[j].ID
		}
		if ai != bi {
			return ai < bi
		}
		return ax < bx
	})
}

// readFileArg returns the trimmed contents of path, or "" when path is empty.
func readFileArg(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// --- of scenario add|update|show ---

// runScenarioShow prints one scenario's full spec (the five fields). It is the
// read the skill's assess / create-* verbs use to fetch description + process +
// acceptance_criteria — the coverage/status views carry only ids and state, and
// the skill must NOT read replaydata/agents/scenarios.json directly.
func runScenarioShow(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("of scenario show")
	var (
		name     = fs.String("name", "", "scenario name (kebab slug)")
		asJSON   = fs.Bool("json", false, "emit JSON")
		repoRoot = fs.String(repoRootFlagName, ".", repoRootFlagUsage)
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *name == "" {
		fmt.Fprintln(stderr, "of scenario show: --name is required")
		return exitUsage
	}
	sh, ok := shard.Load(*repoRoot, *name)
	if !ok {
		fmt.Fprintf(stderr, "of scenario show: %q not in the catalog\n", *name)
		return exitFail
	}
	if *asJSON {
		if err := writeJSON(stdout, sh); err != nil {
			fmt.Fprintf(stderr, "of scenario show: encode: %v\n", err)
			return exitUsage
		}
		return exitOK
	}
	fmt.Fprintf(stdout, "id: %s\nname: %s\ndescription: %s\n\n## process\n%s\n\n## acceptance_criteria\n%s\n",
		sh.ID, sh.Name, sh.Description, sh.Process, sh.AcceptanceCriteria)
	return exitOK
}

func runScenario(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: of scenario add|update|show ...")
		return exitUsage
	}
	verb := args[0]
	if verb == "show" {
		return runScenarioShow(args[1:], stdout, stderr)
	}
	fs := newFlagSet("of scenario " + verb)
	var (
		id       = fs.String("id", "", "scenario id <section>.<index> (add only)")
		name     = fs.String("name", "", "scenario name (kebab slug)")
		desc     = fs.String("description", "", "one-line description")
		procF    = fs.String("process-file", "", "markdown file for the process block")
		accF     = fs.String("acceptance-file", "", "markdown file for the acceptance_criteria block")
		repoRoot = fs.String(repoRootFlagName, ".", repoRootFlagUsage)
	)
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	if *name == "" {
		fmt.Fprintln(stderr, "of scenario: --name is required")
		return exitUsage
	}
	process, err := readFileArg(*procF)
	if err != nil {
		fmt.Fprintf(stderr, "of scenario: %v\n", err)
		return exitUsage
	}
	acceptance, err := readFileArg(*accF)
	if err != nil {
		fmt.Fprintf(stderr, "of scenario: %v\n", err)
		return exitUsage
	}

	cat, err := loadWriteCatalog(*repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "of scenario: %v\n", err)
		return exitUsage
	}

	idx := findScenarioIndex(cat, *name)
	var rc int
	switch verb {
	case "add":
		rc = applyScenarioAdd(cat, idx, *id, *name, *desc, process, acceptance, stderr)
	case "update":
		rc = applyScenarioUpdate(cat, idx, fs, *desc, *procF, *accF, process, acceptance, *name, stderr)
	default:
		fmt.Fprintln(stderr, "of scenario: verb must be add, update, or show")
		return exitUsage
	}
	if rc != exitOK {
		return rc
	}

	cat.sortByID()
	if err := writeJSONFileAtomic(shard.File(*repoRoot), cat); err != nil {
		fmt.Fprintf(stderr, "of scenario: write: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "of scenario %s: %s ok\n", verb, *name)
	return exitOK
}

// findScenarioIndex returns the index of the scenario named name in
// cat.Scenarios, or -1 if it isn't present.
func findScenarioIndex(cat *writeCatalog, name string) int {
	for i := range cat.Scenarios {
		if cat.Scenarios[i].Name == name {
			return i
		}
	}
	return -1
}

// applyScenarioAdd appends a new scenario to cat after checking it doesn't
// already exist (idx < 0) and that its id/name are well-formed and unique.
func applyScenarioAdd(cat *writeCatalog, idx int, id, name, desc, process, acceptance string, stderr io.Writer) int {
	if idx >= 0 {
		fmt.Fprintf(stderr, "of scenario add: %q already exists (use update)\n", name)
		return exitFail
	}
	if id == "" {
		fmt.Fprintln(stderr, "of scenario add: --id is required")
		return exitUsage
	}
	if !idRe.MatchString(id) {
		fmt.Fprintf(stderr, "of scenario add: id %q is not <section>.<index>\n", id)
		return exitFail
	}
	if !nameRe.MatchString(name) {
		fmt.Fprintf(stderr, "of scenario add: name %q is not a kebab slug\n", name)
		return exitFail
	}
	for _, s := range cat.Scenarios {
		if s.ID == id {
			fmt.Fprintf(stderr, "of scenario add: id %q already in use by %q\n", id, s.Name)
			return exitFail
		}
	}
	cat.Scenarios = append(cat.Scenarios, shard.Shard{
		ID: id, Name: name, Description: desc,
		Process: process, AcceptanceCriteria: acceptance,
	})
	return exitOK
}

// applyScenarioUpdate patches the existing scenario at idx in place:
// description only when --description was explicitly passed (so an empty
// value can clear it), process/acceptance_criteria only when their file
// flags were given.
func applyScenarioUpdate(cat *writeCatalog, idx int, fs *flag.FlagSet, desc, procF, accF, process, acceptance, name string, stderr io.Writer) int {
	if idx < 0 {
		fmt.Fprintf(stderr, "of scenario update: %q not found (use add)\n", name)
		return exitFail
	}
	s := &cat.Scenarios[idx]
	if flagPassed(fs, "description") {
		s.Description = desc
	}
	if procF != "" {
		s.Process = process
	}
	if accF != "" {
		s.AcceptanceCriteria = acceptance
	}
	return exitOK
}

// --- of agent add ---

// agentMeta is replaydata/agents/<id>/metadata.json: the column descriptor.
type agentMeta struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Provider      string   `json:"provider"`
	Prerequisites []string `json:"prerequisites,omitempty"`
}

type prereqFlag []string

func (p *prereqFlag) String() string     { return strings.Join(*p, ",") }
func (p *prereqFlag) Set(v string) error { *p = append(*p, v); return nil }

func runAgent(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "add" {
		fmt.Fprintln(stderr, "usage: of agent add --id --name --provider [--min-version v] [--prereq p]...")
		return exitUsage
	}
	fs := newFlagSet("of agent add")
	var prereqs prereqFlag
	var (
		id       = fs.String("id", "", "agent id (kebab slug)")
		name     = fs.String("name", "", "display name")
		provider = fs.String("provider", "", "provider (e.g. anthropic, openai)")
		minVer   = fs.String("min-version", "0.0.0", "minimum supported agent version (column registration)")
		repoRoot = fs.String(repoRootFlagName, ".", repoRootFlagUsage)
	)
	fs.Var(&prereqs, "prereq", "a recording prerequisite (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	if *id == "" || *name == "" || *provider == "" {
		fmt.Fprintln(stderr, "of agent add: --id, --name, --provider are all required")
		return exitUsage
	}
	if !nameRe.MatchString(*id) {
		fmt.Fprintf(stderr, "of agent add: id %q is not a kebab slug\n", *id)
		return exitFail
	}
	metaPath := filepath.Join(*repoRoot, "replaydata", "agents", *id, "metadata.json")
	if fileExists(metaPath) {
		fmt.Fprintf(stderr, "of agent add: agent %q already exists\n", *id)
		return exitFail
	}
	// Register the column in scenarios.json meta.min_versions so the viewer
	// shows it and the matrix treats it as onboarded.
	if rc := registerAgentColumn(*repoRoot, *id, *minVer, stderr); rc != exitOK {
		return rc
	}
	am := agentMeta{ID: *id, Name: *name, Provider: *provider, Prerequisites: prereqs}
	if err := writeJSONFileAtomic(metaPath, am); err != nil {
		fmt.Fprintf(stderr, "of agent add: write: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "of agent add: %s ok (provider=%s, prereqs=%d)\n", *id, *provider, len(prereqs))
	return exitOK
}

// registerAgentColumn adds id→minVer to scenarios.json meta.min_versions,
// preserving the rest of meta (transcript_extensions).
func registerAgentColumn(repoRoot, id, minVer string, stderr io.Writer) int {
	cat, err := loadWriteCatalog(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "of agent add: %v\n", err)
		return exitUsage
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(cat.Meta, &meta); err != nil {
		fmt.Fprintf(stderr, "of agent add: meta is not a JSON object: %v\n", err)
		return exitFail
	}
	mv := map[string]string{}
	if raw, ok := meta["min_versions"]; ok {
		_ = json.Unmarshal(raw, &mv)
	}
	mv[id] = minVer
	b, _ := json.Marshal(mv)
	meta["min_versions"] = b
	mb, _ := json.Marshal(meta)
	cat.Meta = mb
	if err := writeJSONFileAtomic(shard.File(repoRoot), cat); err != nil {
		fmt.Fprintf(stderr, "of agent add: write catalog: %v\n", err)
		return exitUsage
	}
	return exitOK
}

// --- of cell write|spec ---

const cellUsage = `usage: of cell write --agent a --scenario s --file metadata.json [--folder f]
       of cell spec  --agent a --scenario s --file expected.jsonl [--folder f]`

func runCell(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, cellUsage)
		return exitUsage
	}
	switch args[0] {
	case "write":
		return runCellWrite(args[1:], stdout, stderr)
	case "spec":
		return runCellSpec(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, cellUsage)
		return exitUsage
	}
}

func runCellWrite(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("of cell write")
	var (
		agent    = fs.String("agent", "", "agent id")
		scenario = fs.String("scenario", "", "scenario name (the FK)")
		file     = fs.String("file", "", "metadata.json content to write")
		folder   = fs.String("folder", "", "override on-disk folder (default: <dashed-id>_<name>)")
		repoRoot = fs.String(repoRootFlagName, ".", repoRootFlagUsage)
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *agent == "" || *scenario == "" || *file == "" {
		fmt.Fprintln(stderr, "of cell write: --agent, --scenario, --file are required")
		return exitUsage
	}
	sh, ok := shard.Load(*repoRoot, *scenario)
	if !ok {
		fmt.Fprintf(stderr, "of cell write: scenario %q not in the catalog\n", *scenario)
		return exitFail
	}
	b, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(stderr, "of cell write: %v\n", err)
		return exitUsage
	}
	var cell shard.ShardAgent
	if err := json.Unmarshal(b, &cell); err != nil {
		fmt.Fprintf(stderr, "of cell write: --file is not valid metadata.json: %v\n", err)
		return exitFail
	}
	// Force the FK so the cell always links back to its catalog row.
	cell.ScenarioID = *scenario
	// details.assessment is the verdict's source of truth (the matrix reads it
	// for routing). Mirror its three pillars + confidence into the metadata
	// overview tier so the two tiers can't drift — the author only has to get
	// details.assessment right.
	mirrorAssessmentPillars(&cell)
	// Default the driver-consumed recipe fields a script recipe omits
	// (timeout_seconds, settings) so a malformed recipe never reaches a driver.
	defaultRecipeFields(&cell)
	fold := resolveCellFolder(*repoRoot, *agent, sh, *folder)
	metaPath := filepath.Join(*repoRoot, "replaydata", "agents", *agent, "scenarios", fold, "metadata.json")
	if err := writeJSONFileAtomic(metaPath, cell); err != nil {
		fmt.Fprintf(stderr, "of cell write: write: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "of cell write: %s/%s ok\n", *agent, fold)
	return exitOK
}

// mirrorAssessmentPillars copies the three pillars + confidence from
// details.assessment (the verdict of record, which the matrix reads for
// disposition/route) into the metadata overview tier (which the viewer and the
// matrix's DisplayState fallback read). Keeping one authored source prevents the
// two tiers from telling different stories. No-op when details.assessment is
// absent or carries no pillar keys.
func mirrorAssessmentPillars(cell *shard.ShardAgent) {
	if len(cell.Details.Assessment) == 0 {
		return
	}
	var a struct {
		AgentSupports    string  `json:"agent_supports"`
		DaemonCapability string  `json:"daemon_capability"`
		DriverCapability string  `json:"driver_capability"`
		Confidence       float64 `json:"confidence"`
	}
	if json.Unmarshal(cell.Details.Assessment, &a) != nil {
		return
	}
	if a.AgentSupports != "" {
		cell.Metadata.AgentSupports = a.AgentSupports
	}
	if a.DaemonCapability != "" {
		cell.Metadata.DaemonCapability = a.DaemonCapability
	}
	if a.DriverCapability != "" {
		cell.Metadata.DriverCapability = a.DriverCapability
	}
	if a.Confidence != 0 {
		cell.Metadata.Confidence = a.Confidence
	}
}

// runCellSpec writes a cell's expected.jsonl (the spec) through the factory so
// the skill never edits replaydata directly. It validates well-formed JSONL and
// forces the meta line's scenario_id to the FK; phase lines are kept verbatim.
func runCellSpec(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("of cell spec")
	var (
		agent    = fs.String("agent", "", "agent id")
		scenario = fs.String("scenario", "", "scenario name (the FK)")
		file     = fs.String("file", "", "expected.jsonl content to write")
		folder   = fs.String("folder", "", "override on-disk folder (default: <dashed-id>_<name>)")
		repoRoot = fs.String(repoRootFlagName, ".", repoRootFlagUsage)
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *agent == "" || *scenario == "" || *file == "" {
		fmt.Fprintln(stderr, "of cell spec: --agent, --scenario, --file are required")
		return exitUsage
	}
	sh, ok := shard.Load(*repoRoot, *scenario)
	if !ok {
		fmt.Fprintf(stderr, "of cell spec: scenario %q not in the catalog\n", *scenario)
		return exitFail
	}
	b, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(stderr, "of cell spec: %v\n", err)
		return exitUsage
	}
	out, err := normalizeExpectedJSONL(b, *scenario)
	if err != nil {
		fmt.Fprintf(stderr, "of cell spec: %v\n", err)
		return exitFail
	}
	fold := resolveCellFolder(*repoRoot, *agent, sh, *folder)
	specPath := filepath.Join(*repoRoot, "replaydata", "agents", *agent, "scenarios", fold, "expected.jsonl")
	if err := writeBytesAtomic(specPath, out); err != nil {
		fmt.Fprintf(stderr, "of cell spec: write: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "of cell spec: %s/%s ok\n", *agent, fold)
	return exitOK
}

// normalizeExpectedJSONL validates that b is well-formed JSONL (one JSON object
// per non-empty line) and forces the meta line (the first non-empty line) to
// carry scenario_id=scenarioID + a schema_version (default 1). Phase lines are
// emitted byte-for-byte (modulo CRLF normalization) so a re-written spec doesn't
// churn their key order. Phases are validated on the same terms as the reader
// (validate.ParseShardSpec) so a structurally-broken phase is rejected here, not
// silently written and only caught later at record/verify time.
func normalizeExpectedJSONL(b []byte, scenarioID string) ([]byte, error) {
	var out []string
	var metaLine json.RawMessage
	var phaseLines []json.RawMessage
	for i, raw := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		ln := strings.TrimRight(raw, "\r") // normalize CRLF → LF so endings stay uniform
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(ln), &obj); err != nil {
			return nil, fmt.Errorf("line %d is not a JSON object: %w", i+1, err)
		}
		if obj == nil { // a bare `null` line unmarshals to a nil map with no error
			return nil, fmt.Errorf("line %d is JSON null, not an object", i+1)
		}
		if metaLine != nil {
			out = append(out, ln) // phase line — verbatim
			phaseLines = append(phaseLines, json.RawMessage(ln))
			continue
		}
		enc, err := buildMetaLine(obj, scenarioID)
		if err != nil {
			return nil, err
		}
		metaLine = enc
		out = append(out, string(enc))
	}
	if metaLine == nil {
		return nil, fmt.Errorf("expected.jsonl has no meta line (empty or whitespace-only file)")
	}
	if _, _, _, err := validate.ParseShardSpec(metaLine, phaseLines); err != nil {
		return nil, err
	}
	return []byte(strings.Join(out, "\n") + "\n"), nil
}

// buildMetaLine forces the FK + a default schema_version onto the
// expected.jsonl meta line (the first non-empty line) and re-emits it WITHOUT
// HTML-escaping so it matches the file's literal style (the rest of
// replaydata is not >-escaped); otherwise a re-write would churn every
// < > & in source/notes into escapes.
func buildMetaLine(obj map[string]json.RawMessage, scenarioID string) (json.RawMessage, error) {
	obj["scenario_id"], _ = json.Marshal(scenarioID)
	if _, ok := obj["schema_version"]; !ok {
		obj["schema_version"] = json.RawMessage("1")
	}
	return marshalNoEscape(obj)
}

// marshalNoEscape encodes v as compact JSON without Go's default HTML escaping
// of <, >, & — matching the literal (non-\u-escaped) style of replaydata files.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil // Encode appends a trailing newline
}
