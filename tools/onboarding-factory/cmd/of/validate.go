package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"irrlicht/tools/onboarding-factory/internal/shard"
	"irrlicht/tools/onboarding-factory/internal/validate"
)

var (
	idRe   = regexp.MustCompile(`^\d+\.\d+$`)
	nameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// allowedScenarioKeys is the exact MECE field set for a catalog scenario. Any
// other key is a schema violation (the factory keeps the structure minimal).
var allowedScenarioKeys = map[string]bool{
	"id": true, "name": true, "description": true,
	"acceptance_criteria": true, "process": true,
}

// finding is one validation violation. path locates it; msg explains it.
type finding struct {
	Path string `json:"path"`
	Msg  string `json:"msg"`
}

const (
	// catalogRelPath is the repo-relative catalog path used in finding
	// messages (the actual read goes through shard.File).
	catalogRelPath = "replaydata/agents/scenarios.json"
	// metadataJSONSuffix is appended to a cell's relative path to name its
	// metadata.json file in finding messages.
	metadataJSONSuffix = "/metadata.json"
)

// runValidate checks the whole replaydata tree for schema + referential
// integrity: the catalog parses and every scenario is the minimal 5-field
// shape with a well-formed unique id/name; every agent cell parses, links to a
// real scenario, and (when recorded) carries an expected.jsonl; no orphan
// recording folders sit under scenarios/. Exit 1 + findings on any violation.
func runValidate(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("of validate")
	var (
		asJSON   = fs.Bool("json", false, "emit findings as JSON")
		repoRoot = fs.String("repo-root", ".", "repository root")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	var findings []finding
	add := func(path, msg string) { findings = append(findings, finding{Path: path, Msg: msg}) }

	names := validateCatalog(*repoRoot, add)
	validateCells(*repoRoot, names, add)

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Msg < findings[j].Msg
	})

	if *asJSON {
		out := map[string]any{"ok": len(findings) == 0, "findings": findings}
		if findings == nil {
			out["findings"] = []finding{}
		}
		_ = writeJSON(stdout, out)
	} else if len(findings) == 0 {
		fmt.Fprintln(stdout, "of validate: OK — catalog + cells are schema-valid and referentially consistent")
	} else {
		fmt.Fprintf(stderr, "of validate: %d violation(s):\n", len(findings))
		for _, f := range findings {
			fmt.Fprintf(stderr, "  %s: %s\n", f.Path, f.Msg)
		}
	}
	if len(findings) > 0 {
		return exitFail
	}
	return exitOK
}

