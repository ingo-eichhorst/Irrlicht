package desktopdriver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const pollInterval = 100 * time.Millisecond

const (
	maxProcessCensusBytes = 4 * 1024 * 1024
	maxProcessCensusRows  = 100_000
)

var errTranscriptPending = errors.New("Claude transcript has not appeared")

type LiveOptions struct {
	Home               string
	HelperPath         string
	DaemonAddress      string
	RecordingDirectory string
	IrrlichtVersion    string
	DesktopSupportRoot string
	ClaudeProjectsRoot string
	ConfigurationRoots []string
}

type LiveRuntime struct {
	options         LiveOptions
	helper          helperClient
	controls        map[string]helperSelector
	processes       map[string]int
	processEvidence map[string]ProcessEvidence
	processBaseline map[int]struct{}
	stepLog         string
	httpClient      *http.Client
	registryByID    map[string]RegistrySession
	workingSeen     map[string]bool
	deepLinkOpened  bool
	openDeepLink    func(context.Context, string) error
	processExists   func(int) (bool, error)
	listProcesses   func(context.Context) (map[int]struct{}, error)
	observeProcess  func(context.Context, int) (string, error)
}

func NewLiveRuntime(options LiveOptions, stepLog string) (*LiveRuntime, error) {
	if options.Home == "" || !filepath.IsAbs(options.Home) {
		return nil, errors.New("an absolute home directory is required")
	}
	if options.HelperPath == "" || !filepath.IsAbs(options.HelperPath) {
		return nil, errors.New("an absolute Desktop helper path is required")
	}
	if options.DaemonAddress == "" || options.RecordingDirectory == "" {
		return nil, errors.New("daemon address and recording directory are required")
	}
	if options.DesktopSupportRoot == "" {
		options.DesktopSupportRoot = filepath.Join(options.Home, "Library", "Application Support", "Claude")
	}
	if options.ClaudeProjectsRoot == "" {
		options.ClaudeProjectsRoot = filepath.Join(options.Home, ".claude", "projects")
	}
	if len(options.ConfigurationRoots) == 0 {
		options.ConfigurationRoots = defaultConfigurationRoots(options.Home, options.DesktopSupportRoot)
	}
	return &LiveRuntime{
		options: options, helper: helperClient{path: options.HelperPath},
		processes: map[string]int{}, processEvidence: map[string]ProcessEvidence{}, stepLog: stepLog,
		httpClient:     &http.Client{Timeout: 2 * time.Second},
		registryByID:   map[string]RegistrySession{},
		workingSeen:    map[string]bool{},
		openDeepLink:   openOfficialDesktopURL,
		processExists:  liveProcessExists,
		listProcesses:  readProcessCensus,
		observeProcess: processCommand,
	}, nil
}

func defaultConfigurationRoots(home, desktopRoot string) []string {
	return []string{
		filepath.Join(desktopRoot, "claude_desktop_config.json"),
		filepath.Join(desktopRoot, "config.json"),
		filepath.Join(desktopRoot, "cowork-enabled-cli-ops.json"),
		filepath.Join(desktopRoot, "extensions-blocklist.json"),
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".claude", "plugins"),
		filepath.Join(home, ".claude", "skills"),
	}
}

func (runtime *LiveRuntime) Preflight(ctx context.Context) (Versions, error) {
	info, err := os.Stat(runtime.options.HelperPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return Versions{}, fmt.Errorf("Desktop helper is not executable at %q", runtime.options.HelperPath)
	}
	status, err := runtime.helper.preflight(ctx)
	if err != nil {
		return Versions{}, err
	}
	return validateVersions(status, runtime.options.IrrlichtVersion)
}

