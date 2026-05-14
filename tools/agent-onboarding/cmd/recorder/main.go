// Command agent-onboard is the multi-tool for #268's agent onboarding
// pipeline. Phase 1 ships the `record` subcommand: wraps an agent
// invocation that's already running and emits time-aligned signals.jsonl
// plus 1-fps text frames into a per-recording output directory.
//
// Phase 1's recorder is "observer-only": it does NOT spawn the agent CLI
// or set up tmux/pipe-pane/script wrappers itself. The maintainer (or
// Phase 6's pipeline/run-pipeline.sh) is responsible for that. The
// recorder takes paths and a PID and runs the sensors against them.
//
// Future subcommands: `survey`, `label`, `synth`, `gen`, `validate`, `viewer`.
package main

import (
	"bufio"
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

	"irrlicht/tools/agent-onboarding/internal/codegen"
	"irrlicht/tools/agent-onboarding/internal/frames"
	"irrlicht/tools/agent-onboarding/internal/groundtruth"
	"irrlicht/tools/agent-onboarding/internal/preflight"
	"irrlicht/tools/agent-onboarding/internal/sensors"
	"irrlicht/tools/agent-onboarding/internal/synth"
	"irrlicht/tools/agent-onboarding/internal/validate"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: agent-onboard <subcommand> [flags]")
		fmt.Fprintln(os.Stderr, "subcommands:")
		fmt.Fprintln(os.Stderr, "  record    capture signals + frames against a running agent")
		fmt.Fprintln(os.Stderr, "  label     convert driver `gt:` sidecar into ground_truth.jsonl")
		fmt.Fprintln(os.Stderr, "  synth     derive ruleset.json + driver_protocol.json from a recording")
		fmt.Fprintln(os.Stderr, "  gen       generate adapter Go files + interactive driver from synth output")
		fmt.Fprintln(os.Stderr, "  validate  compare emitted transitions to ground_truth.jsonl")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "record":
		os.Exit(cmdRecord(os.Args[2:]))
	case "label":
		os.Exit(cmdLabel(os.Args[2:]))
	case "synth":
		os.Exit(cmdSynth(os.Args[2:]))
	case "gen":
		os.Exit(cmdGen(os.Args[2:]))
	case "validate":
		os.Exit(cmdValidate(os.Args[2:]))
	case "-h", "--help":
		fmt.Fprintln(os.Stderr, "usage: agent-onboard <subcommand> [flags]")
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		os.Exit(2)
	}
}

func cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	agent := fs.String("agent", "", "adapter slug — required")
	scenario := fs.String("scenario", "", "scenario id — required")
	eventsPath := fs.String("events", "", "path to events.jsonl — required")
	gtPath := fs.String("ground-truth", "", "path to ground_truth.jsonl — required")
	agentVersion := fs.String("agent-version", "", "recorded into result for staleness")
	adapterVersion := fs.String("adapter-version", "", "recorded into result for staleness")
	outDir := fs.String("out", ".build/agent-onboarding/validate", "directory for the per-scenario verdict JSON")
	coverage := fs.String("coverage", "", "optional path to .specs/agent-scenarios-coverage.json for writeback")
	coverageID := fs.String("coverage-id", "", "canonical scenario id in coverage.json (required with --coverage)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *agent == "" || *scenario == "" || *eventsPath == "" || *gtPath == "" {
		fmt.Fprintln(os.Stderr, "error: --agent, --scenario, --events, --ground-truth are required")
		fs.Usage()
		return 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := validate.Run(ctx, validate.Input{
		Agent: *agent, Scenario: *scenario,
		EventsPath: *eventsPath, GroundTruth: *gtPath,
		AgentVersion: *agentVersion, AdapterVersion: *adapterVersion,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	path, _ := validate.WriteResultJSON(*outDir, result)
	fmt.Fprintf(os.Stderr, "result: %s (wrote %s)\n", result.Result(), path)
	if *coverage != "" && *coverageID != "" {
		err := validate.WriteCoverage(*coverage, *coverageID, *agent, validate.CoverageCell{
			LastTested:     time.Now().UTC(),
			AgentVersion:   *agentVersion,
			AdapterVersion: *adapterVersion,
			Result:         result.Result(),
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "coverage writeback:", err)
			return 1
		}
		fmt.Fprintln(os.Stderr, "coverage writeback OK")
	}
	if !result.Pass {
		return 1
	}
	return 0
}

func cmdGen(args []string) int {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	agent := fs.String("agent", "", "adapter slug — required")
	staging := fs.String("staging", "", "staging dir containing ruleset.json + driver_protocol.json — required")
	adapterOut := fs.String("adapter-out", "", "directory to write adapter Go files — required")
	driverOut := fs.String("driver-out", ".claude/skills/ir:onboard-agent/scripts", "directory to write drive-<agent>-interactive.sh")
	displayName := fs.String("display-name", "", "human-readable agent name; defaults to titlecase of agent")
	processName := fs.String("process-name", "", "OS process name to match; defaults to agent slug")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *agent == "" || *staging == "" || *adapterOut == "" {
		fmt.Fprintln(os.Stderr, "error: --agent, --staging, --adapter-out are required")
		fs.Usage()
		return 2
	}
	if err := codegen.Generate(codegen.Input{
		Agent: *agent, StagingDir: *staging,
		AdapterOutDir: *adapterOut, DriverOutDir: *driverOut,
		DisplayName: *displayName, ProcessName: *processName,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "generated adapter in %s and driver in %s\n", *adapterOut, *driverOut)
	return 0
}

func cmdSynth(args []string) int {
	fs := flag.NewFlagSet("synth", flag.ContinueOnError)
	agent := fs.String("agent", "", "adapter slug — required")
	scenario := fs.String("scenario", "", "scenario id — required")
	signalsPath := fs.String("signals", "", "path to signals.jsonl — required")
	gtPath := fs.String("ground-truth", "", "path to ground_truth.jsonl — required")
	staging := fs.String("staging", ".build/agent-onboarding/staged", "staging dir (per-agent subdir created automatically)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *agent == "" || *scenario == "" || *signalsPath == "" || *gtPath == "" {
		fmt.Fprintln(os.Stderr, "error: --agent, --scenario, --signals, --ground-truth are required")
		fs.Usage()
		return 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stageDir := filepath.Join(*staging, *agent)
	if err := synth.Run(ctx, synth.Input{
		Agent: *agent, Scenario: *scenario,
		SignalsPath: *signalsPath, GroundTruth: *gtPath, StagingDir: stageDir,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "wrote ruleset.json + driver_protocol.json + synthesis_conflicts.json under %s\n", stageDir)
	return 0
}

func cmdLabel(args []string) int {
	fs := flag.NewFlagSet("label", flag.ContinueOnError)
	sidecar := fs.String("sidecar", "", "path to driver sidecar containing `gt:<marker>` lines — required")
	outDir := fs.String("out-dir", "", "scenario directory to write ground_truth.jsonl into — required")
	agent := fs.String("agent", "", "adapter slug — required")
	scenario := fs.String("scenario", "", "scenario id — required")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *sidecar == "" || *outDir == "" || *agent == "" || *scenario == "" {
		fmt.Fprintln(os.Stderr, "error: --sidecar, --out-dir, --agent, --scenario are required")
		fs.Usage()
		return 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path, err := groundtruth.Process(ctx, *sidecar, *outDir, *agent, *scenario, time.Time{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", path)
	return 0
}

// recordCfg bundles every flag for the record subcommand.
type recordCfg struct {
	repoRoot       string
	agent          string
	scenario       string
	outDir         string
	pid            int
	transcript     string
	tmuxTarget     string
	pipepaneFile   string
	ptyFile        string
	watchDir       string
	framesInterval time.Duration
	skipPrereq     bool
}

func cmdRecord(args []string) int {
	var cfg recordCfg
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	fs.StringVar(&cfg.repoRoot, "repo-root", ".", "repo root containing replaydata/ and .agent-onboarding/")
	fs.StringVar(&cfg.agent, "agent", "", "adapter slug (e.g. claudecode) — required")
	fs.StringVar(&cfg.scenario, "scenario", "", "scenario id — required")
	fs.StringVar(&cfg.outDir, "out", "", "output dir — required (will be created)")
	fs.IntVar(&cfg.pid, "pid", 0, "agent root PID (enables proc + net sensors)")
	fs.StringVar(&cfg.transcript, "transcript", "", "transcript file path (enables transcript sensor)")
	fs.StringVar(&cfg.tmuxTarget, "tmux-target", "", "tmux pane target (enables pane sensor + frames)")
	fs.StringVar(&cfg.pipepaneFile, "pipepane-file", "", "file `tmux pipe-pane` is writing to (enables pipepane sensor)")
	fs.StringVar(&cfg.ptyFile, "pty-file", "", "file containing raw PTY stream (enables pty sensor)")
	fs.StringVar(&cfg.watchDir, "watch-dir", "", "directory tree to watch for FS events (enables fs sensor)")
	fs.DurationVar(&cfg.framesInterval, "frames-interval", time.Second, "frame capture cadence")
	fs.BoolVar(&cfg.skipPrereq, "no-prereq-check", false, "skip the prerequisites gate (NOT for production use)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if cfg.agent == "" || cfg.scenario == "" || cfg.outDir == "" {
		fmt.Fprintln(os.Stderr, "error: --agent, --scenario, --out are required")
		fs.Usage()
		return 2
	}
	if err := runRecord(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func runRecord(cfg recordCfg) error {
	// Preflight: prerequisites gate.
	if !cfg.skipPrereq {
		r, err := preflight.Check(cfg.repoRoot, cfg.agent)
		if err != nil {
			if errors.Is(err, preflight.ErrPrereqsNotMet) {
				return fmt.Errorf("%s\n  manifest: %s\n  ok file:  %s",
					r.Detail, r.ManifestPath, r.OKPath)
			}
			return fmt.Errorf("preflight: %w", err)
		}
		fmt.Fprintln(os.Stderr, r.Detail)
	}

	// Prepare output dir.
	if err := os.MkdirAll(cfg.outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir out: %w", err)
	}
	signalsPath := filepath.Join(cfg.outDir, "signals.jsonl")
	sigFile, err := os.Create(signalsPath)
	if err != nil {
		return fmt.Errorf("create signals.jsonl: %w", err)
	}
	defer sigFile.Close()
	sigWriter := bufio.NewWriter(sigFile)
	defer sigWriter.Flush()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SIGINT / SIGTERM → graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "shutting down...")
		cancel()
	}()

	// Build the sensor set from configured inputs. Each sensor is optional;
	// silent if its input flag is empty.
	var sources []<-chan sensors.Signal
	var enabled []string
	add := func(name string, ch <-chan sensors.Signal) {
		sources = append(sources, ch)
		enabled = append(enabled, name)
	}

	if cfg.transcript != "" {
		add("transcript", (&sensors.Transcript{Path: cfg.transcript}).Run(ctx))
	}
	if cfg.tmuxTarget != "" {
		add("pane", (&sensors.Pane{Target: cfg.tmuxTarget}).Run(ctx))
	}
	if cfg.pipepaneFile != "" {
		add("pipepane", (&sensors.PipePane{Path: cfg.pipepaneFile}).Run(ctx))
	}
	if cfg.ptyFile != "" {
		add("pty", (&sensors.PTY{Path: cfg.ptyFile}).Run(ctx))
	}
	if cfg.pid > 0 {
		add("proc", (&sensors.Proc{RootPID: cfg.pid}).Run(ctx))
		add("net", (&sensors.Net{RootPID: cfg.pid}).Run(ctx))
	}
	if cfg.watchDir != "" {
		add("fs", (&sensors.FS{Root: cfg.watchDir}).Run(ctx))
	}

	if len(sources) == 0 {
		return errors.New("no sensors enabled — pass at least one of --transcript / --tmux-target / --pipepane-file / --pty-file / --pid / --watch-dir")
	}
	fmt.Fprintf(os.Stderr, "recording with sensors: %s\n", strings.Join(enabled, ", "))

	// Start frame renderer in parallel if we have a tmux target.
	framesDone := make(chan error, 1)
	if cfg.tmuxTarget != "" {
		go func() {
			framesDone <- frames.Run(ctx, cfg.outDir, &frames.TextRenderer{Target: cfg.tmuxTarget}, cfg.framesInterval)
		}()
	} else {
		framesDone <- nil // closed channel-style sentinel
		close(framesDone)
	}

	// Write meta now so a partial recording still has the header.
	meta := recordingMeta{
		Agent:            cfg.agent,
		Scenario:         cfg.scenario,
		StartedAt:        time.Now().UTC(),
		PID:              cfg.pid,
		SensorsEnabled:   enabled,
		FramesIntervalMs: int(cfg.framesInterval.Milliseconds()),
	}
	if err := writeMeta(cfg.outDir, meta); err != nil {
		return err
	}

	// Drain signals into signals.jsonl. Returns when every sensor channel
	// has closed (or ctx is cancelled).
	merged := sensors.Merge(sources...)
	writeErr := sensors.WriteSignals(ctx, sigWriter, merged)

	// On context cancellation WriteSignals returns immediately, but the
	// sensor goroutines feeding `merged` may still be racing to push
	// final values into the 64-element merge buffer. If nobody drains
	// `merged`, those goroutines block on send forever and Merge's
	// wg.Wait()→close(out) never runs. Spawn a drain so every sensor
	// finishes cleanly before we tear down.
	go func() {
		for range merged {
		}
	}()

	// Update meta with end time on shutdown.
	meta.EndedAt = time.Now().UTC()
	if err := writeMeta(cfg.outDir, meta); err != nil {
		// Already wrote the header — don't fail the run for this.
		fmt.Fprintln(os.Stderr, "warning: update meta:", err)
	}

	// Drain the frame renderer's exit so it doesn't outlive us.
	if cfg.tmuxTarget != "" {
		<-framesDone
	}

	if writeErr != nil && !errors.Is(writeErr, context.Canceled) {
		return writeErr
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", signalsPath)
	return nil
}

type recordingMeta struct {
	Agent            string    `json:"agent"`
	Scenario         string    `json:"scenario"`
	StartedAt        time.Time `json:"started_at"`
	EndedAt          time.Time `json:"ended_at,omitzero"`
	PID              int       `json:"pid,omitempty"`
	SensorsEnabled   []string  `json:"sensors_enabled"`
	FramesIntervalMs int       `json:"frames_interval_ms"`
}

func writeMeta(outDir string, m recordingMeta) error {
	path := filepath.Join(outDir, "recording-meta.json")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return err
	}
	return nil
}

