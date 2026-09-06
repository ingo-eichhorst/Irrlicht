package desktopdriver

// Claude Desktop stops exposing its composer to the accessibility tree the
// moment it is backgrounded. Every site that drives a control must therefore
// bring it forward FIRST, on every attempt — not once at the start of the run.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// countingFront records how many times a drive site brought Desktop forward.
func countingFront(calls *int) func(context.Context) error {
	return func(context.Context) error {
		*calls++
		return nil
	}
}

// RED-FIRST. This is cell 2-20 on 2026-09-06:
//
//	Desktop recipe step 3 (interrupt): interrupt the in-flight Desktop turn:
//	Claude Desktop's accessibility tree kept moving across 5 attempts; last
//	failure: Desktop environment control requires one AXPopUpButton titled
//	"Local"; found 0. Visible AXPopUpButton controls: described "More
//	navigation items", described "Filter", … "More options for …"
//
// Every visible control in that list belongs to the sidebar. The composer was
// not in the tree at all, because something else had taken focus during the
// recipe's `sleep` step before the interrupt — and interrupt, unlike submit and
// set-prompt, never brought Desktop back.
func TestEveryDriveSiteBringsDesktopForward(t *testing.T) {
	workspace := "/repo/workspace"
	settled := func(context.Context) ([]helperElement, error) {
		return controlsComposerElements("workspace"), nil
	}
	inFlight := func(context.Context) ([]helperElement, error) {
		return append(inFlightComposerElements("workspace"),
			fixtureElement("send", "AXButton", "", "Send")), nil
	}
	noClick := func(context.Context, helperSelector, helperPostcondition) error { return nil }

	tests := map[string]func(activate func(context.Context) error) error{
		"interrupt": func(activate func(context.Context) error) error {
			return interruptTurn(context.Background(), workspace, activate, inFlight, noClick)
		},
		"press key": func(activate func(context.Context) error) error {
			keyboard := func(context.Context, helperSelector, uint16, []string, helperPostcondition) error {
				return nil
			}
			return pressKey(context.Background(), "Enter", workspace, activate, settled, keyboard)
		},
		"submit": func(activate func(context.Context) error) error {
			return submitPrompt(context.Background(), workspace, activate, settled, noClick)
		},
		"set prompt": func(activate func(context.Context) error) error {
			setValue := func(context.Context, helperSelector, string) error { return nil }
			return setPrompt(context.Background(), workspace, OwnedSession{}, "hi", activate, settled, setValue)
		},
	}
	for name, drive := range tests {
		t.Run(name, func(t *testing.T) {
			calls := 0
			if err := drive(countingFront(&calls)); err != nil {
				t.Fatalf("%s error = %v", name, err)
			}
			if calls == 0 {
				t.Fatalf("%s drove a control without bringing Desktop forward; "+
					"a backgrounded Desktop exposes no composer at all", name)
			}
		})
	}
}

// The behavioural test above can only cover the sites it names. This one is the
// tripwire for a site nobody has written yet: every retry loop in the live
// runtime drives a control, and each one must front Desktop before it looks.
func TestEveryLiveRetryLoopFrontsDesktopFirst(t *testing.T) {
	const window = 6 // lines of closure body to search after the retry call
	retryCalls := []string{"retryTransientAX(", "retryTransientAXFor(", "retryIdempotentAXFor("}
	files := []string{"live.go", "live_controls.go", "live_archive.go"}

	checked, waived := 0, 0
	for _, name := range files {
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v; this check cannot run, which is a failure", name, err)
		}
		source := readSourceLines(t, name)
		// The retry helpers call one another. Their own bodies drive nothing, so
		// skip what is declared inside them rather than flagging the mechanism
		// for not using itself.
		type span struct{ from, to int }
		var helpers []span
		for _, decl := range parsed.Decls {
			function, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			switch function.Name.Name {
			case "retryTransientAX", "retryTransientAXFor", "retryIdempotentAXFor", "retryIdempotentAX":
				helpers = append(helpers, span{
					from: fileSet.Position(function.Pos()).Line,
					to:   fileSet.Position(function.End()).Line,
				})
			}
		}
		insideHelper := func(line int) bool {
			for _, helper := range helpers {
				if line >= helper.from && line <= helper.to {
					return true
				}
			}
			return false
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			line := fileSet.Position(call.Pos()).Line
			text := source[line-1]
			named := false
			for _, retry := range retryCalls {
				if strings.Contains(text, retry) {
					named = true
				}
			}
			if !named || insideHelper(line) {
				return true
			}
			checked++
			body := strings.Join(source[line:min(line+window, len(source))], "\n")
			// A site may opt out, but only in writing. `// nofront: <reason>`
			// inside the loop's first lines is the waiver, and a bare marker
			// with no reason does not count — the point is that skipping this
			// is a decision somebody made on purpose and can be read back.
			if waiver := strings.Index(body, "nofront:"); waiver >= 0 {
				reason := strings.TrimSpace(body[waiver+len("nofront:"):])
				if reason == "" {
					t.Errorf("%s:%d — nofront waiver carries no reason", name, line)
				}
				waived++
				return true
			}
			if !strings.Contains(body, "activate(ctx)") && !strings.Contains(body, "runtime.front(ctx)") {
				t.Errorf("%s:%d — this retry loop drives a control but does not bring Desktop "+
					"forward in its first lines; a backgrounded Desktop exposes no composer:\n%s",
					name, line, body)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no retry loops were found in the live runtime; this check cannot run, which is a failure")
	}
	if waived == checked {
		t.Fatalf("all %d retry loops are waived; the rule would be checking nothing", checked)
	}
	t.Logf("checked %d live retry loops (%d waived in writing)", checked, waived)
}

func readSourceLines(t *testing.T, name string) []string {
	t.Helper()
	data, err := readFileForTest(name)
	if err != nil {
		t.Fatalf("read %s: %v; this check cannot run, which is a failure", name, err)
	}
	return strings.Split(string(data), "\n")
}

func readFileForTest(name string) ([]byte, error) { return os.ReadFile(name) }
