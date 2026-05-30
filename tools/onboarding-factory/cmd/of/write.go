package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"irrlicht/tools/onboarding-factory/internal/shard"
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
// min_versions/transcript_extensions/capability_vocab from a scenario write).
type writeCatalog struct {
	Meta      json.RawMessage `json:"meta"`
	Scenarios []shard.Shard   `json:"scenarios"`
}

// writeJSONFileAtomic marshals v (2-space indent) and replaces path atomically
// via a temp file + rename, so a crashed write never leaves a half file.
func writeJSONFileAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

// --- of scenario add|update ---

func runScenario(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: of scenario add|update ...")
		return exitUsage
	}
	verb := args[0]
	fs := newFlagSet("of scenario " + verb)
	var (
		id       = fs.String("id", "", "scenario id <section>.<index> (add only)")
		name     = fs.String("name", "", "scenario name (kebab slug)")
		desc     = fs.String("description", "", "one-line description")
		procF    = fs.String("process-file", "", "markdown file for the process block")
		accF     = fs.String("acceptance-file", "", "markdown file for the acceptance_criteria block")
		repoRoot = fs.String("repo-root", ".", "repository root")
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

	idx := -1
	for i := range cat.Scenarios {
		if cat.Scenarios[i].Name == *name {
			idx = i
			break
		}
	}

	switch verb {
	case "add":
		if idx >= 0 {
			fmt.Fprintf(stderr, "of scenario add: %q already exists (use update)\n", *name)
			return exitFail
		}
		if *id == "" {
			fmt.Fprintln(stderr, "of scenario add: --id is required")
			return exitUsage
		}
		if !idRe.MatchString(*id) {
			fmt.Fprintf(stderr, "of scenario add: id %q is not <section>.<index>\n", *id)
			return exitFail
		}
		if !nameRe.MatchString(*name) {
			fmt.Fprintf(stderr, "of scenario add: name %q is not a kebab slug\n", *name)
			return exitFail
		}
		for _, s := range cat.Scenarios {
			if s.ID == *id {
				fmt.Fprintf(stderr, "of scenario add: id %q already in use by %q\n", *id, s.Name)
				return exitFail
			}
		}
		cat.Scenarios = append(cat.Scenarios, shard.Shard{
			ID: *id, Name: *name, Description: *desc,
			Process: process, AcceptanceCriteria: acceptance,
		})
	case "update":
		if idx < 0 {
			fmt.Fprintf(stderr, "of scenario update: %q not found (use add)\n", *name)
			return exitFail
		}
		s := &cat.Scenarios[idx]
		if flagPassed(fs, "description") {
			s.Description = *desc
		}
		if *procF != "" {
			s.Process = process
		}
		if *accF != "" {
			s.AcceptanceCriteria = acceptance
		}
	default:
		fmt.Fprintln(stderr, "of scenario: verb must be add or update")
		return exitUsage
	}

	cat.sortByID()
	if err := writeJSONFileAtomic(shard.File(*repoRoot), cat); err != nil {
		fmt.Fprintf(stderr, "of scenario: write: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "of scenario %s: %s ok\n", verb, *name)
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
		repoRoot = fs.String("repo-root", ".", "repository root")
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
// preserving the rest of meta (transcript_extensions, capability_vocab).
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

// --- of cell write ---

func runCell(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "write" {
		fmt.Fprintln(stderr, "usage: of cell write --agent a --scenario s --file metadata.json [--folder f]")
		return exitUsage
	}
	fs := newFlagSet("of cell write")
	var (
		agent    = fs.String("agent", "", "agent id")
		scenario = fs.String("scenario", "", "scenario name (the FK)")
		file     = fs.String("file", "", "metadata.json content to write")
		folder   = fs.String("folder", "", "override on-disk folder (default: <dashed-id>_<name>)")
		repoRoot = fs.String("repo-root", ".", "repository root")
	)
	if err := fs.Parse(args[1:]); err != nil {
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
	fold := *folder
	if fold == "" {
		fold = strings.ReplaceAll(sh.ID, ".", "-") + "_" + sh.Name
	}
	metaPath := filepath.Join(*repoRoot, "replaydata", "agents", *agent, "scenarios", fold, "metadata.json")
	if err := writeJSONFileAtomic(metaPath, cell); err != nil {
		fmt.Fprintf(stderr, "of cell write: write: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "of cell write: %s/%s ok\n", *agent, fold)
	return exitOK
}
