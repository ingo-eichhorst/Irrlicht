package desktopdriver

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The environment measured on the Claude Desktop the rig had started itself,
// 2026-09-07. Three runs of cell 2-8 drove their whole recipe against this and
// then timed out twelve minutes later.
const contaminatedDesktopEnvironment = "/Applications/Claude.app/Contents/MacOS/Claude " +
	"__CFBundleIdentifier=com.anthropic.claudefordesktop " +
	"TERM=xterm-256color TERM_PROGRAM=vscode CLAUDECODE=1 CLAUDE_CODE_SSE_PORT=48021 " +
	"CLAUDE_CODE_MESSAGING_TOKEN=8552b1cd3558b4a48eab74512c601f0a " +
	"VSCODE_GIT_ASKPASS_NODE=/Applications/Visual"

// The same app after `env -i HOME=… PATH=… USER=… open -a Claude`. Plain
// `open -a Claude` is NOT enough: measured the same evening, it forwards the
// calling shell's environment and the app came back contaminated.
const cleanDesktopEnvironment = "/Applications/Claude.app/Contents/MacOS/Claude " +
	"__CFBundleIdentifier=com.anthropic.claudefordesktop " +
	"HOME=/Users/ingo PATH=/usr/bin:/bin:/usr/sbin:/sbin USER=ingo"

func reader(environment string) desktopEnvironmentReader {
	return func(context.Context, int) (string, error) { return environment, nil }
}

// RED-FIRST shape: without this guard the run reaches Desktop, opens a session,
// drives the whole recipe and only then fails — with a message about Irrlicht
// state, twelve minutes later, naming nothing that could be acted on.
func TestARunRefusesADesktopCarryingTheDrivingSessionsEnvironment(t *testing.T) {
	err := requireUncontaminatedDesktop(context.Background(), 27220, reader(contaminatedDesktopEnvironment))
	if err == nil {
		t.Fatal("requireUncontaminatedDesktop() = nil; this environment cost three runs their whole budget")
	}
	for _, want := range []string{"TERM_PROGRAM", "CLAUDECODE", "CLAUDE_CODE_SSE_PORT", "27220"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
	// The refusal has to be actionable, and the obvious command is the wrong one.
	if !strings.Contains(err.Error(), `env -i HOME="$HOME"`) {
		t.Errorf("the refusal does not give the scrubbed relaunch command: %v", err)
	}
	if !strings.Contains(err.Error(), "Plain `open -a Claude` is NOT enough") {
		t.Errorf("the refusal does not warn that plain open is not enough: %v", err)
	}
}

func TestACleanDesktopIsAccepted(t *testing.T) {
	if err := requireUncontaminatedDesktop(
		context.Background(), 62694, reader(cleanDesktopEnvironment)); err != nil {
		t.Fatalf("requireUncontaminatedDesktop() error = %v; this environment recorded cleanly", err)
	}
}

// A check that cannot look must not report the same thing as a check that
// looked and found nothing.
func TestAnUnreadableDesktopEnvironmentIsARefusal(t *testing.T) {
	cases := map[string]struct {
		pid  int
		read desktopEnvironmentReader
	}{
		"ps failed": {62694, func(context.Context, int) (string, error) {
			return "", errors.New("ps: no such process")
		}},
		"ps returned nothing": {62694, reader("   ")},
		"no process id":       {0, reader(cleanDesktopEnvironment)},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			err := requireUncontaminatedDesktop(context.Background(), testCase.pid, testCase.read)
			if err == nil {
				t.Fatal("requireUncontaminatedDesktop() = nil; it could not look, which is not a pass")
			}
			if !strings.Contains(err.Error(), "cannot check") {
				t.Fatalf("error = %v; want it to say it could not look", err)
			}
		})
	}
}

// A variable whose NAME merely contains a watched one is not that variable.
func TestOnlyExactVariableNamesCount(t *testing.T) {
	environment := "/Applications/Claude.app/Contents/MacOS/Claude " +
		"MY_TERM_PROGRAM=vscode CLAUDECODE_LEGACY=1 XCLAUDECODE=1"
	if err := requireUncontaminatedDesktop(context.Background(), 1, reader(environment)); err != nil {
		t.Fatalf("requireUncontaminatedDesktop() error = %v; none of these is a watched variable", err)
	}
}

func TestEachWatchedVariableIsEnoughOnItsOwn(t *testing.T) {
	for _, name := range contaminatingVariables {
		environment := cleanDesktopEnvironment + " " + name + "=x"
		err := requireUncontaminatedDesktop(context.Background(), 1, reader(environment))
		if err == nil {
			t.Errorf("%s alone was accepted; each watched variable must be enough on its own", name)
		}
	}
	if len(contaminatingVariables) == 0 {
		t.Fatal("no variables are watched, so this check cannot run, which is a failure")
	}
}
