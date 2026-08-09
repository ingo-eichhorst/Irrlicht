package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// TestMain owns the one irrlichd binary the subprocess tests share, so it is
// removed after the run rather than leaked into the temp dir.
func TestMain(m *testing.M) {
	code := m.Run()
	sharedBin.remove()
	os.Exit(code)
}

// daemonBinary is the lazily built irrlichd shared by every test that needs a
// real process.
type daemonBinary struct {
	once sync.Once
	dir  string
	path string
	err  error
}

var sharedBin daemonBinary

func (b *daemonBinary) remove() {
	if b.dir != "" {
		_ = os.RemoveAll(b.dir)
	}
}

// buildIrrlichd compiles the daemon once per test binary and returns its path.
//
// Built lazily rather than eagerly in TestMain so an unrelated `go test -run …`
// does not pay for it, and shared rather than per-test because two tests need a
// real binary — the startup smoke test and the beacon's process-level
// exit-code test — and compiling the same unchanged package twice costs about
// 0.8s of a suite that otherwise finishes in a fraction of a second.
func buildIrrlichd(t *testing.T) string {
	t.Helper()
	sharedBin.once.Do(func() {
		sharedBin.dir, sharedBin.err = os.MkdirTemp("", "irrlichd-build")
		if sharedBin.err != nil {
			return
		}
		sharedBin.path = filepath.Join(sharedBin.dir, "irrlichd")
		if out, err := exec.Command("go", "build", "-o", sharedBin.path, ".").CombinedOutput(); err != nil {
			sharedBin.err = fmt.Errorf("%w\n%s", err, out)
		}
	})
	if sharedBin.err != nil {
		t.Fatalf("build irrlichd: %v", sharedBin.err)
	}
	return sharedBin.path
}
