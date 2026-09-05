package main

// Building and writing the managed-file oracle's expected state, given
// already-validated options and an already-parsed baseline manifest (both
// defined in oracle_options.go). This is where the Claude Code agent
// declaration is resolved, its Apply closures are exercised across every
// ordered subset in a shadow HOME, and the resulting file states are diffed
// against the baseline and written out.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
	"irrlicht/core/pkg/daemonaddr"
)

const (
	maxOracleFileBytes        = 16 * 1024 * 1024
	maxOracleApplyPermissions = 6
)

// oracleBuild is what a managed-file oracle build resolves once and then
// reuses for every subset of Apply closures: the caller's options, the
// parsed baseline manifest, the Claude Code agent's declaration, the managed
// paths it declares (read while HOME is still the real recorder's HOME, so
// they name the real paths, not a shadow), and the Modify permissions whose
// Apply closures the oracle exercises.
type oracleBuild struct {
	options          managedFileOracleOptions
	entries          []managedFileEntry
	declaration      agent.Agent
	realPaths        []string
	applyPermissions []agent.Permission
}

func buildManagedFileOracle(options managedFileOracleOptions) error {
	if err := validateOracleOptions(options); err != nil {
		return err
	}
	entries, err := readManagedFileManifest(filepath.Join(options.baselineDir, "manifest"))
	if err != nil {
		return err
	}
	if err := os.Mkdir(options.outputDir, 0o700); err != nil {
		return fmt.Errorf("create oracle output: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(options.outputDir)
		}
	}()
	build, err := newOracleBuild(options, entries)
	if err != nil {
		return err
	}
	restoreHome := preserveEnvironment("HOME")
	defer restoreHome()
	restoreBind := preserveEnvironment(daemonaddr.EnvBindAddr)
	defer restoreBind()
	if err := os.Setenv(daemonaddr.EnvBindAddr, options.bindAddress); err != nil {
		return err
	}
	if err := build.writeEveryApplySubset(); err != nil {
		return err
	}
	complete = true
	return nil
}

// newOracleBuild resolves the agent declaration this oracle exercises: the
// Modify permissions that carry an Apply closure (rejecting a count outside
// 1..maxOracleApplyPermissions, since the mask below assumes it fits an int),
// and the real-HOME paths those permissions declare.
func newOracleBuild(options managedFileOracleOptions, entries []managedFileEntry) (*oracleBuild, error) {
	declaration := claudecode.Agent()
	applyPermissions := managedApplyPermissions(declaration)
	if len(applyPermissions) == 0 || len(applyPermissions) > maxOracleApplyPermissions {
		return nil, fmt.Errorf(
			"Claude Code declares %d managed Apply closures; expected 1..%d",
			len(applyPermissions),
			maxOracleApplyPermissions,
		)
	}
	realPaths, err := declaredManagedPaths(declaration)
	if err != nil {
		return nil, err
	}
	return &oracleBuild{
		options:          options,
		entries:          entries,
		declaration:      declaration,
		realPaths:        realPaths,
		applyPermissions: applyPermissions,
	}, nil
}

// writeEveryApplySubset builds one oracle state per ordered subset of the
// Apply closures — the mask enumerates which of them ran, so recovery from a
// permission denied partway through matches a state this oracle modeled —
// then copies the all-applied state to the oracle's own root as the default
// expected state.
func (build *oracleBuild) writeEveryApplySubset() error {
	statesDir := filepath.Join(build.options.outputDir, "states")
	if err := os.Mkdir(statesDir, 0o700); err != nil {
		return fmt.Errorf("create oracle states directory: %w", err)
	}
	allMask := (1 << len(build.applyPermissions)) - 1
	for mask := 0; mask <= allMask; mask++ {
		stateDir := filepath.Join(statesDir, strconv.Itoa(mask))
		shadowHome := filepath.Join(build.options.outputDir, ".shadow-"+strconv.Itoa(mask))
		if err := build.writeApplySubset(stateDir, shadowHome, mask); err != nil {
			return fmt.Errorf("build oracle state %d: %w", mask, err)
		}
	}
	return copyOracleState(
		filepath.Join(statesDir, strconv.Itoa(allMask)),
		build.options.outputDir,
		build.entries,
	)
}

