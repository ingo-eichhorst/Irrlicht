package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// idRe is the kebab slug every provider directory and manifest id must match —
// the same shape replaydata/agents' scenario names use.
var idRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ManifestFile is the per-provider manifest's filename.
const ManifestFile = "manifest.json"

// relRoot is the tree's repo-relative path, used verbatim in finding messages
// so a reader can paste it.
const relRoot = "replaydata/providers"

// Finding is one violation: Path locates it, Message explains it. The shape
// mirrors desktopresults.Finding so cmd/of can adapt it in one line.
type Finding struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Root returns the absolute path of the provider tree.
func Root(repoRoot string) string { return filepath.Join(repoRoot, "replaydata", "providers") }

// Exists reports whether the provider tree is a DIRECTORY. A path that exists
// but is not one returns false.
//
// That makes it the wrong predicate for deciding whether to look: `of
// validate` scopes its provider arm on os.Stat instead, so a tree clobbered
// into a regular file still reaches ValidateRepo and is reported. Exists is
// for callers asking "is there a usable catalog here" -- the shipped-catalog
// tripwire, and the locks that assert a fixture tree has none.
func Exists(repoRoot string) bool {
	info, err := os.Stat(Root(repoRoot))
	return err == nil && info.IsDir()
}

// Loaded is one provider directory that parsed. Raw keeps the decoded object
// so the closed-field-set check can run against the same bytes the struct was
// built from.
type Loaded struct {
	ID       string
	Dir      string // absolute
	RelDir   string // repo-relative, for findings
	Manifest Manifest
	Raw      map[string]json.RawMessage
}

// loadResult carries what one read produced: the parsed manifests plus the
// findings the read itself generated. Loading and validating are split the way
// internal/shard splits them — but the READ ERRORS travel with the result
// rather than being swallowed, because an unreadable tree and an empty tree
// must not produce the same output (AGENTS.md).
type loadResult struct {
	Providers []Loaded
	Findings  []Finding
}

// load walks replaydata/providers/, parsing one manifest per subdirectory.
//
// Every way this can fail to look is a finding: an unreadable tree, an entry
// that is not a directory, a missing manifest, a manifest that is not JSON.
// None of them is a silent skip.
func load(repoRoot string) loadResult {
	var out loadResult
	root := Root(repoRoot)

	entries, err := os.ReadDir(root)
	if err != nil {
		out.Findings = append(out.Findings, Finding{
			Path:    relRoot,
			Message: fmt.Sprintf("cannot read the provider tree: %v", err),
		})
		return out
	}

	for _, e := range entries {
		out.absorb(loadEntry(root, e))
	}
	if len(out.Providers) == 0 && len(out.Findings) == 0 {
		out.Findings = append(out.Findings, Finding{
			Path:    relRoot,
			Message: "the provider tree holds no provider directories — an empty catalog and an unread one must not look alike",
		})
	}
	return out
}

func (r *loadResult) absorb(other loadResult) {
	r.Providers = append(r.Providers, other.Providers...)
	r.Findings = append(r.Findings, other.Findings...)
}

