package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
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

type managedFileOracleOptions struct {
	baselineDir string
	outputDir   string
	realHome    string
	bindAddress string
}

type managedFileEntry struct {
	state string
	slot  string
	path  string
}

type managedPathPair struct {
	real   string
	shadow string
}

func runManagedFileOracle(args []string) error {
	var value managedFileOracleOptions
	flags := flag.NewFlagSet("managed-file-oracle", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&value.baselineDir, "baseline-dir", "", "active managed-file snapshot")
	flags.StringVar(&value.outputDir, "output-dir", "", "new expected-state directory")
	flags.StringVar(&value.realHome, "real-home", "", "home used by the real recorder")
	flags.StringVar(&value.bindAddress, "bind-address", "", "real recorder loopback address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("managed-file-oracle accepts flags only")
	}
	return buildManagedFileOracle(value)
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
	declaration := claudecode.Agent()
	applyPermissions := managedApplyPermissions(declaration)
	if len(applyPermissions) == 0 || len(applyPermissions) > maxOracleApplyPermissions {
		return fmt.Errorf(
			"Claude Code declares %d managed Apply closures; expected 1..%d",
			len(applyPermissions),
			maxOracleApplyPermissions,
		)
	}
	realPaths, err := declaredManagedPaths(declaration)
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
	statesDir := filepath.Join(options.outputDir, "states")
	if err := os.Mkdir(statesDir, 0o700); err != nil {
		return fmt.Errorf("create oracle states directory: %w", err)
	}
	allMask := (1 << len(applyPermissions)) - 1
	for mask := 0; mask <= allMask; mask++ {
		stateDir := filepath.Join(statesDir, strconv.Itoa(mask))
		shadowHome := filepath.Join(options.outputDir, ".shadow-"+strconv.Itoa(mask))
		if err := buildOracleVariant(
			options.baselineDir,
			stateDir,
			options.realHome,
			shadowHome,
			declaration,
			entries,
			realPaths,
			applyPermissions,
			mask,
		); err != nil {
			return fmt.Errorf("build oracle state %d: %w", mask, err)
		}
	}
	if err := copyOracleState(
		filepath.Join(statesDir, strconv.Itoa(allMask)),
		options.outputDir,
		entries,
	); err != nil {
		return err
	}
	complete = true
	return nil
}

func buildOracleVariant(
	baselineDir string,
	stateDir string,
	realHome string,
	shadowHome string,
	declaration agent.Agent,
	entries []managedFileEntry,
	realPaths []string,
	applyPermissions []agent.Permission,
	mask int,
) error {
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
	shadowPaths, err := declaredManagedPaths(declaration)
	if err != nil {
		return err
	}
	pairs, err := pairManagedPaths(realHome, shadowHome, realPaths, shadowPaths)
	if err != nil {
		return err
	}
	if err := materializeShadowBaseline(baselineDir, entries, pairs); err != nil {
		return err
	}
	if err := applyManagedPermissions(applyPermissions, mask); err != nil {
		return err
	}
	return writeOracleState(baselineDir, stateDir, entries, pairs)
}

func validateOracleOptions(options managedFileOracleOptions) error {
	paths := []struct {
		name  string
		value string
	}{
		{name: "baseline directory", value: options.baselineDir},
		{name: "output directory", value: options.outputDir},
		{name: "real home", value: options.realHome},
	}
	for _, path := range paths {
		if !filepath.IsAbs(path.value) {
			return fmt.Errorf("%s must be absolute", path.name)
		}
	}
	baselineInfo, err := os.Lstat(options.baselineDir)
	if err != nil || !baselineInfo.IsDir() || baselineInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("baseline directory must be an existing non-symlink directory: %q", options.baselineDir)
	}
	if filepath.Clean(options.outputDir) != filepath.Join(filepath.Clean(options.baselineDir), "oracle") {
		return errors.New("oracle output must be the new oracle directory inside the baseline")
	}
	currentHome, err := os.UserHomeDir()
	if err != nil || filepath.Clean(currentHome) != filepath.Clean(options.realHome) {
		return fmt.Errorf("real home %q does not match the process HOME %q", options.realHome, currentHome)
	}
	if _, err := os.Lstat(options.outputDir); !os.IsNotExist(err) {
		return errors.New("oracle output must not exist")
	}
	host, port, err := net.SplitHostPort(options.bindAddress)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("oracle bind address must be numeric IPv4 loopback: %q", options.bindAddress)
	}
	numericPort, err := strconv.Atoi(port)
	if err != nil || numericPort < 1 || numericPort > 65535 {
		return fmt.Errorf("oracle bind port is invalid: %q", port)
	}
	return nil
}

// parseManagedFileRow validates one manifest row in isolation. keep is false
// for a row this oracle does not model (an absent directory); every other
// shape that is not a well-formed file row is an error, never a skip.
func parseManagedFileRow(line string) (managedFileEntry, bool, error) {
	parts := strings.SplitN(line, "\t", 3)
	if len(parts) != 3 {
		return managedFileEntry{}, false, fmt.Errorf("invalid managed-file manifest row %q", line)
	}
	entry := managedFileEntry{state: parts[0], slot: parts[1], path: parts[2]}
	if entry.state == "absentdir" {
		return managedFileEntry{}, false, nil
	}
	if entry.state != "saved" && entry.state != "absent" {
		return managedFileEntry{}, false, fmt.Errorf("invalid managed-file state %q", entry.state)
	}
	slotNumber, slotErr := strconv.Atoi(entry.slot)
	if slotErr != nil || slotNumber < 0 || strconv.Itoa(slotNumber) != entry.slot {
		return managedFileEntry{}, false, fmt.Errorf("invalid managed-file slot %q", entry.slot)
	}
	if !filepath.IsAbs(entry.path) {
		return managedFileEntry{}, false, fmt.Errorf("duplicate or invalid managed-file identity for %q", entry.path)
	}
	return entry, true, nil
}

func readManagedFileManifest(path string) ([]managedFileEntry, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("managed-file manifest is not a regular file: %q", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var entries []managedFileEntry
	seenSlots := map[string]bool{}
	seenPaths := map[string]bool{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		entry, keep, err := parseManagedFileRow(scanner.Text())
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}
		if seenSlots[entry.slot] || seenPaths[entry.path] {
			return nil, fmt.Errorf("duplicate or invalid managed-file identity for %q", entry.path)
		}
		seenSlots[entry.slot] = true
		seenPaths[entry.path] = true
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("managed-file manifest has no file rows")
	}
	return entries, nil
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
		source := ""
		state := entry.state
		if shadowPath, ok := pairs[entry.path]; ok {
			info, statErr := os.Lstat(shadowPath)
			switch {
			case os.IsNotExist(statErr):
				state = "absent"
			case statErr != nil:
				return statErr
			case !info.Mode().IsRegular():
				return fmt.Errorf("oracle Apply produced a non-regular path: %q", shadowPath)
			default:
				state = "saved"
				source = shadowPath
			}
		} else if state == "saved" {
			source = filepath.Join(baselineDir, entry.slot)
		}
		oracleState := "absent"
		if state == "saved" {
			data, mode, err := readBoundedRegularFile(source)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(outputDir, entry.slot), data, mode.Perm()); err != nil {
				return err
			}
			oracleState = "file"
		}
		if _, err := fmt.Fprintf(manifest, "%s\t%s\t%s\n", oracleState, entry.slot, entry.path); err != nil {
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
