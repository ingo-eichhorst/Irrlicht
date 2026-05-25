//go:build !darwin

package processlifecycle

import (
	"fmt"
	"runtime"

	"irrlicht/core/ports/outbound"
)

// stubObserver is the placeholder process observer for every non-darwin
// platform. Discovery returns nothing, so no sessions are observed via the
// process scanner — but the package compiles and the daemon boots. The real
// per-OS mechanism (Linux /proc) replaces this in process_linux.go; env
// reading already works cross-platform via readProcessEnv, so EnvOf is wired
// through even here.
type stubObserver struct{}

func newObserver() outbound.ProcessObserver { return stubObserver{} }

func (stubObserver) FindByName(string) ([]int, error)    { return nil, nil }
func (stubObserver) FindByCmdline(string) ([]int, error) { return nil, nil }

func (stubObserver) CWDOf(pid int) (string, error) {
	return "", fmt.Errorf("process observation unsupported on %s", runtime.GOOS)
}

func (stubObserver) WriterOf(string) (int, error) { return 0, nil }

func (stubObserver) EnvOf(pid int) (map[string]string, error) { return readProcessEnv(pid) }
