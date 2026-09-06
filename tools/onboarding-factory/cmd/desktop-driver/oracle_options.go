package main

// The managed-file oracle's validated inputs: the CLI flags that configure a
// build, and the baseline manifest naming which of Claude Code's managed
// files this run models. Nothing here knows about the Claude Code agent
// declaration, its Apply closures, or shadow HOMEs — that begins in
// oracle.go, once these inputs are already valid. This file has no
// dependency on the agent/permission domain at all: it is pure flag and
// manifest-text handling.

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// validateOracleOptions checks the oracle's flags in the same order the
// caller needs to reason about them: which paths must be absolute, that the
// baseline exists, where the output must land, that this process really is
// the real recorder's HOME, that the output does not already exist, and that
// the bind address names a loopback port. Each check is its own function so a
// failure names exactly one thing, and no check is skipped or reordered.
func validateOracleOptions(options managedFileOracleOptions) error {
	if err := validateOracleAbsolutePaths(options); err != nil {
		return err
	}
	if err := validateOracleBaselineDirectory(options.baselineDir); err != nil {
		return err
	}
	if err := validateOracleOutputPath(options); err != nil {
		return err
	}
	if err := validateOracleRealHome(options.realHome); err != nil {
		return err
	}
	if err := validateOracleOutputAbsent(options.outputDir); err != nil {
		return err
	}
	return validateOracleBindAddress(options.bindAddress)
}

func validateOracleAbsolutePaths(options managedFileOracleOptions) error {
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
	return nil
}

func validateOracleBaselineDirectory(baselineDir string) error {
	baselineInfo, err := os.Lstat(baselineDir)
	if err != nil || !baselineInfo.IsDir() || baselineInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("baseline directory must be an existing non-symlink directory: %q", baselineDir)
	}
	return nil
}

func validateOracleOutputPath(options managedFileOracleOptions) error {
	if filepath.Clean(options.outputDir) != filepath.Join(filepath.Clean(options.baselineDir), "oracle") {
		return errors.New("oracle output must be the new oracle directory inside the baseline")
	}
	return nil
}

func validateOracleRealHome(realHome string) error {
	currentHome, err := os.UserHomeDir()
	if err != nil || filepath.Clean(currentHome) != filepath.Clean(realHome) {
		return fmt.Errorf("real home %q does not match the process HOME %q", realHome, currentHome)
	}
	return nil
}

func validateOracleOutputAbsent(outputDir string) error {
	if _, err := os.Lstat(outputDir); !os.IsNotExist(err) {
		return errors.New("oracle output must not exist")
	}
	return nil
}

func validateOracleBindAddress(bindAddress string) error {
	host, port, err := net.SplitHostPort(bindAddress)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("oracle bind address must be numeric IPv4 loopback: %q", bindAddress)
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