// validateCatalog parses replaydata/agents/scenarios.json, enforces the 5-field
// scenario schema + id/name well-formedness + uniqueness, and returns the set
// of valid scenario names for the referential checks.
func validateCatalog(repoRoot string, add func(path, msg string)) map[string]bool {
	names := map[string]bool{}
	path := shard.File(repoRoot)
	b, err := os.ReadFile(path)
	if err != nil {
		add(catalogRelPath, fmt.Sprintf("cannot read catalog: %v", err))
		return names
	}
	var doc struct {
		Meta      json.RawMessage   `json:"meta"`
		Scenarios []json.RawMessage `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		add(catalogRelPath, fmt.Sprintf("catalog is not valid JSON: %v", err))
		return names
	}
	if len(doc.Scenarios) == 0 {
		add(catalogRelPath, "catalog has no scenarios")
	}
	seenID := map[string]bool{}
	for _, raw := range doc.Scenarios {
		validateScenarioEntry(raw, seenID, names, add)
	}
	return names
}

// validateScenarioEntry validates one catalog scenario entry: its field set
// against allowedScenarioKeys, and its id/name well-formedness + uniqueness,
// recording into seenID/names as it goes (mirroring validateCatalog's
// original inline bookkeeping across the whole catalog).
func validateScenarioEntry(raw json.RawMessage, seenID map[string]bool, names map[string]bool, add func(path, msg string)) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		add(catalogRelPath, "a scenario entry is not a JSON object")
		return
	}
	var s struct{ ID, Name string }
	_ = json.Unmarshal(raw, &s)
	loc := "scenario " + s.Name
	if s.Name == "" {
		loc = "scenario id=" + s.ID
	}
	for k := range fields {
		if !allowedScenarioKeys[k] {
			add(loc, fmt.Sprintf("unexpected field %q (allowed: id, name, description, acceptance_criteria, process)", k))
		}
	}
	if s.ID == "" {
		add(loc, "missing id")
	} else if !idRe.MatchString(s.ID) {
		add(loc, fmt.Sprintf("id %q is not <section>.<index>", s.ID))
	} else if seenID[s.ID] {
		add(loc, fmt.Sprintf("duplicate id %q", s.ID))
	}
	seenID[s.ID] = true
	if s.Name == "" {
		add(loc, "missing name")
	} else {
		if !nameRe.MatchString(s.Name) {
			add(loc, fmt.Sprintf("name %q is not a kebab slug", s.Name))
		}
		if names[s.Name] {
			add(loc, fmt.Sprintf("duplicate name %q", s.Name))
		}
		names[s.Name] = true
	}
}

// validateCells checks each agent's scenarios/ cells: metadata.json parses,
// links to a real scenario (scenario_id FK), and recorded cells carry an
// expected.jsonl. Orphan folders (recordings but no metadata.json) are flagged.
func validateCells(repoRoot string, names map[string]bool, add func(path, msg string)) {
	for _, agent := range shard.Agents(repoRoot) {
		scenDir := filepath.Join(repoRoot, "replaydata", "agents", agent, "scenarios")
		entries, err := os.ReadDir(scenDir)
		if err != nil {
			continue // adapter may have no scenarios/ tree yet
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			folder := e.Name()
			rel := filepath.Join("replaydata/agents", agent, "scenarios", folder)
			metaPath := filepath.Join(scenDir, folder, "metadata.json")
			hasRecordings := dirHasChildren(filepath.Join(scenDir, folder, "recordings"))

			mb, err := os.ReadFile(metaPath)
			if err != nil {
				if hasRecordings {
					add(rel, "orphan recording folder: has recordings/ but no metadata.json")
				}
				continue
			}
			var cell shard.ShardAgent
			if err := json.Unmarshal(mb, &cell); err != nil {
				add(rel+metadataJSONSuffix, fmt.Sprintf("not valid JSON: %v", err))
				continue
			}
			if cell.ScenarioID == "" {
				add(rel+metadataJSONSuffix, "missing scenario_id (the cell→catalog foreign key)")
			} else if !names[cell.ScenarioID] {
				add(rel+metadataJSONSuffix, fmt.Sprintf("scenario_id %q not in the catalog", cell.ScenarioID))
			}
			// A recipe the driver will run must carry the fields the driver reads
			// positionally — a missing timeout_seconds once crashed a driver.
			if msg := recipeTimeoutFinding(cell.Details.Recipe); msg != "" {
				add(rel+metadataJSONSuffix, msg)
			}
			// A cell is "recorded" iff NewestRecordingDir resolves one — the SAME
			// definition matrix.cellRecorded uses, so the two never disagree.
			// (hasRecordings/dirHasChildren above is only for orphan detection:
			// content present but no metadata.json.)
			cellDir := filepath.Join(scenDir, folder)
			if recDir, ok := validate.NewestRecordingDir(cellDir); ok {
				if !fileExists(filepath.Join(cellDir, "expected.jsonl")) {
					add(rel, "recorded cell is missing expected.jsonl")
				}
				// The newest recording is authoritative (it gates validation and
				// the viewer autoselects it); it must be complete. Older recordings
				// are kept as drift signals. The on-disk tree is the single source
				// of truth, so an incomplete newest recording is a hard error.
				recRel := filepath.Join(rel, "recordings", filepath.Base(recDir))
				for _, finding := range validate.RecordingComplete(recDir) {
					add(recRel, "incomplete recording: "+finding)
				}
			}
		}
	}
}

func dirHasChildren(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
