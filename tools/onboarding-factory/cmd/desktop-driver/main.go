// Command desktop-driver drives one bounded Claude Desktop Local turn.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"irrlicht/tools/onboarding-factory/internal/desktopdriver"
)

// Version is injected by the recording precheck for build provenance.
var Version = "dev"

type options struct {
	repoRoot        string
	staging         string
	workspace       string
	promptFile      string
	helper          string
	daemonAddress   string
	recordings      string
	irrlichtVersion string
	timeout         time.Duration
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "desktop-driver: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "managed-file-oracle" {
		return runManagedFileOracle(args[1:])
	}
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	prompt, err := os.ReadFile(options.promptFile)
	if err != nil {
		return fmt.Errorf("read prompt file: %w", err)
	}
	if len(prompt) == 0 {
		return errors.New("prompt file is empty")
	}
	evidenceDir := filepath.Join(options.staging, "desktop-evidence")
	runtime, err := desktopdriver.NewLiveRuntime(desktopdriver.LiveOptions{
		Home:               userHome(),
		HelperPath:         options.helper,
		DaemonAddress:      options.daemonAddress,
		RecordingDirectory: options.recordings,
		IrrlichtVersion:    options.irrlichtVersion,
	}, filepath.Join(options.staging, "desktop-driver.steps"))
	if err != nil {
		return err
	}
	result, err := desktopdriver.Run(ctx, runtime, desktopdriver.RunRequest{
		Workspace:      options.workspace,
		Prompt:         string(prompt),
		EvidenceDir:    evidenceDir,
		OverallTimeout: options.timeout,
		StepTimeout:    min(options.timeout/3, 90*time.Second),
		CleanupTimeout: 45 * time.Second,
	})
	if err != nil {
		return err
	}
	if err := writeResultFiles(options.staging, result); err != nil {
		return err
	}
	return nil
}

func parseOptions(args []string) (options, error) {
	var value options
	flags := flag.NewFlagSet("desktop-driver", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&value.repoRoot, "repo-root", "", "absolute repository root")
	flags.StringVar(&value.staging, "staging", "", "absolute staging directory")
	flags.StringVar(&value.workspace, "workspace", "", "absolute staging workspace")
	flags.StringVar(&value.promptFile, "prompt-file", "", "prompt file under staging")
	flags.StringVar(&value.helper, "helper", "", "absolute helper executable")
	flags.StringVar(&value.daemonAddress, "daemon-address", "", "recording daemon host:port")
	flags.StringVar(&value.recordings, "recordings", "", "recording directory")
	flags.StringVar(&value.irrlichtVersion, "irrlicht-version", "", "recording daemon version")
	flags.DurationVar(&value.timeout, "timeout", 120*time.Second, "whole-run deadline")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("desktop-driver accepts flags only")
	}
	paths := []struct {
		name  string
		value string
	}{
		{"repo root", value.repoRoot},
		{"staging", value.staging},
		{"workspace", value.workspace},
		{"prompt file", value.promptFile},
		{"helper", value.helper},
		{"recordings", value.recordings},
	}
	for _, path := range paths {
		if !filepath.IsAbs(path.value) {
			return options{}, fmt.Errorf("%s must be absolute", path.name)
		}
	}
	if value.daemonAddress == "" || value.irrlichtVersion == "" || value.timeout <= 0 {
		return options{}, errors.New("daemon address, Irrlicht version, and a positive timeout are required")
	}
	realRepo, err := filepath.EvalSymlinks(filepath.Clean(value.repoRoot))
	if err != nil {
		return options{}, fmt.Errorf("resolve repository root: %w", err)
	}
	realBuild, err := filepath.EvalSymlinks(filepath.Join(realRepo, ".build"))
	if err != nil {
		return options{}, fmt.Errorf("resolve repository .build: %w", err)
	}
	realStaging, err := resolveWithin("staging", value.staging, realBuild)
	if err != nil {
		return options{}, err
	}
	value.repoRoot = realRepo
	value.staging = realStaging
	for name, path := range map[string]*string{
		"workspace": &value.workspace, "prompt file": &value.promptFile,
		"recordings": &value.recordings,
	} {
		resolved, err := resolveWithin(name, *path, realStaging)
		if err != nil {
			return options{}, err
		}
		*path = resolved
	}
	value.helper, err = resolveWithin("helper", value.helper, realBuild)
	if err != nil {
		return options{}, err
	}
	return value, nil
}

func resolveWithin(name, path, root string) (string, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("%s must resolve under %q: %w", name, root, err)
	}
	if !pathWithin(resolved, root) {
		return "", fmt.Errorf("%s must resolve under %q", name, root)
	}
	return resolved, nil
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}

func writeResultFiles(staging string, result desktopdriver.RunResult) error {
	files := map[string]string{
		"session.uuid":       result.Owned.Transcript.SessionID + "\n",
		"transcript.path":    result.Evidence.TranscriptPath + "\n",
		"desktop.session-id": result.Owned.Registry.SessionID + "\n",
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(staging, name), []byte(value), 0o600); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(result.Versions, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(staging, "desktop.versions.json"), data, 0o600)
}

func userHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
