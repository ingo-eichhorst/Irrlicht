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
	options  LiveOptions
	helper   helperClient
	controls map[string]helperSelector
	// workspace is the composer WaitComposer verified. Submit re-resolves
	// against it rather than against a caller-supplied value.
	workspace       string
	processes       map[string]int
	processEvidence map[string]ProcessEvidence
	processBaseline map[int]struct{}
	stepLog         string
	httpClient      *http.Client
	registryByID    map[string]RegistrySession
	workingSeen     map[string]bool
	deepLinkOpened  bool
	openDeepLink    func(context.Context, string) error
	frontDesktop    func(context.Context) error
	// foreignTrustPrompt records that a workspace trust sheet was ALREADY open
	// when CaptureBaseline ran, before this driver opened anything. Only such a
	// prompt is someone else's; one that appears afterwards is the driver's own.
	foreignTrustPrompt bool
	processExists      func(int) (bool, error)
	listProcesses      func(context.Context) (map[int]struct{}, error)
	observeProcess     func(context.Context, int) (string, error)
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
		frontDesktop:   activateDesktop,
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
	// Is a workspace trust sheet already up? The accessibility tree never names
	// the folder it asks about, so this is the only moment the driver can tell
	// a foreign prompt from the one its own deep link is about to raise.
	// An unreadable tree here must not read as "no prompt": that is the
	// permissive answer, so it is an error.
	elements, inspectErr := runtime.helper.inspect(ctx)
	if inspectErr != nil {
		return Baseline{}, fmt.Errorf("capture Desktop trust-prompt baseline: %w", inspectErr)
	}
	_, runtime.foreignTrustPrompt = trustPromptButton(elements)
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

// front brings Claude Desktop forward through the runtime's own seam, so a
// test can observe the call without launching anything.
func (runtime *LiveRuntime) front(ctx context.Context) error {
	if runtime.frontDesktop == nil {
		return nil
	}
	return runtime.frontDesktop(ctx)
}

// activateDesktop brings Claude Desktop to the front.
//
// This is not cosmetic. Measured on 1.46388.4: with Desktop frontmost the
// composer is present continuously from t=9s to t=35s; the moment focus moves
// to another app it leaves the accessibility tree and does not come back on its
// own. Every composer timeout on this branch was that — the wait polling an app
// that had been backgrounded, reporting "control is missing at stable path"
// for a composer that was no longer being exposed. The helper also refuses to
// click unless Desktop is frontmost, so the trust prompt needs this too.
func activateDesktop(ctx context.Context) error {
	command := exec.CommandContext(ctx, "/usr/bin/open", "-a", "Claude")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bring Claude Desktop to the front: %w (%s)", err, strings.TrimSpace(string(output)))
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
		ctx, workspace, runtime.helper.inspect, runtime.helper.probe, runtime.helper.click,
		runtime.RecordStep, runtime.front, runtime.foreignTrustPrompt)
	if err == nil {
		runtime.controls = controls
		runtime.workspace = workspace
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
	activate func(context.Context) error,
	foreignPrompt bool,
) (map[string]helperSelector, error) {
	var controls map[string]helperSelector
	var lastMismatch error
	trusted := false
	err := poll(ctx, "verified Desktop composer controls", func() (bool, error) {
		// Front FIRST, every tick. Desktop stops exposing the composer when it
		// is backgrounded, and anything on the operator's machine can take
		// focus mid-run — an editor reacting to a file write is enough.
		if activateErr := activate(ctx); activateErr != nil {
			// Do NOT overwrite a composer observation with this. At the
			// deadline the step context kills the activation, so the last thing
			// recorded became "bring Claude Desktop to the front: signal:
			// killed" — masking the control mismatch that actually explains the
			// failure. Keep it only when nothing better has been seen.
			if lastMismatch == nil {
				lastMismatch = activateErr
			}
			return false, nil
		}
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
		confirm, prompted := trustPromptButton(elements)
		if prompted {
			if foreignPrompt {
				// A sheet that was already up when this wait began is not ours.
				// The tree does not name the folder it asks about, so clicking
				// it would grant persistent trust to a workspace someone else
				// opened — a grant nothing here restores, VerifyBaseline cannot
				// see, and the evidence does not record. Refuse by name.
				return false, fmt.Errorf(
					"a Claude Desktop workspace trust prompt was already open when this run "+
						"captured its baseline, before it asked for %q; refusing to answer a "+
						"prompt this driver did not raise",
					workspace)
			}
			if trusted {
				return false, fmt.Errorf(
					"Claude Desktop raised a second workspace trust prompt while waiting for the composer for %q; refusing to answer it",
					workspace)
			}
			err := click(ctx, confirm, helperPostcondition{
				Selector: confirm, Condition: "absent", TimeoutMilliseconds: 10_000,
			})
			// The guard counts clicks that may have LANDED, not confirmed
			// dismissals. The helper posts the mouse event and only then
			// verifies its postcondition, so on this side "the click failed"
			// and "the click worked but a second queued sheet kept the button
			// present" are the same error. Counting confirmations under-counts
			// exactly the case this guard exists for, and the driver went on to
			// grant trust to a second, different folder and report success.
			//
			// stale_control is the one provably pre-click failure: the helper's
			// hit test refuses before it posts anything (its own guard against
			// clicking the wrong control), and a live 1.46388.4 sheet produces
			// it while animating in. That one is retried; everything else
			// spends the guard.
			if err != nil && isPreClickRefusal(err) {
				lastMismatch = fmt.Errorf("answer Desktop workspace trust prompt: %w", err)
				return false, nil
			}
			trusted = true
			if err != nil {
				// It may have landed. Stop clicking, keep looking: the prompt
				// either clears and the composer check proceeds, or it does not
				// and the branch above refuses the next one by name.
				lastMismatch = fmt.Errorf("answer Desktop workspace trust prompt: %w", err)
				return false, nil
			}
			recordStep("trust-workspace-prompt-answered")
			lastMismatch = errors.New("answered the Desktop workspace trust prompt; waiting for the composer")
			return false, nil
		}
		controls, err = composerControls(elements, workspace, basicTurnControls())
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

// Submit resolves the send button from a FRESH reading rather than from the
// controls verified before the turn was typed. The send slot only carries the
// "Send" label once the composer holds text; before that the same slot reads
// "Stop" or nothing at all. Resolving it up front made an empty composer look
// like a missing composer.
func (runtime *LiveRuntime) Submit(ctx context.Context) error {
	elements, err := runtime.helper.inspect(ctx)
	if err != nil {
		return err
	}
	controls, err := composerControls(elements, runtime.workspace, []string{"send"})
	if err != nil {
		return fmt.Errorf("resolve the Desktop send button after the prompt was typed: %w", err)
	}
	send := controls["send"]
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

// isPreClickRefusal reports whether a click failed BEFORE the helper posted the
// mouse event, so nothing can have happened in the app. Only stale_control
// qualifies: the helper resolves the control, hit-tests the click point, and
// refuses when the point does not land on it — all before the click.
func isPreClickRefusal(err error) bool {
	return strings.Contains(err.Error(), "stale_control")
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
