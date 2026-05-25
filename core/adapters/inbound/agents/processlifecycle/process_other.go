//go:build !darwin && !linux

package processlifecycle

import (
	"fmt"
	"runtime"

	"irrlicht/core/ports/outbound"
)

// stubObserver is the placeholder process observer for platforms without a
// native mechanism yet (e.g. Windows, until process_windows.go lands).
// Discovery returns nothing, so no sessions are observed via the process
// scanner — but the package compiles and the daemon boots. Env reading still
// works wherever readProcessEnv is implemented, so EnvOf is wired through.
type stubObserver struct{}

func newObserver() outbound.ProcessObserver { return stubObserver{} }

func (stubObserver) FindByName(string) ([]int, error)    { return nil, nil }
func (stubObserver) FindByCmdline(string) ([]int, error) { return nil, nil }

func (stubObserver) CWDOf(pid int) (string, error) {
	return "", fmt.Errorf("process observation unsupported on %s", runtime.GOOS)
}

func (stubObserver) WriterOf(string) (int, error) { return 0, nil }

func (stubObserver) EnvOf(pid int) (map[string]string, error) {
	m, _ := readProcessEnv(pid) // empty map, never an error (port contract)
	return m, nil
}