func validateVersions(status helperStatus, irrlichtVersion string) (Versions, error) {
	if status.BundleIdentifier != desktopBundleID {
		return Versions{}, fmt.Errorf("Desktop bundle ID is %q, want %q", status.BundleIdentifier, desktopBundleID)
	}
	if status.DesktopVersion != supportedDesktopVersion {
		return Versions{}, fmt.Errorf(
			"Desktop version %q has no verified control catalog; supported version is %q",
			status.DesktopVersion,
			supportedDesktopVersion,
		)
	}
	if status.BundledClaudeCodeVersion != supportedClaudeCodeVersion {
		return Versions{}, fmt.Errorf(
			"bundled Claude Code version %q is not the verified version %q",
			status.BundledClaudeCodeVersion,
			supportedClaudeCodeVersion,
		)
	}
	if irrlichtVersion == "" {
		return Versions{}, errors.New("Irrlicht version must be available")
	}
	return Versions{
		DesktopApp: status.DesktopVersion,
		ClaudeCode: status.BundledClaudeCodeVersion,
		Irrlicht:   irrlichtVersion,
	}, nil
}

func (runtime *LiveRuntime) CaptureBaseline(ctx context.Context) (Baseline, error) {
	sessions, files, err := runtime.readRegistry()
	if err != nil {
		return Baseline{}, err
	}
	config, err := CaptureTreeSnapshot(runtime.options.ConfigurationRoots)
	if err != nil {
		return Baseline{}, err
	}
	processes, err := runtime.listProcesses(ctx)
	if err != nil {
		return Baseline{}, fmt.Errorf("capture process baseline: %w", err)
	}
	runtime.processBaseline = processes
	ids := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		ids[session.SessionID] = struct{}{}
	}
	return Baseline{SessionIDs: ids, Files: files, Config: config, Processes: processes}, nil
}

func (runtime *LiveRuntime) OpenComposer(ctx context.Context, workspace string) error {
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("requested Desktop workspace is not a directory: %q", workspace)
	}
	values := url.Values{}
	values.Set("folder", workspace)
	deepLink := "claude://code/new?" + values.Encode()
	// The route can reach Desktop before /usr/bin/open reports an error. Mark
	// the ownership window first so deferred cleanup still searches for it.
	runtime.deepLinkOpened = true
	if err := runtime.openDeepLink(ctx, deepLink); err != nil {
		return err
	}
	return nil
}