// loadEntry parses one directory entry under the provider tree.
func loadEntry(root string, e os.DirEntry) loadResult {
	name := e.Name()
	rel := relRoot + "/" + name

	// A README at the tree root documents the tree; it is not a provider.
	if !e.IsDir() {
		if name == "README.md" {
			return loadResult{}
		}
		return loadResult{Findings: []Finding{{
			Path:    rel,
			Message: "the provider tree holds one directory per provider; this entry is a file",
		}}}
	}

	path := filepath.Join(root, name, ManifestFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return loadResult{Findings: []Finding{{
			Path:    rel + "/" + ManifestFile,
			Message: fmt.Sprintf("cannot read the manifest: %v", err),
		}}}
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return loadResult{Findings: []Finding{{
			Path:    rel + "/" + ManifestFile,
			Message: fmt.Sprintf("the manifest is not a JSON object: %v", err),
		}}}
	}

	// Decoded separately from raw, and NOT gated on the error: Go's decoder
	// populates what it could decode while still returning an error, so
	// skipping the checks on an error would let one wrong-typed field switch
	// off every unrelated check. Input that cannot be parsed with confidence
	// gets MORE checking, not less.
	var m Manifest
	var findings []Finding
	if err := json.Unmarshal(b, &m); err != nil {
		findings = append(findings, Finding{
			Path:    rel + "/" + ManifestFile,
			Message: fmt.Sprintf("the manifest does not match the schema: %v", err),
		})
	}

	findings = append(findings, unknownNestedFieldFindings(rel+"/"+ManifestFile, b, raw)...)

	return loadResult{
		Providers: []Loaded{{ID: name, Dir: filepath.Join(root, name), RelDir: rel, Manifest: m, Raw: raw}},
		Findings:  findings,
	}
}

// unknownNestedFieldFindings catches a field the schema does not define
// ANYWHERE in the manifest, not only at the top level.
//
// The top-level closed-key check in verifyIdentity compares raw's keys against
// manifestKeys, which says nothing about the nested objects; they decode into
// structs that discard what they do not recognise. The failure that matters is
// a misspelled OPTIONAL field — "mutation_fixtuers" under implementation drops
// four mutation-fixture claims and every required-field check still passes.
//
// DisallowUnknownFields stops at the FIRST unknown field rather than listing
// them all, so this is a diagnostic that reveals one per run. It is filtered
// to unknown-field errors: a type error is already reported by the caller's
// own decode, and reporting it twice would say nothing new.
func unknownNestedFieldFindings(at string, b []byte, raw map[string]json.RawMessage) []Finding {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var strict Manifest
	err := dec.Decode(&strict)
	if err == nil {
		return nil
	}
	const prefix = "json: unknown field "
	idx := strings.Index(err.Error(), prefix)
	if idx < 0 {
		return nil
	}
	name := strings.Trim(err.Error()[idx+len(prefix):], `"`)
	if _, topLevel := raw[name]; topLevel {
		// verifyIdentity already names this one, with the allowed set.
		return nil
	}
	return []Finding{{
		Path:    at,
		Message: fmt.Sprintf("nested field %q is not part of the manifest schema (a misspelled optional field reads as an absent one)", name),
	}}
}

// evidenceResolves reports whether an evidence entry's ref names something
// that is actually on disk. Used by Status to stop a dead citation earning a
// claim; ValidateRepo reports the dead citation itself.
func evidenceResolves(repoRoot string, e EvidenceEntry) bool {
	if !safeRelPath(e.Ref) {
		return false
	}
	p := filepath.Join(repoRoot, e.Ref)
	return fileExists(p) || dirExists(p)
}

// manifestKeys is the closed top-level field set. A key outside it is a
// violation, the way allowedScenarioKeys works for the scenario catalog.
var manifestKeys = []string{
	"schema_version", "id", "display_name", "_comment", "products",
	"observation", "authentication", "credential_resolvers", "platforms",
	"redaction", "fixtures", "capabilities", "evidence",
}

// safeRelPath reports whether p is a usable repo-relative path: non-empty,
// not absolute, and free of "..". The readers under internal/validate answer a
// ".."-bearing path with an EMPTY result rather than an error, so a path this
// function rejects has to become a finding here instead of being handed on.
func safeRelPath(p string) bool {
	if p == "" || filepath.IsAbs(p) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	return clean != ".." && !strings.HasPrefix(clean, "../")
}

// fileExists reports whether p is an existing regular file.
func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// dirExists reports whether p is an existing directory.
func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// oneOf renders a closed set for a finding message.
func oneOf(values []string) string { return strings.Join(values, ", ") }

// inSet is slices.Contains, named for how it reads at the call sites.
func inSet(v string, set []string) bool { return slices.Contains(set, v) }
