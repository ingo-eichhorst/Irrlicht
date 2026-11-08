package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/opencode"
)

// TestShellServiceArgvMatchesTheGo keeps the opencode-v2 driver's shell test
// for "is this the 2.x service" (replaydata/agents/opencode-v2/service-api.sh
// oc2_is_service_args) equal to the daemon's own, opencode.IsServiceArgv, over
// a corpus of argv shapes. The driver refuses unless exactly one service runs
// because the daemon binds a service-hosted session only then; if the two
// tests disagreed, the driver could count a process the daemon does not, or
// the reverse (#2109 simplify review).
//
// Every argv here starts with an opencode binary, the half of the daemon's
// rule (processCmdPattern) this shell function also applies; two non-opencode
// argv0s are checked on the shell side alone.
func TestShellServiceArgvMatchesTheGo(t *testing.T) {
	lib := filepath.Join(mustAbs(t, filepath.Join("..", "..", "..", "..")), "replaydata", "agents", "opencode-v2", "service-api.sh")
	shell := func(argv []string) bool {
		t.Helper()
		// #nosec G204 -- bash runs the repo's own library with a fixed corpus.
		cmd := exec.Command("bash", append([]string{"-c", `source "$1"; shift; oc2_is_service_args "$@"`, "_", lib}, argv...)...)
		err := cmd.Run()
		if err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				t.Fatalf("running oc2_is_service_args: %v", err)
			}
			return false
		}
		return true
	}

	corpus := [][]string{
		{"/Users/x/.oc2sb/shim/opencode", "serve", "--service"},
		{"opencode2", "serve", "--service"},
		{"opencode.exe", "serve", "--port", "49374", "--service"},
		{"opencode", "--log-level", "debug", "serve", "--service"},
		{"opencode", "serve"},
		{"opencode", "run", "--service"},
		{"opencode", "serve", "--", "--service"},
		{"opencode", "-s", "serve", "--service"},
		{"opencode", "--session", "ses_x", "run", "hi"},
		{"opencode"},
	}
	trues := 0
	for _, argv := range corpus {
		want := opencode.IsServiceArgv(argv)
		if got := shell(argv); got != want {
			t.Errorf("%q: the driver's oc2_is_service_args says %v, opencode.IsServiceArgv says %v",
				strings.Join(argv, " "), got, want)
		}
		if want {
			trues++
		}
	}
	// Vacuity guard: a corpus the Go side rejects entirely would agree with a
	// shell function that always answers no.
	if trues == 0 {
		t.Fatal("no corpus argv is a service by opencode.IsServiceArgv — this compared nothing")
	}
	for _, argv := range [][]string{{"node", "opencode", "serve", "--service"}, {"/usr/bin/vim", "serve", "--service"}} {
		if shell(argv) {
			t.Errorf("%q: argv0 is not an opencode binary, but the driver counts it as the service", strings.Join(argv, " "))
		}
	}
}