// writeApplySubset runs the Apply closures named by mask in a fresh shadow
// HOME and records the resulting managed-file state under stateDir.
func (build *oracleBuild) writeApplySubset(stateDir, shadowHome string, mask int) error {
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(shadowHome, 0o700); err != nil {
		return fmt.Errorf("create oracle shadow HOME: %w", err)
	}
	defer os.RemoveAll(shadowHome)
	if err := os.Setenv("HOME", shadowHome); err != nil {
		return err
	}
	shadowPaths, err := declaredManagedPaths(build.declaration)
	if err != nil {
		return err
	}
	pairs, err := pairManagedPaths(build.options.realHome, shadowHome, build.realPaths, shadowPaths)
	if err != nil {
		return err
	}
	if err := materializeShadowBaseline(build.options.baselineDir, build.entries, pairs); err != nil {
		return err
	}
	if err := applyManagedPermissions(build.applyPermissions, mask); err != nil {
		return err
	}
	return writeOracleState(build.options.baselineDir, stateDir, build.entries, pairs)
}

func declaredManagedPaths(declaration agent.Agent) ([]string, error) {
	var paths []string
	for _, declaredPermission := range declaration.Permissions {
		if declaredPermission.Kind != permission.KindModify || declaredPermission.Writes == nil {
			continue
		}
		resolved, err := resolveWrittenPaths(declaration.Identity.Name, declaredPermission)
		if err != nil {
			return nil, err
		}
		paths = append(paths, resolved...)
	}
	return paths, nil
}

// resolveWrittenPaths runs one Modify permission's own path resolvers. A
// resolver that fails is an error, never a skipped path: a managed file this
// oracle cannot name is one the restore would not compare.
func resolveWrittenPaths(agentName string, declaredPermission agent.Permission) ([]string, error) {
	resolvers := append([]func() (string, error){declaredPermission.Writes.Path}, declaredPermission.Writes.Also...)
	paths := make([]string, 0, len(resolvers))
	for _, resolve := range resolvers {
		path, err := resolve()
		if err != nil {
			return nil, fmt.Errorf("resolve %s/%s managed path: %w", agentName, declaredPermission.Key, err)
		}
		paths = append(paths, filepath.Clean(path))
	}
	return paths, nil
}

func pairManagedPaths(realHome, shadowHome string, realPaths, shadowPaths []string) (map[string]string, error) {
	if len(realPaths) != len(shadowPaths) || len(realPaths) == 0 {
		return nil, errors.New("Claude Code managed-path projection changed while creating the oracle")
	}
	pairs := make(map[string]string, len(realPaths))
	for index, realPath := range realPaths {
		realRelative, err := filepath.Rel(filepath.Clean(realHome), realPath)
		if err != nil || realRelative == ".." || strings.HasPrefix(realRelative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("Claude Code managed path is outside real HOME: %q", realPath)
		}
		shadowPath := shadowPaths[index]
		shadowRelative, err := filepath.Rel(filepath.Clean(shadowHome), shadowPath)
		if err != nil || shadowRelative != realRelative {
			return nil, fmt.Errorf("Claude Code managed path did not relocate exactly: %q to %q", realPath, shadowPath)
		}
		if prior, exists := pairs[realPath]; exists && prior != shadowPath {
			return nil, fmt.Errorf("Claude Code managed path %q has inconsistent shadow paths", realPath)
		}
		pairs[realPath] = shadowPath
	}
	return pairs, nil
}

func materializeShadowBaseline(
	baselineDir string,
	entries []managedFileEntry,
	pairs map[string]string,
) error {
	byPath := make(map[string]managedFileEntry, len(entries))
	for _, entry := range entries {
		byPath[entry.path] = entry
	}
	for realPath, shadowPath := range pairs {
		entry, ok := byPath[realPath]
		if !ok {
			return fmt.Errorf("baseline does not contain Claude Code managed path %q", realPath)
		}
		if entry.state == "absent" {
			continue
		}
		data, mode, err := readBoundedRegularFile(filepath.Join(baselineDir, entry.slot))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(shadowPath), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(shadowPath, data, mode.Perm()); err != nil {
			return err
		}
	}
	return nil
}

