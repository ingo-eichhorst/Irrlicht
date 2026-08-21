package righome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the committed mutation evidence for Reconcile and Table, in the
// shape AGENTS.md asks for: "prefer committing that mutation to describing it",
// because a paragraph in a merged PR body is re-run by nothing.
//
// The live wiring next door cannot supply it. Mutating THAT means editing the
// shell table or an adapter's path resolution, which is a different change with
// a different blast radius, and the evidence would live in a PR description
// again. So each row below is an input that is wrong in exactly ONE way, paired
// with the fragment of the problem Reconcile must report AND the fragments it
// must NOT — the silences are half the evidence, since seven mutations against
// one function are equally satisfied by a function that complains about
// everything.

// registry is a stand-in for hookcov.Declared(): every adapter present, the
// value answering "declares a hooks permission". A false is "asked and
// answered", which is what lets an unknown name be told from a hookless one.
func registry() map[string]bool {
	return map[string]bool{
		"claudecode":   true,
		"codex":        true,
		"copilot":      true,
		"gemini-cli":   true,
		"kiro-cli":     true,
		"mistral-vibe": true,
		"aider":        false,
		"pi":           false,
	}
}

func goodRows() []Row {
	return []Row{
		{Adapter: "copilot", EnvVar: "COPILOT_HOME", Policy: PolicyDefault},
		{Adapter: "codex", EnvVar: "CODEX_HOME", Policy: PolicyOptIn},
		{Adapter: "kiro-cli", EnvVar: "KIRO_HOME", Policy: PolicyOptIn},
		{Adapter: "mistral-vibe", EnvVar: "VIBE_HOME", Policy: PolicyOptIn},
	}
}

func goodExempt() map[string]string {
	return map[string]string{
		"claudecode": "hook config is $HOME-anchored",
		"gemini-cli": "no override exists",
	}
}

// allFragments is every distinguishing phrase Reconcile can produce. A case
// names the ones it expects; everything else in this list must be absent, which
// is what stops a mutation from being "satisfied" by an unrelated complaint.
var allFragments = []string{
	"neither a row",
	"not in the daemon's adapter registry — the rig",
	"an exemption for something that does not exist",
	"declares no hooks permission",
	"has BOTH a row",
	"twice",
	"broken measurement",
}

func TestReconcileNamesEveryWrongShape(t *testing.T) {
	cases := []struct {
		name    string
		rows    []Row
		exempt  map[string]string
		reg     map[string]bool
		reports []string // fragments that MUST appear; empty means "silent"
		names   string   // an adapter name the report must also carry, when relevant
	}{
		{
			// The vacuity guard. Without it a Reconcile that reported
			// unconditionally would satisfy every row below and read as
			// excellent coverage.
			name:   "the real shape reports nothing",
			rows:   goodRows(),
			exempt: goodExempt(),
			reg:    registry(),
		},
		{
			// A non-hooks adapter with a row is legitimate: isolation is useful
			// for fixture hygiene whether or not the adapter installs anything.
			// This is the want:false case — the rule must not drift into
			// "only hooks adapters may be isolated".
			name:   "a row for a hookless adapter is fine",
			rows:   append(goodRows(), Row{Adapter: "aider", EnvVar: "AIDER_HOME", Policy: PolicyOptIn}),
			exempt: goodExempt(),
			reg:    registry(),
		},
		{
			name:    "a hooks adapter with neither a row nor an exemption",
			rows:    goodRows()[:3], // mistral-vibe dropped
			exempt:  goodExempt(),
			reg:     registry(),
			reports: []string{"neither a row"},
			names:   "mistral-vibe",
		},
		{
			name:    "a row naming an adapter the registry does not have",
			rows:    append(goodRows(), Row{Adapter: "kiro", EnvVar: "KIRO_HOME", Policy: PolicyOptIn}),
			exempt:  goodExempt(),
			reg:     registry(),
			reports: []string{"not in the daemon's adapter registry — the rig"},
			names:   "kiro",
		},
		{
			name:   "an exemption naming an adapter the registry does not have",
			rows:   goodRows(),
			exempt: map[string]string{"claudecode": "x", "gemini-cli": "y", "gemini": "z"},
			reg:    registry(),
			// The stale name is the whole point: an exemption for something that
			// does not exist reads as coverage of something that does.
			reports: []string{"an exemption for something that does not exist"},
			names:   "gemini",
		},
		{
			name:    "an exemption for an adapter that declares no hooks",
			rows:    goodRows(),
			exempt:  map[string]string{"claudecode": "x", "gemini-cli": "y", "aider": "z"},
			reg:     registry(),
			reports: []string{"declares no hooks permission"},
			names:   "aider",
		},
		{
			name:    "an adapter with both a row and an exemption",
			rows:    goodRows(),
			exempt:  map[string]string{"claudecode": "x", "gemini-cli": "y", "codex": "z"},
			reg:     registry(),
			reports: []string{"has BOTH a row"},
			names:   "codex",
		},
		{
			name:    "the same adapter declared twice",
			rows:    append(goodRows(), Row{Adapter: "codex", EnvVar: "CODEX_DIR", Policy: PolicyOptIn}),
			exempt:  goodExempt(),
			reg:     registry(),
			reports: []string{"twice"},
			names:   "codex",
		},
		{
			// The measurement-broke case, distinct from a clean result. Without
			// it, a caller whose registry projection came back empty would get
			// an empty problem list — the "absence of a finding and inability to
			// look produce the same output" shape.
			name:    "an empty registry projection refuses",
			rows:    goodRows(),
			exempt:  goodExempt(),
			reg:     map[string]bool{},
			reports: []string{"broken measurement"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(Reconcile(tc.rows, tc.exempt, tc.reg), "\n")
			assertReported(t, got, tc.reports, tc.names)
			assertSilentOnEverythingElse(t, got, tc.reports)
		})
	}
}

