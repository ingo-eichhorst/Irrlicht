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
	scriptFile      string
	helper          string
	daemonAddress   string
	recordings      string
	irrlichtVersion string
	timeout         time.Duration
}

// notRunnableExit is the process status for `not runnable through Desktop`. It
// is distinct from 1 so a caller can tell "this recipe needs a control the
// Desktop driver does not have" — which changed nothing — apart from a run that
// started and failed.
const notRunnableExit = 6

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "desktop-driver: %v\n", err)
		var notRunnable *desktopdriver.NotRunnableError
		if errors.As(err, &notRunnable) {
			os.Exit(notRunnableExit)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "managed-file-oracle":
			return runManagedFileOracle(args[1:])
		case "primitives":
			return printPrimitives(args[1:])
		case "plan":
			return planOnly(args[1:])
		}
	}
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	request, err := driveRequest(options)
	if err != nil {
		return err
	}
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
	result, err := desktopdriver.Run(ctx, runtime, request)
	if err != nil {
		return err
	}
	return writeResultFiles(options.staging, result)
}

// driveRequest reads whichever input form was given. Exactly one of them is
// accepted, so a caller that passes both never silently gets one of them.
func driveRequest(options options) (desktopdriver.RunRequest, error) {
	request := desktopdriver.RunRequest{
		Workspace:      options.workspace,
		EvidenceDir:    filepath.Join(options.staging, "desktop-evidence"),
		OverallTimeout: options.timeout,
		StepTimeout:    min(options.timeout/3, 90*time.Second),
		CleanupTimeout: 45 * time.Second,
	}
	if options.scriptFile != "" {
		steps, err := readRecipe(options.scriptFile)
		if err != nil {
			return desktopdriver.RunRequest{}, err
		}
		request.Script = steps
		return request, nil
	}
	prompt, err := os.ReadFile(options.promptFile)
	if err != nil {
		return desktopdriver.RunRequest{}, fmt.Errorf("read prompt file: %w", err)
	}
	if len(prompt) == 0 {
		return desktopdriver.RunRequest{}, errors.New("prompt file is empty")
	}
	request.Prompt = string(prompt)
	return request, nil
}

func readRecipe(path string) ([]desktopdriver.Step, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read recipe script file: %w", err)
	}
	steps, err := desktopdriver.ParseRecipe(data)
	if err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, errors.New("recipe script file has no steps")
	}
	return steps, nil
}

// printPrimitives emits the two declarations replaydata/agents/claudecode/
// driver-desktop.sh carries, in the exact form the shell file must hold. It is
// how those lines are regenerated, and desktopdriver's contract test is what
// proves they were.
func printPrimitives(args []string) error {
	if len(args) != 0 {
		return errors.New("desktop-driver primitives takes no arguments")
	}
	fmt.Printf("DRIVE_ELICITS=%q\n", strings.Join(desktopdriver.Primitives(), " "))
	fmt.Printf("DRIVE_MISSING_CONTROLS=%q\n", strings.Join(desktopdriver.MissingControls(), " "))
	return nil
}

// planOnly answers "could this recipe run through Claude Desktop?" without
// starting anything. It is the static half of the refusal the driver makes
// again at run time.
func planOnly(args []string) error {
	flags := flag.NewFlagSet("desktop-driver plan", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	scriptFile := flags.String("script-file", "", "recipe script JSON file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *scriptFile == "" {
		return errors.New("usage: desktop-driver plan --script-file <path>")
	}
	steps, err := readRecipe(*scriptFile)
	if err != nil {
		return err
	}
	if err := desktopdriver.Plan(steps); err != nil {
		return err
	}
	fmt.Printf("runnable through Desktop: %d step(s)\n", len(steps))
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
	flags.StringVar(&value.scriptFile, "script-file", "", "recipe script JSON file under staging")
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
	if (value.promptFile == "") == (value.scriptFile == "") {
		return options{}, errors.New("exactly one of --prompt-file and --script-file is required")
	}
	inputName, inputPath := "prompt file", value.promptFile
	if value.scriptFile != "" {
		inputName, inputPath = "script file", value.scriptFile
	}
	paths := []struct {
		name  string
		value string
	}{
		{"repo root", value.repoRoot},
		{"staging", value.staging},
		{"workspace", value.workspace},
		{inputName, inputPath},
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
	within := map[string]*string{
		"workspace": &value.workspace, "recordings": &value.recordings,
	}
	if value.scriptFile != "" {
		within["script file"] = &value.scriptFile
	} else {
		within["prompt file"] = &value.promptFile
	}
	for name, path := range within {
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
	// The plural forms are run-cell.sh's multi-session curation contract, the
	// same two files every interactive driver writes. They are written for a
	// one-session run too, so a reader never has to know which shape ran.
	var transcriptIDs, desktopIDs []string
	for _, session := range result.Sessions {
		if session.TranscriptID != "" {
			transcriptIDs = append(transcriptIDs, session.TranscriptID)
		}
		if session.DesktopSessionID != "" {
			desktopIDs = append(desktopIDs, session.DesktopSessionID)
		}
	}
	files["session.uuids"] = joinLines(transcriptIDs)
	files["desktop.session-ids"] = joinLines(desktopIDs)
	// One path per session, and the count MUST match session.uuids: run-cell.sh
	// zips the two lists to build a multi-session fixture, and a short list
	// there would curate one session's transcript for a recording that names
	// several. Evidence resolves the first session's path today, so more than
	// one session is a loud refusal rather than a short list.
	if len(transcriptIDs) > 1 {
		return fmt.Errorf(
			"the run owns %d Desktop sessions but only the first session's transcript path was resolved; "+
				"multi-session evidence staging is not wired", len(transcriptIDs))
	}
	files["transcript.paths"] = joinLines([]string{result.Evidence.TranscriptPath})
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(staging, name), []byte(value), 0o600); err != nil {
			return err
		}
	}
	for name, value := range map[string]any{
		"desktop.versions.json": result.Versions,
		"desktop.sessions.json": result.Sessions,
	} {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if err := os.WriteFile(filepath.Join(staging, name), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func joinLines(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, "\n") + "\n"
}

func userHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
