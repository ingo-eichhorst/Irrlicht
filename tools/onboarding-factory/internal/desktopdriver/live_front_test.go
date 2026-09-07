package desktopdriver

// Claude Desktop stops exposing its composer to the accessibility tree the
// moment it is backgrounded. Every site that drives a control must therefore
// bring it forward FIRST, on every attempt — not once at the start of the run.

import (
	"context"
	"encoding/json"
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
			return pressKey(context.Background(), "Enter", workspace, activate, settled, keyboard, nil)
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
	files := []string{"live.go", "live_controls.go", "live_archive.go"}
	census := retryFrontCensus{}
	for _, name := range files {
		census.inspect(t, parseRetryFrontFile(t, name))
	}
	census.assertUseful(t)
}

const retryFrontWindow = 6

var retryFunctionCalls = []string{
	"retryTransientAX(",
	"retryTransientAXFor(",
	"retryIdempotentAXFor(",
}

type sourceSpan struct{ from, to int }

type retryFrontFile struct {
	name    string
	fileSet *token.FileSet
	parsed  *ast.File
	source  []string
	helpers []sourceSpan
}

type retryFrontCensus struct {
	checked int
	waived  int
}

func parseRetryFrontFile(t *testing.T, name string) retryFrontFile {
	t.Helper()
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, name, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v; this check cannot run, which is a failure", name, err)
	}
	return retryFrontFile{
		name:    name,
		fileSet: fileSet,
		parsed:  parsed,
		source:  readSourceLines(t, name),
		helpers: retryHelperSpans(fileSet, parsed),
	}
}

func retryHelperSpans(fileSet *token.FileSet, parsed *ast.File) []sourceSpan {
	var helpers []sourceSpan
	for _, decl := range parsed.Decls {
		function, ok := decl.(*ast.FuncDecl)
		if ok && isRetryHelper(function.Name.Name) {
			helpers = append(helpers, sourceSpan{
				from: fileSet.Position(function.Pos()).Line,
				to:   fileSet.Position(function.End()).Line,
			})
		}
	}
	return helpers
}

func isRetryHelper(name string) bool {
	switch name {
	case "retryTransientAX", "retryTransientAXFor", "retryIdempotentAXFor", "retryIdempotentAX":
		return true
	default:
		return false
	}
}

func (file retryFrontFile) insideHelper(line int) bool {
	for _, helper := range file.helpers {
		if line >= helper.from && line <= helper.to {
			return true
		}
	}
	return false
}

func (file retryFrontFile) retryLine(node ast.Node) (int, bool) {
	call, ok := node.(*ast.CallExpr)
	if !ok {
		return 0, false
	}
	line := file.fileSet.Position(call.Pos()).Line
	return line, containsRetryCall(file.source[line-1]) && !file.insideHelper(line)
}

func containsRetryCall(line string) bool {
	for _, retry := range retryFunctionCalls {
		if strings.Contains(line, retry) {
			return true
		}
	}
	return false
}

func (census *retryFrontCensus) inspect(t *testing.T, file retryFrontFile) {
	t.Helper()
	ast.Inspect(file.parsed, func(node ast.Node) bool {
		line, isRetry := file.retryLine(node)
		if isRetry {
			census.inspectRetry(t, file, line)
		}
		return true
	})
}

func (census *retryFrontCensus) inspectRetry(t *testing.T, file retryFrontFile, line int) {
	t.Helper()
	census.checked++
	body := strings.Join(file.source[line:min(line+retryFrontWindow, len(file.source))], "\n")
	if census.acceptWaiver(t, file.name, line, body) {
		return
	}
	if !strings.Contains(body, "activate(ctx)") && !strings.Contains(body, "runtime.front(ctx)") {
		t.Errorf("%s:%d — this retry loop drives a control but does not bring Desktop "+
			"forward in its first lines; a backgrounded Desktop exposes no composer:\n%s",
			file.name, line, body)
	}
}

func (census *retryFrontCensus) acceptWaiver(t *testing.T, name string, line int, body string) bool {
	t.Helper()
	waiver := strings.Index(body, "nofront:")
	if waiver < 0 {
		return false
	}
	reason := strings.TrimSpace(body[waiver+len("nofront:"):])
	if reason == "" {
		t.Errorf("%s:%d — nofront waiver carries no reason", name, line)
	}
	census.waived++
	return true
}

func (census retryFrontCensus) assertUseful(t *testing.T) {
	t.Helper()
	if census.checked == 0 {
		t.Fatal("no retry loops were found in the live runtime; this check cannot run, which is a failure")
	}
	if census.waived == census.checked {
		t.Fatalf("all %d retry loops are waived; the rule would be checking nothing", census.checked)
	}
	t.Logf("checked %d live retry loops (%d waived in writing)", census.checked, census.waived)
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

// A failure dump exists to answer one question: where was the control, and what
// else was there? It cannot answer it if the decoder drops the geometry.
//
// RED-FIRST: before helperElement carried a Frame, this decoded to nil and the
// first archive-failure tree written on 2026-09-07 had `frame: null` on every
// one of its 513 controls.
func TestHelperElementsCarryGeometryForFailureDumps(t *testing.T) {
	const response = `{"ok":true,"elements":[{"path":[0],"role":"AXPopUpButton",
	  "description":"More options for X","hierarchy":["AXApplication","AXWindow"],
	  "frame":{"x":1815,"y":-175,"width":20,"height":20}}]}`
	var decoded helperResponse
	if err := json.Unmarshal([]byte(response), &decoded); err != nil {
		t.Fatalf("decode: %v; this check cannot run, which is a failure", err)
	}
	if len(decoded.Elements) != 1 {
		t.Fatalf("decoded %d elements, want 1", len(decoded.Elements))
	}
	frame := decoded.Elements[0].Frame
	if frame == nil {
		t.Fatal("the element carries no frame; a failure dump cannot say where the control was")
	}
	if frame.X != 1815 || frame.Y != -175 || frame.Width != 20 || frame.Height != 20 {
		t.Errorf("frame = %+v, want the measured geometry verbatim", *frame)
	}
	// And it must survive the round trip a dump makes.
	data, err := json.Marshal(decoded.Elements)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if !strings.Contains(string(data), `"frame"`) {
		t.Errorf("the dump dropped the frame on the way out: %s", data)
	}
}