func managedApplyPermissions(declaration agent.Agent) []agent.Permission {
	var permissions []agent.Permission
	for _, declaredPermission := range declaration.Permissions {
		if declaredPermission.Kind != permission.KindModify || declaredPermission.Writes == nil {
			continue
		}
		permissions = append(permissions, declaredPermission)
	}
	return permissions
}

func applyManagedPermissions(permissions []agent.Permission, mask int) error {
	for index, declaredPermission := range permissions {
		if mask&(1<<index) == 0 {
			continue
		}
		if declaredPermission.Apply == nil {
			return fmt.Errorf("Claude Code managed permission %q has no Apply closure", declaredPermission.Key)
		}
		if err := declaredPermission.Apply(); err != nil {
			return fmt.Errorf("apply Claude Code permission %q in oracle HOME: %w", declaredPermission.Key, err)
		}
	}
	return nil
}

func copyOracleState(sourceDir, outputDir string, entries []managedFileEntry) error {
	manifest, _, err := readBoundedRegularFile(filepath.Join(sourceDir, "manifest"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		source := filepath.Join(sourceDir, entry.slot)
		info, statErr := os.Lstat(source)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("oracle state slot is not a regular file: %q", source)
		}
		data, mode, err := readBoundedRegularFile(source)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outputDir, entry.slot), data, mode.Perm()); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(outputDir, "manifest"), manifest, 0o600); err != nil {
		return err
	}
	return nil
}

// oracleEntrySource picks the bytes one manifest entry contributes to the
// expected state: what the Apply closures wrote in the shadow HOME when the
// path is one they touch, the baseline copy when it is not, and "" when the
// expected state is that the path is absent.
func oracleEntrySource(
	baselineDir string,
	entry managedFileEntry,
	pairs map[string]string,
) (string, error) {
	shadowPath, touched := pairs[entry.path]
	if !touched {
		if entry.state == "saved" {
			return filepath.Join(baselineDir, entry.slot), nil
		}
		return "", nil
	}
	info, err := os.Lstat(shadowPath)
	switch {
	case os.IsNotExist(err):
		return "", nil
	case err != nil:
		return "", err
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("oracle Apply produced a non-regular path: %q", shadowPath)
	}
	return shadowPath, nil
}

func writeOracleState(
	baselineDir, outputDir string,
	entries []managedFileEntry,
	pairs map[string]string,
) error {
	manifest, err := os.OpenFile(filepath.Join(outputDir, "manifest.tmp"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	manifestComplete := false
	defer func() {
		_ = manifest.Close()
		if !manifestComplete {
			_ = os.Remove(filepath.Join(outputDir, "manifest.tmp"))
		}
	}()
	for _, entry := range entries {
		if err := writeOracleManifestEntry(baselineDir, outputDir, entry, pairs, manifest); err != nil {
			return err
		}
	}
	if err := manifest.Close(); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(outputDir, "manifest.tmp"), filepath.Join(outputDir, "manifest")); err != nil {
		return err
	}
	manifestComplete = true
	return nil
}

// writeOracleManifestEntry resolves the bytes one manifest entry contributes
// to the expected state, writes them to the entry's output slot when the
// state is not absent, and appends the resulting manifest row.
func writeOracleManifestEntry(
	baselineDir, outputDir string,
	entry managedFileEntry,
	pairs map[string]string,
	manifest io.Writer,
) error {
	source, err := oracleEntrySource(baselineDir, entry, pairs)
	if err != nil {
		return err
	}
	oracleState := "absent"
	if source != "" {
		data, mode, err := readBoundedRegularFile(source)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outputDir, entry.slot), data, mode.Perm()); err != nil {
			return err
		}
		oracleState = "file"
	}
	_, err = fmt.Fprintf(manifest, "%s\t%s\t%s\n", oracleState, entry.slot, entry.path)
	return err
}

func readBoundedRegularFile(path string) ([]byte, os.FileMode, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("oracle source is not a regular file: %q", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxOracleFileBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(data) > maxOracleFileBytes {
		return nil, 0, fmt.Errorf("oracle source exceeds %d bytes: %q", maxOracleFileBytes, path)
	}
	return data, info.Mode(), nil
}

func preserveEnvironment(name string) func() {
	value, present := os.LookupEnv(name)
	return func() {
		if present {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	}
}
