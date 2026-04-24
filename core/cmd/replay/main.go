// replay is an offline simulator that takes a Claude Code transcript .jsonl
// file (or a lifecycle-events sidecar) and replays it through the production
// tailer + state classifier using virtual time.
//
// It consolidates the former replay-session and replay-lifecycle tools into a
// single binary with two replay paths:
//
//   - Sidecar-driven (primary): when a .events.jsonl sidecar is present or
//     passed directly, the replay is driven by the lifecycle recording —
//     fswatcher fires, process events, hook events — for full-fidelity state
//     machine reproduction.
//   - Transcript-only (fallback): when no sidecar exists, events are batched
//     by timestamp and debounced, approximating what the daemon would have
//     seen.
//
// Usage:
//
//	go run ./core/cmd/replay [flags] INPUT.jsonl
//
// INPUT can be a transcript .jsonl or a sidecar .events.jsonl.
//
// Flags:
//
//	--out FILE              Write JSON report to FILE (default stdout).
//	--adapter NAME          Adapter name (claude-code, codex, pi); auto-detected from path if omitted.
//	--session ID            Filter sidecar events to a single session (multi-session recordings).
//	--debounce DURATION     Simulated activity debounce window. Default 2s.
//	--flicker-max DURATION  Episodes shorter than this are counted as flickers. Default 10s.
//	--quiet                 Suppress per-event progress on stderr.
//
// The report is a JSON object containing every state transition (with reason,
// virtual timestamp, event index, and a metric snapshot) plus a flicker
// summary. Pipe through `jq` or feed to the bundled visualizer.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/pi"
)

// detectAdapter infers the adapter name from a transcript path by matching
// either the canonical session-storage root for each supported format or the
// repo-relative testdata/replay/<adapter>/ fixture layout.
func detectAdapter(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	switch {
	case strings.Contains(abs, "/.claude/projects/"),
		strings.Contains(abs, "/testdata/replay/claudecode/"):
		return claudecode.AdapterName, nil
	case strings.Contains(abs, "/.codex/sessions/"),
		strings.Contains(abs, "/testdata/replay/codex/"):
		return codex.AdapterName, nil
	case strings.Contains(abs, "/.pi/agent/sessions/"),
		strings.Contains(abs, "/.pi/sessions/"),
		strings.Contains(abs, "/testdata/replay/pi/"):
		return pi.AdapterName, nil
	}
	return "", fmt.Errorf("cannot infer adapter from path %q — pass --adapter claude-code|codex|pi", path)
}

// cliOptions bundles the parsed CLI flags and positional argument so the
// main helpers can pass a single value around instead of a long arg list.
type cliOptions struct {
	outPath      string
	adapterFlag  string
	sessionFlag  string
	debounceFlag time.Duration
	flickerMax   time.Duration
	quiet        bool
	src          string
}

func main() {
	opts := parseFlags()
	transcriptPath, sidecarPath, useSidecar := resolveInputPaths(opts.src)
	cfg := buildReportSettings(opts, transcriptPath)
	report := runReplay(opts, useSidecar, transcriptPath, sidecarPath, cfg)
	emitReport(opts, report)
	if c := report.ExtendedCheck; c != nil {
		if len(c.OrderedMismatches) > 0 || len(c.MissingKinds) > 0 || len(c.ExtraKinds) > 0 {
			os.Exit(1)
		}
	}
}

// parseFlags reads all CLI flags plus the positional transcript/sidecar path
// and exits with usage on a missing or extra argument.
func parseFlags() cliOptions {
	var opts cliOptions
	flag.StringVar(&opts.outPath, "out", "", "output JSON report path (default: stdout)")
	flag.StringVar(&opts.adapterFlag, "adapter", "", "adapter name (claude-code, codex, pi); auto-detected from path if omitted")
	flag.StringVar(&opts.sessionFlag, "session", "", "filter sidecar events to a single session ID")
	flag.DurationVar(&opts.debounceFlag, "debounce", 2*time.Second, "simulated activity debounce window")
	flag.DurationVar(&opts.flickerMax, "flicker-max", 10*time.Second, "episodes shorter than this are counted as flickers (automated agent loops cycle turns in ~25s, so 30s overcounts)")
	flag.BoolVar(&opts.quiet, "quiet", false, "suppress per-event progress on stderr")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: replay [flags] INPUT.jsonl")
		flag.PrintDefaults()
		os.Exit(2)
	}
	opts.src = flag.Arg(0)
	return opts
}

