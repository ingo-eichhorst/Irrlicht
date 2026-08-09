package main

import (
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestBeaconSubprocessAlwaysExitsZero runs the REAL binary and asserts the real
// process exit status.
//
// This exists because every other test of the contract asserts hookbeacon.Post's
// return value, and Post ends in a literal `return 0` — so those assertions
// cannot fail by construction, however the code around them changes. The thing
// the ticket actually promises is a property of the *process*: gemini-cli's
// BeforeTool and Copilot's preToolUse read the exit status, not a Go return
// value. Only a subprocess can observe a panic (exit 2), a mis-wired main(), an
// init() that calls log.Fatal, or a dispatch that fell through to runDaemon.
//
// Every case runs with no daemon reachable, which is the situation the whole
// mechanism exists for.
func TestBeaconSubprocessAlwaysExitsZero(t *testing.T) {
	bin := buildIrrlichd(t)

	// A port nothing is listening on, so the subprocess can never reach a real
	// daemon on this machine.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	deadAddr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("closing the reserved port: %v", err)
	}

	const payload = `{"hook_event_name":"BeforeTool","transcript_path":"/tmp/x.jsonl"}`

	tests := map[string]struct {
		args  []string
		stdin string
	}{
		"the form we install, with no daemon running": {[]string{"--version", "hook-post", "gemini-cli"}, payload},
		"the bare verb form":                          {[]string{"hook-post", "gemini-cli"}, payload},
		"no stdin at all":                             {[]string{"hook-post", "gemini-cli"}, ""},
		"malformed stdin":                             {[]string{"hook-post", "gemini-cli"}, "}{not json"},
		"no adapter argument":                         {[]string{"hook-post"}, payload},
		"an unsafe adapter argument":                  {[]string{"hook-post", "../../debug/bundle"}, payload},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(bin, tt.args...)
			cmd.Stdin = strings.NewReader(tt.stdin)
			cmd.Env = append(os.Environ(),
				"IRRLICHT_HOME="+t.TempDir(),
				"IRRLICHT_BIND_ADDR="+deadAddr,
			)
			var stdout, stderr strings.Builder
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			start := time.Now()
			err := cmd.Run()
			elapsed := time.Since(start)

			if err != nil {
				t.Errorf("exit status: %v (stderr: %s) — a non-zero exit from a pre-tool hook is decision \"deny\" on gemini-cli and Copilot, which breaks the user's tool call", err, stderr.String())
			}
			// Empty stdout is a hard requirement, not tidiness: gemini-cli and
			// Claude Code parse a JSON decision from a hook's stdout. This also
			// proves end-to-end that the leading --version guard did NOT reach
			// the version branch on a current binary.
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty — a hook's stdout is a decision channel", stdout.String())
			}
			if elapsed > 10*time.Second {
				t.Errorf("took %s; the agent awaits this process", elapsed)
			}
		})
	}
}
