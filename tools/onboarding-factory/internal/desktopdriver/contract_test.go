package desktopdriver

// The Desktop grammar has ONE owner — recipe.go's tables — and one scraped copy
// in replaydata/agents/claudecode/driver-desktop.sh, because recipe-lint and
// desktop-profile.sh read the driver FILE, not this package. These tests are
// what stop the copy from drifting: they scrape the shell file exactly the way
// the lint does and require it to equal the Go tables.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func desktopDriverScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..",
		"replaydata", "agents", "claudecode", "driver-desktop.sh")
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve driver-desktop.sh: %v", err)
	}
	return absolute
}

// scrapeDeclaration reads one top-level `NAME=` constant from a driver file,
// the way recipe-lint.sh's sed scrape does: first assignment wins, a trailing
// comment is dropped, and quotes are stripped.
//
// It returns ok=false when the constant is absent. Callers must treat that as a
// FAILURE, never as "nothing to compare" — a scrape that silently returns
// nothing is how a grammar guard stops guarding.
func scrapeDeclaration(source, name string) (string, bool) {
	pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `=(.*)$`)
	match := pattern.FindStringSubmatch(source)
	if match == nil {
		return "", false
	}
	value := match[1]
	if index := strings.Index(value, "#"); index >= 0 {
		value = value[:index]
	}
	value = strings.ReplaceAll(value, `"`, "")
	value = strings.ReplaceAll(value, "'", "")
	return strings.Join(strings.Fields(value), " "), true
}

// checkDesktopDeclarations is the guard itself, applied to any driver source so
// the mutation fixtures below exercise the same code the real file does.
func checkDesktopDeclarations(source string) error {
	elicits, ok := scrapeDeclaration(source, "DRIVE_ELICITS")
	if !ok {
		return fmt.Errorf("the driver declares no DRIVE_ELICITS; this check cannot run, which is a failure, not a pass")
	}
	if want := strings.Join(Primitives(), " "); elicits != want {
		return fmt.Errorf("DRIVE_ELICITS is %q, want %q", elicits, want)
	}
	missing, ok := scrapeDeclaration(source, "DRIVE_MISSING_CONTROLS")
	if !ok {
		return fmt.Errorf("the driver declares no DRIVE_MISSING_CONTROLS; this check cannot run, which is a failure, not a pass")
	}
	if want := strings.Join(MissingControls(), " "); missing != want {
		return fmt.Errorf("DRIVE_MISSING_CONTROLS is %q, want %q", missing, want)
	}
	slash, ok := scrapeDeclaration(source, "DRIVE_SLASH_REQUIRES_STEP_TYPE")
	if !ok {
		return fmt.Errorf("the driver declares no DRIVE_SLASH_REQUIRES_STEP_TYPE; this check cannot run, which is a failure, not a pass")
	}
	// The Desktop composer stores a typed slash command as prompt text, so a
	// slash smuggled through `send` must be refused by the lint too.
	if slash != "true" {
		return fmt.Errorf("DRIVE_SLASH_REQUIRES_STEP_TYPE is %q, want \"true\"", slash)
	}
	return nil
}

func TestDriverDesktopDeclarationMatchesTheGoGrammar(t *testing.T) {
	path := desktopDriverScript(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := checkDesktopDeclarations(string(raw)); err != nil {
		t.Fatalf("%s drifted from the Go grammar: %v\n"+
			"Regenerate with: go run ./tools/onboarding-factory/cmd/desktop-driver primitives", path, err)
	}
}

// The committed mutation fixtures. Each is a driver-desktop.sh whose scraped
// declaration has been changed in one way, and the guard above must go red on
// every one of them. Without these, the guard's green would only ever have been
// seen against a file that already agreed with it.
func TestDeclarationGuardGoesRedOnEveryCommittedMutation(t *testing.T) {
	fixtures := map[string]string{
		"elicits-drops-a-primitive.sh":       "DRIVE_ELICITS is",
		"elicits-adds-an-unelicited-step.sh": "DRIVE_ELICITS is",
		"missing-controls-renamed.sh":        "DRIVE_MISSING_CONTROLS is",
		"no-declarations.sh":                 "this check cannot run",
		"slash-allowed-in-send.sh":           "DRIVE_SLASH_REQUIRES_STEP_TYPE is",
	}
	dir := filepath.Join("testdata", "driver-declaration-mutations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read mutation fixtures: %v", err)
	}
	// Count the fixtures on disk against the table. A fixture added without a
	// table entry would otherwise never run, and this test would keep passing.
	found := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sh") {
			found++
		}
	}
	if found != len(fixtures) {
		t.Fatalf("found %d mutation fixtures in %s, expected %d", found, dir, len(fixtures))
	}
	for name, want := range fixtures {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read mutation fixture: %v", err)
			}
			err = checkDesktopDeclarations(string(raw))
			if err == nil {
				t.Fatal("the declaration guard accepted a mutated driver declaration")
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("guard error = %v; want it to name %q", err, want)
			}
		})
	}
}

// A driver-desktop.sh that no longer routes a recipe to --script-file would
// silently drive a script cell's raw JSON as a PROMPT: one turn whose text is
// the recipe. The shell file is not covered by the Go executor's tests, so this
// pins the two flags it must be able to pass.
func TestDriverDesktopRoutesBothInputForms(t *testing.T) {
	path := desktopDriverScript(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	source := string(raw)
	for _, needle := range []string{"--script-file", "--prompt-file", `type == "array"`} {
		if !strings.Contains(source, needle) {
			t.Fatalf("%s no longer carries %q", path, needle)
		}
	}
}
