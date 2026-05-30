package viewer

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// handleScenarioSpec parses .specs/agent-scenarios.md on demand and returns
// the structured spec for one scenario id. Lookup matches by the kebab-case
// slug of each "### Feature: <name>" heading — the same slug the coverage
// JSON uses for its scenarios[].id field. 404 if the id has no heading.
func (s *Server) handleScenarioSpec(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/scenario-spec/")
	if id == "" {
		http.Error(w, "scenario id required", http.StatusBadRequest)
		return
	}
	specPath := s.resolveSpecPath()
	if specPath == "" {
		http.Error(w, ".specs/agent-scenarios.md not found in repo or main checkout", http.StatusNotFound)
		return
	}
	b, err := os.ReadFile(specPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("read %s: %v", specPath, err), http.StatusInternalServerError)
		return
	}
	out := parseScenarioSpec(string(b), id)
	if out == nil {
		http.Error(w, fmt.Sprintf("scenario %q not found in %s", id, specPath), http.StatusNotFound)
		return
	}
	writeJSON(w, out)
}

// resolveSpecPath mirrors resolveCoveragePath: look in the worktree first,
// then walk back to the main checkout via .git/worktrees if the worktree's
// `.git` is a pointer file. Returns "" if neither has the file.
func (s *Server) resolveSpecPath() string {
	direct := filepath.Join(s.RepoRoot, ".specs", "agent-scenarios.md")
	if _, err := os.Stat(direct); err == nil {
		return direct
	}
	gitMeta := filepath.Join(s.RepoRoot, ".git")
	st, err := os.Stat(gitMeta)
	if err != nil || st.IsDir() {
		return ""
	}
	data, err := os.ReadFile(gitMeta)
	if err != nil {
		return ""
	}
	const prefix = "gitdir:"
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	main := filepath.Dir(filepath.Dir(filepath.Dir(gitdir)))
	candidate := filepath.Join(main, ".specs", "agent-scenarios.md")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

// parseScenarioSpec walks the catalog markdown and pulls out the Feature
// heading matching `id`. The catalog's structure is regular:
//
//	## <Section>
//	### Feature: <Name>
//	Scenario: <one or more lines>
//	Expected:
//	- bullet
//	(blank)
//	Scenario: <next>
//	---
//
// Lookup matches the kebab-case slug of `<Name>` against `id`.
func parseScenarioSpec(md string, id string) *ScenarioSpec {
	wantSlug := strings.ToLower(id)
	var (
		section    string
		feature    string
		curSlug    string
		out        *ScenarioSpec
		curCase    *ScenarioSpecCase
		inExpected bool
	)
	flush := func() {
		if curCase == nil {
			return
		}
		curCase.Text = strings.TrimSpace(curCase.Text)
		if out != nil {
			out.Scenarios = append(out.Scenarios, *curCase)
		}
		curCase = nil
		inExpected = false
	}
	for _, raw := range strings.Split(md, "\n") {
		line := strings.TrimRight(raw, " \t")
		switch {
		case strings.HasPrefix(line, "## "):
			flush()
			if out != nil {
				return out
			}
			section = strings.TrimSpace(strings.TrimPrefix(line, "## "))
		case strings.HasPrefix(line, "### Feature:"):
			flush()
			if out != nil {
				return out
			}
			feature = strings.TrimSpace(strings.TrimPrefix(line, "### Feature:"))
			curSlug = slugifyFeature(feature)
			if curSlug == wantSlug {
				out = &ScenarioSpec{ID: id, Section: section, Feature: feature}
			}
		case strings.HasPrefix(line, "### "):
			// Other H3s (rare) — treat as feature break.
			flush()
			if out != nil {
				return out
			}
			feature = ""
			curSlug = ""
		case out != nil && strings.HasPrefix(line, "Scenario:"):
			flush()
			curCase = &ScenarioSpecCase{Text: strings.TrimSpace(strings.TrimPrefix(line, "Scenario:"))}
			inExpected = false
		case out != nil && strings.HasPrefix(line, "Expected:"):
			if curCase == nil {
				curCase = &ScenarioSpecCase{}
			}
			inExpected = true
		case out != nil && inExpected && strings.HasPrefix(strings.TrimSpace(line), "- "):
			curCase.Expected = append(curCase.Expected, strings.TrimPrefix(strings.TrimSpace(line), "- "))
		case out != nil && !inExpected && curCase != nil && strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "---"):
			// Continuation of the scenario text.
			if curCase.Text != "" {
				curCase.Text += " "
			}
			curCase.Text += strings.TrimSpace(line)
		case strings.HasPrefix(line, "---"):
			flush()
			if out != nil {
				return out
			}
		}
	}
	flush()
	return out
}

// slugifyFeature converts a Feature: heading like
// "Session reset (`/clear`, `/new`)" into the kebab id "session-reset" the
// coverage JSON uses. Strips parenthetical examples, lowercases, keeps
// alnum + hyphens. featureSlugAliases handles headings that don't slugify
// cleanly into the canonical id.
func slugifyFeature(f string) string {
	if alias, ok := featureSlugAliases[f]; ok {
		return alias
	}
	if i := strings.Index(f, "("); i >= 0 {
		f = strings.TrimSpace(f[:i])
	}
	out := make([]byte, 0, len(f))
	prevDash := false
	for i := 0; i < len(f); i++ {
		c := f[i]
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+32)
			prevDash = false
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			out = append(out, c)
			prevDash = false
		default:
			if !prevDash && len(out) > 0 {
				out = append(out, '-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(string(out), "-")
}

// featureSlugAliases handles cases where the markdown Feature heading
// wording doesn't slugify cleanly into the coverage id. Keep in sync with
// agent-scenarios-coverage.json — every id not derivable via slugifyFeature's
// default rule must have an entry.
var featureSlugAliases = map[string]string{
	"User-blocking tool call (question)":           "user-blocking-question",
	"User-blocking tool call (plan-mode approval)": "user-blocking-plan-mode-approval",
	"Tool gate via permission prompt":              "tool-gate-permission-prompt",
	"Session reset (`/clear`, `/new`)":             "session-reset",
	"Architect/Editor model pair":                  "architect-editor-pair",
	"User ESC interrupt":                           "user-esc-interrupt",
}
