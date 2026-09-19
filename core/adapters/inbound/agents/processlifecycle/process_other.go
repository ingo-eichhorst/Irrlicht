//go:build !darwin && !linux

package processlifecycle

import (
	"context"
	"fmt"
	"runtime"

	"irrlicht/core/ports/outbound"
)

// stubObserver is the placeholder process observer for platforms without a
// native mechanism yet (e.g. Windows, until process_windows.go lands).
// Discovery returns nothing, so no sessions are observed via the process
// scanner — but the package compiles and the daemon boots. Env reading has
// no implementation here either (readProcessEnv, osutil_other.go, always
// refuses), so EnvOf reports the same "unsupported" error CWDOf does rather
// than a silent empty map.
type stubObserver struct{}

func newObserver() outbound.ProcessObserver { return stubObserver{} }

func (stubObserver) ParentPIDOf(context.Context, int) (int, error) {
	return 0, fmt.Errorf("process ancestry unsupported on %s", runtime.GOOS)
}

func (stubObserver) FindByName(string) ([]int, error)    { return nil, nil }
func (stubObserver) FindByCmdline(string) ([]int, error) { return nil, nil }
func (stubObserver) ArgvOf(int) ([]string, error)        { return nil, nil }

func (stubObserver) CWDOf(pid int) (string, error) {
	return "", fmt.Errorf("process observation unsupported on %s", runtime.GOOS)
}

func (stubObserver) WriterOf(string) (int, error) { return 0, nil }

func (stubObserver) EnvOf(pid int, keys map[string]struct{}) (map[string]string, error) {
	return readProcessEnv(pid, keys)
}
