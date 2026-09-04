package driverteardown

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentsDir is the catalog directory holding one subdirectory per adapter.
const AgentsDir = "replaydata/agents"

// DriverName is the file every onboarded adapter carries.
const DriverName = "driver-interactive.sh"

// libDir holds the shell libraries the drivers source. They participate in the
// analysis (alloc_slot lives there, and so does the slot bookkeeping a session
// name flows through) but are never themselves reported: they are shared, so a
// finding there would be raised once per adapter against a file no adapter owns.
const libDir = "replaydata/_lib/drive"

// Adapters lists every adapter in the catalog that ships a driver, read from
// disk so an adapter onboarded tomorrow is graded without being listed here.
func Adapters(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, AgentsDir))
	if err != nil {
		return nil, fmt.Errorf("reading the adapter catalog: %w", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		driverPath := filepath.Join(root, AgentsDir, e.Name(), DriverName)
		if _, err := os.Lstat(driverPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("checking %s: %w", driverPath, err)
		}
		if _, err := os.Stat(driverPath); err != nil {
			return nil, fmt.Errorf("checking %s: %w", driverPath, err)
		}
		out = append(out, e.Name())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no adapter under %s ships a %s — a broken scan and an empty "+
			"catalog must not be the same answer", filepath.Join(root, AgentsDir), DriverName)
	}
	sort.Strings(out)
	return out, nil
}

// LoadDriver reads one adapter's driver plus every shell library it CAN source
// — the shared drive libs and the adapter's own sibling scripts.
//
// "Can", not "does": the whole _lib/drive directory is globbed for every
// adapter, because a driver's `source` lines are computed paths
// (`"$_DRIVE_LIB/slots.sh"`, `"$(dirname "${BASH_SOURCE[0]}")/turn-count.sh"`)
// and libraries source one another, so resolving the real set statically would
// itself be a place this package could be wrong — and getting it wrong DOWNWARD
// turns a resolvable trap handler into a refusal for the whole fleet.
//
// The cost is that every adapter is handed definitions it never sources. That
// is why nameFlow keys positional facts on a funcRef rather than on a bare
// function name: see funcRef for the two `alloc_slot`s this collides.
func LoadDriver(root, adapter string) (File, []File, error) {
	path := filepath.Join(root, AgentsDir, adapter, DriverName)
	src, err := os.ReadFile(path) // #nosec G304 -- built from the repo root and a scanned adapter slug
	if err != nil {
		return File{}, nil, fmt.Errorf("reading %s's driver: %w", adapter, err)
	}
	driver := File{Path: path, Src: string(src)}

	var libs []File
	for _, dir := range []string{filepath.Join(root, libDir), filepath.Join(root, AgentsDir, adapter)} {
		found, err := readShellDir(dir, path)
		if err != nil {
			return File{}, nil, err
		}
		libs = append(libs, found...)
	}
	return driver, libs, nil
}

// readShellDir reads every non-test .sh file in dir except skip.
func readShellDir(dir, skip string) ([]File, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.sh"))
	if err != nil {
		return nil, err
	}
	var out []File
	for _, m := range matches {
		if m == skip || strings.HasSuffix(m, "_test.sh") {
			continue
		}
		b, err := os.ReadFile(m) // #nosec G304 -- a path returned by Glob over a repo directory
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", m, err)
		}
		out = append(out, File{Path: m, Src: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