func openOfficialDesktopURL(ctx context.Context, deepLink string) error {
	command := exec.CommandContext(ctx, "/usr/bin/open", deepLink)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("open official Desktop deep link: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (runtime *LiveRuntime) WaitComposer(ctx context.Context, workspace string) error {
	controls, err := waitForComposerControls(
		ctx, workspace, runtime.helper.inspect, runtime.helper.probe, runtime.helper.click, runtime.RecordStep)
	if err == nil {
		runtime.controls = controls
	}
	return err
}

func waitForComposerControls(
	ctx context.Context,
	workspace string,
	inspect func(context.Context) ([]helperElement, error),
	probe func(context.Context, map[string]helperSelector) error,
	click func(context.Context, helperSelector, helperPostcondition) error,
	recordStep func(string),
) (map[string]helperSelector, error) {
	var controls map[string]helperSelector
	var lastMismatch error
	trusted := false
	err := poll(ctx, "verified Desktop composer controls", func() (bool, error) {
		elements, err := inspect(ctx)
		if err != nil {
			if fatal := transientHelperError(err); fatal != nil {
				return false, fatal
			}
			lastMismatch = err
			return false, nil
		}
		// Claude Desktop holds a modal trust prompt in front of the composer
		// for a workspace it has not seen, and the driver's staging workspace
		// is new on every run. Answer it ONCE: a second prompt during one
		// composer wait is not this run's own scratch folder being trusted, so
		// it is a stop rather than another click.
		if confirm, prompted := trustPromptButton(elements); prompted {
			if trusted {
				return false, fmt.Errorf(
					"Claude Desktop raised a second workspace trust prompt while waiting for the composer for %q; refusing to answer it",
					workspace)
			}
			if err := click(ctx, confirm, helperPostcondition{
				Selector: confirm, Condition: "absent", TimeoutMilliseconds: 10_000,
			}); err != nil {
				// A failed click is never fatal here. Measured against a live
				// 1.46388.4 sheet, one grant produced this in sequence:
				// stale_control (the hit test landed off an animating button),
				// then action_failed (the sheet was dismissing under the
				// click), then control_missing (it was gone). Classifying those
				// codes is guesswork; the next tick simply looks again, and
				// either the prompt is still there — retry — or it is not, and
				// the composer check proceeds. The poll's own deadline is what
				// fails loudly, naming this error as its last observation.
				//
				// The once-only guard stays unspent: it counts confirmed
				// dismissals, and this was not one.
				lastMismatch = fmt.Errorf("answer Desktop workspace trust prompt: %w", err)
				return false, nil
			}
			// Counted only on a CONFIRMED dismissal: "answer at most once" is
			// about grants that actually happened, not attempts.
			trusted = true
			recordStep("trust-workspace-prompt-answered")
			lastMismatch = errors.New("answered the Desktop workspace trust prompt; waiting for the composer")
			return false, nil
		}
		controls, err = composerCatalog(elements, workspace)
		if err != nil {
			lastMismatch = err
			return false, nil
		}
		if err := probe(ctx, controls); err != nil {
			if fatal := transientHelperError(err); fatal != nil {
				return false, fatal
			}
			lastMismatch = err
			return false, nil
		}
		return true, nil
	})
	if err != nil && lastMismatch != nil {
		return controls, fmt.Errorf(
			"%w; last Desktop composer observation for workspace %q: %v",
			err,
			workspace,
			lastMismatch,
		)
	}
	return controls, err
}

func (runtime *LiveRuntime) SetPrompt(ctx context.Context, prompt string) error {
	selector, ok := runtime.controls["prompt"]
	if !ok {
		return errors.New("prompt selector was not verified")
	}
	return runtime.helper.setValue(ctx, selector, prompt)
}

func (runtime *LiveRuntime) Submit(ctx context.Context) error {
	send, ok := runtime.controls["send"]
	if !ok {
		return errors.New("Send selector was not verified")
	}
	stop := helperSelector{Role: "AXButton", Description: "Stop", Hierarchy: send.Hierarchy}
	return runtime.helper.click(ctx, send, helperPostcondition{
		Selector: stop, Condition: "exists", TimeoutMilliseconds: 10_000,
	})
}

// validateArchiveTarget is the final pure ownership guard before any archive
// click. LiveRuntime supplies a fresh registry and accessibility-tree reading.
type archiveTarget struct {
	registry RegistrySession
	menu     helperSelector
	project  helperSelector
}

func (runtime *LiveRuntime) RecordStep(step string) {
	if runtime.stepLog == "" {
		return
	}
	file, err := os.OpenFile(runtime.stepLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = fmt.Fprintf(file, "%s\t%s\n", time.Now().UTC().Format(time.RFC3339Nano), step)
}

func poll(ctx context.Context, name string, observe func() (bool, error)) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		ready, err := observe()
		if err != nil {
			return fmt.Errorf("observe %s: %w", name, err)
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s was not observed before its deadline: %w", name, ctx.Err())
		case <-ticker.C:
		}
	}
}

// axInvalidUIElement is kAXErrorInvalidUIElement. The helper walks the tree
// node by node, so an element that goes away mid-walk — which is what Claude
// Desktop's renderer does while it swaps a view in — fails the whole read with
// this code. It means "look again", not "the composer is wrong": the poll's own
// deadline is what still fails loudly if the tree never settles.
const axInvalidUIElement = "AX error -25202"

func transientHelperError(err error) error {
	message := err.Error()
	if strings.Contains(message, "control_missing") ||
		strings.Contains(message, "app_not_running") ||
		strings.Contains(message, axInvalidUIElement) {
		return nil
	}
	return err
}
