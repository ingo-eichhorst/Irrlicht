package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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
	edit := scenarioEdit{
		ID: *id, Name: *name, Description: *desc,
		DescriptionSet: flagPassed(fs, "description"),
		ProcessFile:    *procF, AcceptanceFile: *accF,
		Process: process, Acceptance: acceptance,
	}
	var rc int
	switch verb {
	case "add":
		rc = applyScenarioAdd(cat, idx, edit, stderr)
	case "update":
		rc = applyScenarioUpdate(cat, idx, edit, stderr)
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

// scenarioEdit carries the field values read from `of scenario add`/`update`
// flags, keeping applyScenarioAdd/applyScenarioUpdate's parameter lists small
// (go:S107) instead of threading each field through individually.
// DescriptionSet distinguishes "--description ”" (clear it) from "not
// passed" (leave it) — computed once at the flag.FlagSet the caller already
// holds, so applyScenarioUpdate itself doesn't need a *flag.FlagSet param.
type scenarioEdit struct {
	ID             string
	Name           string
	Description    string
	DescriptionSet bool
	ProcessFile    string
	AcceptanceFile string
	Process        string
	Acceptance     string
}

// applyScenarioAdd appends a new scenario to cat after checking it doesn't
// already exist (idx < 0) and that its id/name are well-formed and unique.
func applyScenarioAdd(cat *writeCatalog, idx int, edit scenarioEdit, stderr io.Writer) int {
	if idx >= 0 {
		fmt.Fprintf(stderr, "of scenario add: %q already exists (use update)\n", edit.Name)
		return exitFail
	}
	if edit.ID == "" {
		fmt.Fprintln(stderr, "of scenario add: --id is required")
		return exitUsage
	}
	if !idRe.MatchString(edit.ID) {
		fmt.Fprintf(stderr, "of scenario add: id %q is not <section>.<index>\n", edit.ID)
		return exitFail
	}
	if !nameRe.MatchString(edit.Name) {
		fmt.Fprintf(stderr, "of scenario add: name %q is not a kebab slug\n", edit.Name)
		return exitFail
	}
	for _, s := range cat.Scenarios {
		if s.ID == edit.ID {
			fmt.Fprintf(stderr, "of scenario add: id %q already in use by %q\n", edit.ID, s.Name)
			return exitFail
		}
	}
	cat.Scenarios = append(cat.Scenarios, shard.Shard{
		ID: edit.ID, Name: edit.Name, Description: edit.Description,
		Process: edit.Process, AcceptanceCriteria: edit.Acceptance,
	})
	return exitOK
}

// applyScenarioUpdate patches the existing scenario at idx in place:
// description only when --description was explicitly passed (so an empty
// value can clear it), process/acceptance_criteria only when their file
// flags were given.
func applyScenarioUpdate(cat *writeCatalog, idx int, edit scenarioEdit, stderr io.Writer) int {
	if idx < 0 {
		fmt.Fprintf(stderr, "of scenario update: %q not found (use add)\n", edit.Name)
		return exitFail
	}
	s := &cat.Scenarios[idx]
	if edit.DescriptionSet {
		s.Description = edit.Description
	}
	if edit.ProcessFile != "" {
		s.Process = edit.Process
	}
	if edit.AcceptanceFile != "" {
		s.AcceptanceCriteria = edit.Acceptance
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

const agentUsage = `usage: of agent add    --id i --name n --provider p [--min-version v] [--prereq p]...
       of agent update --id i [--name n] [--provider p] [--min-version v] [--prereq p]... [--add-prereq p]...
                       [--maturity planned|alpha|beta|stable] [--capability trait=absent|untraced|traced]...`

func runAgent(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, agentUsage)
		return exitUsage
	}
	switch args[0] {
	case "add":
		return runAgentAdd(args, stdout, stderr)
	case "update":
		return runAgentUpdate(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, agentUsage)
		return exitUsage
	}
}

func runAgentAdd(args []string, stdout, stderr io.Writer) int {
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
	// Give the new column an entry in the capability model too. Without it
	// `of validate` fails the tree the moment the column is registered, and
	// the only remedy would be the hand-edit this CLI exists to avoid (#1369).
	if err := ensureAdapterModel(*repoRoot, *id); err != nil {
		fmt.Fprintf(stderr, "of agent add: capability model: %v\n", err)
		return exitUsage
	}
	am := agentMeta{ID: *id, Name: *name, Provider: *provider, Prerequisites: prereqs}
	if err := writeJSONFileAtomic(metaPath, am); err != nil {
		fmt.Fprintf(stderr, "of agent add: write: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "of agent add: %s ok (provider=%s, prereqs=%d)\n", *id, *provider, len(prereqs))
	return exitOK
}

// runAgentUpdate edits an EXISTING column's metadata.json. It exists because
// `add` is deliberately one-shot (it refuses a column that already exists), and
// a column's recording prerequisites are not a one-shot fact: a `record` sweep
// discovers them — which tools the live CLI actually exposes, which auth mode
// works — and every later cell in that column should inherit the finding
// instead of paying to rediscover it. Without this verb the only ways to record
// one were to hand-edit replaydata/ (the skill's headline anti-pattern) or to
// delete metadata.json and re-add, which drags scenarios.json through a full
// catalog round-trip for a change that never touches it.
//
// Only explicitly-passed fields are written, so an update naming just
// --add-prereq cannot silently reset a name or provider. --prereq REPLACES the
// whole list; --add-prereq APPENDS (skipping exact duplicates, so re-running a
// promotion is idempotent). scenarios.json is touched only when --min-version
// is passed, for the same reason.
func runAgentUpdate(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("of agent update")
	var prereqs, addPrereqs prereqFlag
	caps := capabilityFlag{}
	fs.Var(caps, "capability", "trait=state (absent|untraced|traced); traced removes the declaration")
	var (
		id       = fs.String("id", "", "agent id (kebab slug)")
		name     = fs.String("name", "", "display name")
		provider = fs.String("provider", "", "provider (e.g. anthropic, openai)")
		minVer   = fs.String("min-version", "", "minimum supported agent version (rewrites the column registration)")
		maturity = fs.String("maturity", "", "claimed maturity: planned|alpha|beta|stable (#1369)")
		repoRoot = fs.String(repoRootFlagName, ".", repoRootFlagUsage)
	)
	fs.Var(&prereqs, "prereq", "replace the recording prerequisites with these (repeatable)")
	fs.Var(&addPrereqs, "add-prereq", "append a recording prerequisite, skipping exact duplicates (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *id == "" {
		fmt.Fprintln(stderr, "of agent update: --id is required")
		return exitUsage
	}
	// An update that changes nothing must not report success. With no mutating
	// flag this printed `of agent update: <id> ok (...)` and returned 0 having
	// written the file back byte-identically — so a caller that typo'd a flag
	// name, or a script whose variable came through empty, got a green "ok"
	// for a no-op. That is the same failure this function's own comment below
	// calls "the worse outcome" for --provider, arriving through the front
	// door instead. --repo-root and --id are not mutations; every other flag
	// is.
	if !agentUpdateHasMutation(fs, addPrereqs, caps) {
		fmt.Fprintf(stderr, "of agent update: %s — nothing to do; pass at least one of "+
			"--name, --provider, --prereq, --add-prereq, --min-version, --maturity, --capability\n", *id)
		return exitUsage
	}
	// TWO FILES, TWO REGISTRIES — and this verb writes to both (#1803).
	//
	//	replaydata/agents/<id>/metadata.json  descriptive: name, provider, prereqs
	//	replaydata/agents/adapters.json       the capability model: maturity, traits
	//
	// It used to require the FIRST unconditionally, including for an update
	// that only touches the second. Five of the eleven onboarded columns have
	// no metadata.json at all — they predate `of agent add` — so declaring a
	// capability for aider, claudecode, codex, opencode or pi was impossible
	// through the CLI, and the only remedy was the hand-edit of replaydata/
	// this tool exists to prevent. Reproduce the five with:
	//
	//	for a in $(jq -r '.meta.min_versions|keys[]' replaydata/agents/scenarios.json); do
	//	  [ -f "replaydata/agents/$a/metadata.json" ] || echo "$a"; done
	//
	// So the requirement is now per-flag. The authoritative answer to "is this
	// a real column" is scenarios.json's meta.min_versions, which is what
	// `of validate` checks the capability model against; metadata.json is
	// optional descriptive data and is required only by the flags that write
	// into it. Refusing those rather than silently dropping them is the point:
	// a --provider that reported ok and changed nothing is the worse outcome.
	descriptive := flagPassed(fs, "name") || flagPassed(fs, "provider") ||
		flagPassed(fs, "prereq") || len(addPrereqs) > 0
	metaPath := agentMetaPath(*repoRoot, *id)
	am, haveMeta, rc := loadAgentMetaForUpdate(*repoRoot, *id, descriptive, stderr)
	if rc != exitOK {
		return rc
	}
	if flagPassed(fs, "name") {
		am.Name = *name
	}
	if flagPassed(fs, "provider") {
		am.Provider = *provider
	}
	if flagPassed(fs, "prereq") {
		am.Prerequisites = prereqs
	}
	for _, p := range addPrereqs {
		if !slices.Contains(am.Prerequisites, p) {
			am.Prerequisites = append(am.Prerequisites, p)
		}
	}
	if flagPassed(fs, "min-version") {
		if rc := registerAgentColumn(*repoRoot, *id, *minVer, stderr); rc != exitOK {
			return rc
		}
	}
	if flagPassed(fs, "maturity") || len(caps) > 0 {
		if err := setAdapterModel(*repoRoot, *id, *maturity, caps); err != nil {
			fmt.Fprintf(stderr, "of agent update: capability model: %v\n", err)
			return exitUsage
		}
	}
	// Only write the descriptive file when there is descriptive data to write.
	// Creating one from an empty struct would stamp a column with an empty
	// name and provider, which `of validate` and the viewer both read as a
	// real declaration — a fabricated fact is worse than a missing one.
	if haveMeta || descriptive {
		am.ID = *id // the path is authoritative; a mismatched id in the file is a bug
		if err := writeJSONFileAtomic(metaPath, am); err != nil {
			fmt.Fprintf(stderr, "of agent update: write: %v\n", err)
			return exitUsage
		}
		fmt.Fprintf(stdout, "of agent update: %s ok (provider=%s, prereqs=%d)\n", *id, am.Provider, len(am.Prerequisites))
		return exitOK
	}
	fmt.Fprintf(stdout, "of agent update: %s ok (capability model only; no agent metadata.json)\n", *id)
	return exitOK
}

// agentColumnRegistered reports whether id is an onboarded column, i.e. carries
// a meta.min_versions entry in the catalog. That map is the authoritative
// column registry — `of validate`'s capability gate checks the model's adapter
// set against it in both directions (validate_maturity.go's "adapter %q is not
// an onboarded column" / "has no entry") — so it, not the optional per-agent
// metadata.json, is what a capability-only update must be gated on.
func agentColumnRegistered(repoRoot, id string) bool {
	cat, err := loadWriteCatalog(repoRoot)
	if err != nil {
		return false
	}
	var meta struct {
		MinVersions map[string]string `json:"min_versions"`
	}
	if err := json.Unmarshal(cat.Meta, &meta); err != nil {
		return false
	}
	_, ok := meta.MinVersions[id]
	return ok
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

// agentUpdateHasMutation reports whether `of agent update` was given at least
// one flag that actually changes something. Split out of runAgentUpdate rather
// than inlined: that function already carries the two-registry branch, and both
// CodeScene and Sonar (go:S3776, cognitive complexity 24 > 15) flagged it once
// this arm was added.
//
// `caps` and `addPrereqs` are checked by LENGTH, not via flagPassed: both are
// flag.Value accumulators, so "passed but empty" is not a state they can be in
// — and flagPassed would report true for a `--capability` that failed to
// parse into an entry.
func agentUpdateHasMutation(fs *flag.FlagSet, addPrereqs prereqFlag, caps capabilityFlag) bool {
	for _, name := range []string{"name", "provider", "prereq", "min-version", "maturity"} {
		if flagPassed(fs, name) {
			return true
		}
	}
	return len(addPrereqs) > 0 || len(caps) > 0
}

// loadAgentMetaForUpdate resolves the per-agent metadata.json for an update,
// applying the per-flag requirement described at the call site: the file is
// mandatory only when a descriptive flag needs somewhere to be written, and a
// capability-only update is gated on the column being registered instead.
//
// Split out of runAgentUpdate purely to keep it readable — Sonar (go:S3776)
// and CodeScene both flagged that function once the two-registry branch landed
// on top of what was already there.
//
// Returns (meta, haveMeta, exitOK) on success; the caller returns the code on
// anything else.
func loadAgentMetaForUpdate(repoRoot, id string, descriptive bool, stderr io.Writer) (agentMeta, bool, int) {
	var am agentMeta
	metaPath := agentMetaPath(repoRoot, id)
	b, readErr := os.ReadFile(metaPath)
	if readErr != nil {
		if descriptive {
			fmt.Fprintf(stderr, "of agent update: no agent %q at %s — --name/--provider/--prereq have nowhere to be written (use `of agent add` for a new column)\n", id, metaPath)
			return am, false, exitFail
		}
		if !agentColumnRegistered(repoRoot, id) {
			fmt.Fprintf(stderr, "of agent update: %q is not an onboarded column (see replaydata/agents/scenarios.json meta.min_versions; use `of agent add` for a new one)\n", id)
			return am, false, exitFail
		}
		return am, false, exitOK
	}
	if err := json.Unmarshal(b, &am); err != nil {
		fmt.Fprintf(stderr, "of agent update: %s is not valid agent metadata: %v\n", metaPath, err)
		return am, false, exitFail
	}
	return am, true, exitOK
}

// agentMetaPath is the per-agent descriptive file. Derived in one place rather
// than passed alongside the id it is built from — two parameters that must
// always agree are one parameter.
func agentMetaPath(repoRoot, id string) string {
	return filepath.Join(repoRoot, "replaydata", "agents", id, "metadata.json")
}