// assertReported is the positive half: the fragments this case claims, plus the
// offending adapter's name. Naming a fragment of the obligation's OWN message
// rather than accepting any failure is the rule this corpus is built on —
// "Reconcile failed" and "THIS check fired" are different claims.
func assertReported(t *testing.T, got string, want []string, names string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("report does not contain %q\nreport was:\n%s", w, got)
		}
	}
	if names != "" && !strings.Contains(got, `"`+names+`"`) {
		t.Errorf("report does not name the offending adapter %q\nreport was:\n%s", names, got)
	}
}

// assertSilentOnEverythingElse is the half that carries most of the evidence:
// without it, eight inputs wrong in eight ways are equally satisfied by a
// Reconcile that complains about everything.
func assertSilentOnEverythingElse(t *testing.T, got string, claimed []string) {
	t.Helper()
	for _, frag := range allFragments {
		if containsString(claimed, frag) {
			continue
		}
		if strings.Contains(got, frag) {
			t.Errorf("report unexpectedly contains %q — this case is wrong in one way and must "+
				"fire one check\nreport was:\n%s", frag, got)
		}
	}
	if len(claimed) == 0 && got != "" {
		t.Errorf("expected a silent report, got:\n%s", got)
	}
}

func containsString(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// TestTableRefusesRatherThanReturningAShortList covers the reader. Every check
// in Reconcile is satisfied by a table that came back empty or truncated, so
// Table must never hand one back — "a validator that cannot parse its input
// checks MORE, never less".
func TestTableRefusesRatherThanReturningAShortList(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "an empty table",
			body: "agent_home_table() { :; }\n",
			want: "declared no adapters",
		},
		{
			name: "a row missing its policy",
			body: "agent_home_table() { printf '%s\\n' 'codex CODEX_HOME'; }\n",
			want: "malformed row",
		},
		{
			name: "a row with an unknown policy",
			body: "agent_home_table() { printf '%s\\n' 'codex CODEX_HOME maybe'; }\n",
			want: "unknown policy",
		},
		{
			name: "a shell that will not source",
			body: "agent_home_table() { \n",
			want: "running agent_home_table",
		},
		{
			// The vacuity guard for this table: a well-formed stand-in must be
			// READ, not merely not-refused, or every row above could be passing
			// because the reader rejects everything.
			name: "a well-formed table is read",
			body: "agent_home_table() { printf '%s\\n' 'codex CODEX_HOME optin'; }\n",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, TableScript)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\n"+tc.body), 0o644); err != nil {
				t.Fatal(err)
			}

			rows, err := Table(root)
			if tc.want == "" {
				assertTableWasRead(t, rows, err)
				return
			}
			assertTableRefused(t, rows, err, tc.want)
		})
	}
}

// assertTableWasRead is this corpus's vacuity guard: a well-formed stand-in
// must be READ, not merely not-refused, or every refusal row above could be
// passing because the reader rejects everything it is handed.
func assertTableWasRead(t *testing.T, rows []Row, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("a well-formed table was refused: %v", err)
	}
	if len(rows) != 1 || rows[0].Adapter != "codex" || rows[0].EnvVar != "CODEX_HOME" {
		t.Fatalf("read %+v, want one codex/CODEX_HOME/optin row", rows)
	}
}

// assertTableRefused pins both halves of a refusal: it names its own reason,
// and it returns NO rows. A short list is exactly what every check downstream
// cannot tell from a clean one.
func assertTableRefused(t *testing.T, rows []Row, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a refusal naming %q, got %d rows and no error", want, len(rows))
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("refusal does not name %q: %v", want, err)
	}
	if rows != nil {
		t.Errorf("a refusal returned %d rows — a short list is what every check downstream "+
			"cannot tell from a clean one", len(rows))
	}
}
