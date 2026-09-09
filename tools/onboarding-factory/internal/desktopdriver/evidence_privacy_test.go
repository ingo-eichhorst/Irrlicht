package desktopdriver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A tree dumped from a live Claude Desktop window carries the operator's whole
// screen. no-stop-control-1.46388.4.json shipped the macOS Recent Items menu —
// four document filenames — and ten conversation titles into a public
// repository, and nothing noticed until a later reading of the same file.
//
// This audit is the thing that notices. It runs over every committed
// accessibility tree in the shared evidence directory, and over the mutation
// fixtures in testdata/privacy that each plant one leak class back.
//
// tools/onboarding-factory/scripts/redact-ax-tree.py is what makes a captured
// tree pass it.
const sharedEvidenceDir = "../../../../replaydata/agents/claudecode/desktop-evidence"

const sessionTitlePrefix = "More options for "

var (
	pseudonymisedSession = regexp.MustCompile(`^session-\d+$`)
	operatorHomePath     = regexp.MustCompile(`/Users/[^/\s"]+`)
	emailAddress         = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	redactedHomePath     = "/Users/«operator»"
)

// auditEvidenceTree names every privacy defect it can see in one committed
// tree. An input it cannot read is a finding, never a silent pass: that is the
// one case where the audit would otherwise report "clean" precisely because it
// looked at nothing.
func auditEvidenceTree(name string, data []byte) []string {
	var elements []struct {
		Hierarchy   []string `json:"hierarchy"`
		Role        string   `json:"role"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
	}
	if err := json.Unmarshal(data, &elements); err != nil {
		return []string{fmt.Sprintf("%s cannot be read as an accessibility tree: %v", name, err)}
	}
	if len(elements) == 0 {
		return []string{fmt.Sprintf("%s holds no elements, so this audit proved nothing", name)}
	}

	var findings []string
	for index, element := range elements {
		for _, ancestor := range element.Hierarchy {
			if ancestor == "AXMenuBar" {
				findings = append(findings, fmt.Sprintf(
					"%s element %d (%s %q) sits under AXMenuBar, the subtree that carries Recent Items",
					name, index, element.Role, element.Title))
				break
			}
		}
		if title, found := strings.CutPrefix(element.Description, sessionTitlePrefix); found {
			if !pseudonymisedSession.MatchString(title) {
				findings = append(findings, fmt.Sprintf(
					"%s element %d names a conversation %q; conversation titles must read session-N",
					name, index, title))
			}
		}
		for _, text := range []string{element.Title, element.Description} {
			for _, path := range operatorHomePath.FindAllString(text, -1) {
				if path != redactedHomePath {
					findings = append(findings, fmt.Sprintf(
						"%s element %d carries the home path %q", name, index, path))
				}
			}
			if address := emailAddress.FindString(text); address != "" {
				findings = append(findings, fmt.Sprintf(
					"%s element %d carries the email address %q", name, index, address))
			}
		}
	}
	return findings
}

func TestCommittedEvidenceTreesCarryNoOperatorContent(t *testing.T) {
	trees, err := filepath.Glob(filepath.Join(sharedEvidenceDir, "*.json"))
	if err != nil {
		t.Fatalf("list the evidence directory: %v", err)
	}
	if len(trees) == 0 {
		t.Fatalf("no accessibility tree under %s; this audit proved nothing", sharedEvidenceDir)
	}
	for _, tree := range trees {
		data, err := os.ReadFile(tree)
		if err != nil {
			t.Fatalf("read %s: %v", tree, err)
		}
		for _, finding := range auditEvidenceTree(filepath.Base(tree), data) {
			t.Errorf("%s", finding)
		}
	}
}

// The audit above passes over the committed trees, which is exactly what it
// would do if it had stopped looking. These fixtures plant one leak class each
// and must each be caught.
func TestEvidenceAuditCatchesEveryLeakClass(t *testing.T) {
	fixtures := []struct {
		file string
		want string
	}{
		{"menubar.json", "AXMenuBar"},
		{"free-title.json", "names a conversation"},
		{"home-path.json", "home path"},
		{"email.json", "email address"},
		{"unparseable.json", "cannot be read"},
		{"not-an-array.json", "cannot be read"},
		{"empty.json", "proved nothing"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "privacy", fixture.file))
			if err != nil {
				t.Fatalf("read the fixture: %v", err)
			}
			findings := auditEvidenceTree(fixture.file, data)
			if len(findings) == 0 {
				t.Fatalf("the audit found nothing in %s; it does not catch this leak", fixture.file)
			}
			if !strings.Contains(strings.Join(findings, "\n"), fixture.want) {
				t.Fatalf("findings %q do not name %q", findings, fixture.want)
			}
		})
	}

	clean := []byte(`[{"hierarchy":["AXApplication","AXWebArea"],"role":"AXPopUpButton",` +
		`"title":"","description":"More options for session-3"},` +
		`{"hierarchy":["AXApplication"],"role":"AXStaticText","title":"/Users/«operator»/x"}]`)
	if findings := auditEvidenceTree("clean.json", clean); len(findings) != 0 {
		t.Fatalf("the audit flagged a redacted tree: %q", findings)
	}
}