// resolveInputPaths maps the CLI positional argument to a (transcript,
// sidecar, useSidecar) triple. A .events.jsonl argument names the sidecar
// directly; otherwise a sibling sidecar is auto-detected next to the
// transcript.
func resolveInputPaths(src string) (transcriptPath, sidecarPath string, useSidecar bool) {
	if strings.HasSuffix(src, ".events.jsonl") {
		return strings.TrimSuffix(src, ".events.jsonl") + ".jsonl", src, true
	}
	transcriptPath = src
	if candidate := strings.TrimSuffix(src, ".jsonl") + ".events.jsonl"; candidate != src {
		if _, err := os.Stat(candidate); err == nil {
			return transcriptPath, candidate, true
		}
	}
	return transcriptPath, "", false
}

// buildReportSettings resolves the adapter (explicit flag, or auto-detected
// from the transcript path) and folds the user-tunable knobs into the
// settings struct threaded through both replay paths.
func buildReportSettings(opts cliOptions, transcriptPath string) reportSettings {
	adapterName := opts.adapterFlag
	if adapterName == "" {
		var err error
		adapterName, err = detectAdapter(transcriptPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	return reportSettings{
		Adapter:            adapterName,
		SessionFilter:      opts.sessionFlag,
		DebounceWindow:     opts.debounceFlag,
		FlickerMaxDuration: opts.flickerMax,
	}
}

// runReplay dispatches to the sidecar-driven or transcript-only replay,
// runs the extended sidecar check when applicable, and returns the fully
// populated report. Any error terminates the process.
func runReplay(opts cliOptions, useSidecar bool, transcriptPath, sidecarPath string, cfg reportSettings) *replayReport {
	var (
		report    *replayReport
		replayErr error
	)
	if useSidecar {
		report, replayErr = replayWithSidecar(transcriptPath, sidecarPath, cfg)
	} else {
		if opts.sessionFlag != "" {
			fmt.Fprintln(os.Stderr, "--session requires a sidecar (.events.jsonl); no sidecar found")
			os.Exit(2)
		}
		report, replayErr = replay(transcriptPath, cfg)
	}
	if replayErr != nil {
		fmt.Fprintln(os.Stderr, "replay failed:", replayErr)
		os.Exit(1)
	}
	if useSidecar {
		check, err := runExtendedCheck(sidecarPath, report.Transitions)
		if err != nil {
			fmt.Fprintln(os.Stderr, "extended check failed:", err)
			os.Exit(1)
		}
		report.ExtendedCheck = check
	}
	return report
}

// emitReport encodes the report to the chosen destination and prints the
// one-line progress summary to stderr unless --quiet was passed.
func emitReport(opts cliOptions, report *replayReport) {
	enc := json.NewEncoder(chooseOutput(opts.outPath))
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
	if opts.quiet {
		return
	}
	printSummary(report)
}

// printSummary writes the stderr progress line with per-transition counts,
// flicker bucketing, and the extended-check pass/fail markers.
func printSummary(report *replayReport) {
	s := report.Summary
	fmt.Fprintf(os.Stderr,
		"replay: %d events → %d transitions, %d flickers (ww=%d wr=%d rw=%d)",
		s.TotalEvents, s.TotalTransitions, s.FlickerCount,
		s.FlickersByCategory["working_between_waiting"]+s.FlickersByCategory["waiting_between_working"],
		s.FlickersByCategory["working_between_ready"]+s.FlickersByCategory["ready_between_working"],
		s.FlickersByCategory["ready_between_waiting"]+s.FlickersByCategory["waiting_between_ready"])
	if c := report.ExtendedCheck; c != nil {
		kindsMark := "ok"
		if len(c.MissingKinds) > 0 || len(c.ExtraKinds) > 0 {
			kindsMark = "FAIL"
		}
		orderMark := "ok"
		if len(c.OrderedMismatches) > 0 {
			orderMark = "FAIL"
		}
		fmt.Fprintf(os.Stderr, " [extended-check: kinds %s ordered %d/%d %s]",
			kindsMark, c.OrderedMatches, c.RecordedCount, orderMark)
	}
	fmt.Fprintln(os.Stderr)
}

func chooseOutput(path string) *os.File {
	if path == "" {
		return os.Stdout
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create output:", err)
		os.Exit(1)
	}
	return f
}
