// replay is an offline simulator that takes a Claude Code transcript .jsonl
// file (or a lifecycle-events sidecar) and replays it through the production
// tailer + state classifier using virtual time.
//
// It consolidates the former replay-session and replay-lifecycle tools into a
// single binary with two replay paths:
//
//   - Sidecar-driven (primary): when a lifecycle sidecar is present or passed
//     directly, the replay is driven by the lifecycle recording — fswatcher
//     fires, process events, hook events — for full-fidelity state machine
//     reproduction. A sidecar is found either beside the transcript as a plain
//     events.jsonl (how every recording in replaydata/ stores it) or under the
//     legacy <transcript-stem>.events.jsonl name.
//   - Transcript-only (fallback): when no sidecar exists — or when the one
//     found cannot drive a replay, e.g. a process-owned-store adapter that
//     records no fswatcher fires — events are batched by timestamp and
//     debounced, approximating what the daemon would have seen. The report's
//     sidecar_fallback field says when and why this happened.
//
// Note that the summary's token/cost/model vector always comes from a full
// parse of the transcript, in both modes: it is a property of the file, not of
// when the daemon happened to look. See summaryMetrics in replay_sidecar.go.
//
// Usage:
//
//	go run ./tools/onboarding-factory/cmd/replay [flags] INPUT.jsonl
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
//	--ghosts SIDECAR        Print a human-readable ghost lifecycle timeline from a
//	                        lifecycle sidecar (.events.jsonl) and exit.
//
// The report is a JSON object containing every state transition (with reason,
// virtual timestamp, event index, and a metric snapshot) plus a flicker
// summary. Pipe through `jq` or feed to the bundled visualizer.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/aider"
	"irrlicht/core/adapters/inbound/agents/antigravity"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/copilot"
	"irrlicht/core/adapters/inbound/agents/geminicli"
	"irrlicht/core/adapters/inbound/agents/hermes"
	"irrlicht/core/adapters/inbound/agents/kirocli"
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/inbound/agents/pi"
	"irrlicht/core/adapters/inbound/agents/vibe"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/tailer"
)

// allAgents lists every inbound agent adapter the replay CLI knows about.
// Kept local to main so this package isn't re-importing a shared registry —
// the daemon's production wiring lives in cmd/irrlichd/main.go.
var allAgents = []agent.Agent{
	claudecode.Agent(),
	codex.Agent(),
	pi.Agent(),
	aider.Agent(),
	opencode.Agent(),
	kirocli.Agent(),
	geminicli.Agent(),
	antigravity.Agent(),
	vibe.Agent(),
	copilot.Agent(),
}

// parserFactories is the per-adapter parser map consumed by parserFor()
// below. Built once at package init via agents.Parsers plus explicit
// entries for FilesUnderCWD (aider) and ProcessOwnedStore (opencode)
// adapters — those variants don't carry a parser factory in the new
// shape compatible with agents.ParserFactory.
var parserFactories = func() map[string]agents.ParserFactory {
	m := agents.Parsers(allAgents)
	m[aider.AdapterName] = func() tailer.TranscriptParser { return &aider.Parser{} }
	m[opencode.AdapterName] = func() tailer.TranscriptParser { return &opencode.Parser{} }
	m[hermes.AdapterName] = func() tailer.TranscriptParser { return &hermes.Parser{} }
	return m
}()

// parserFor returns a fresh TranscriptParser for the given adapter name,
// falling back to Claude Code for unknown names (preserves prior behavior).
func parserFor(name string) tailer.TranscriptParser {
	if f, ok := parserFactories[name]; ok {
		return f()
	}
	return &claudecode.Parser{}
}

// detectAdapter infers the adapter name from a transcript path by matching
// either the canonical session-storage root for each supported format or the
// repo-relative replaydata/agents/<adapter>/ fixture layout.
func detectAdapter(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	switch {
	case strings.Contains(abs, "/.claude/projects/"),
		strings.Contains(abs, "/replaydata/agents/claudecode/"):
		return claudecode.AdapterName, nil
	case strings.Contains(abs, "/.codex/sessions/"),
		strings.Contains(abs, "/replaydata/agents/codex/"):
		return codex.AdapterName, nil
	case strings.Contains(abs, "/.pi/agent/sessions/"),
		strings.Contains(abs, "/.pi/sessions/"),
		strings.Contains(abs, "/replaydata/agents/pi/"):
		return pi.AdapterName, nil
	case strings.Contains(abs, "/replaydata/agents/aider/"),
		strings.HasSuffix(abs, "/.aider.chat.history.md"):
		return aider.AdapterName, nil
	case strings.Contains(abs, "/.local/share/opencode/"),
		strings.Contains(abs, "/replaydata/agents/opencode/"):
		return opencode.AdapterName, nil
	case strings.Contains(abs, "/.kiro/sessions/cli/"),
		strings.Contains(abs, "/replaydata/agents/kiro-cli/"):
		return kirocli.AdapterName, nil
	case strings.Contains(abs, "/.gemini/antigravity-cli/"),
		strings.Contains(abs, "/.gemini/antigravity/brain/"),
		strings.Contains(abs, "/replaydata/agents/antigravity/"):
		return antigravity.AdapterName, nil
	case strings.Contains(abs, "/.gemini/tmp/"),
		strings.Contains(abs, "/replaydata/agents/gemini-cli/"):
		return geminicli.AdapterName, nil
	case strings.Contains(abs, "/.vibe/logs/session/"),
		strings.Contains(abs, "/replaydata/agents/mistral-vibe/"):
		return vibe.AdapterName, nil
	case strings.Contains(abs, "/.copilot/session-state/"),
		strings.Contains(abs, "/replaydata/agents/copilot/"):
		return copilot.AdapterName, nil
	// Hermes' store is a single state.db under the Hermes home. The home is
	// relocatable via HERMES_HOME, so the fixture path is the reliable
	// discriminator and the live path matches the default location only.
	case strings.Contains(abs, "/.hermes/state.db"),
		strings.Contains(abs, "/replaydata/agents/hermes/"):
		return hermes.AdapterName, nil
	}
	return "", fmt.Errorf("cannot infer adapter from path %q — pass --adapter claude-code|codex|pi|aider|opencode|kiro-cli|gemini-cli|antigravity|mistral-vibe|copilot|hermes", abs)
}

// eventsSidecarExt is the legacy lifecycle-events sidecar file extension,
// paired with a transcript's .jsonl (e.g. session.jsonl / session.events.jsonl).
const eventsSidecarExt = ".events.jsonl"

// eventsSidecarName is the sidecar's filename in the current recording layout,
// where each recording is its own folder holding transcript.jsonl beside a
// plain events.jsonl. Every one of the sidecars in replaydata/ uses this
// spelling; the legacy extension above survives only for hand-built inputs.
const eventsSidecarName = "events.jsonl"

// cliOptions bundles the parsed CLI flags and positional argument so the
// main helpers can pass a single value around instead of a long arg list.
type cliOptions struct {
	outPath      string
	adapterFlag  string
	sessionFlag  string
	debounceFlag time.Duration
	flickerMax   time.Duration
	quiet        bool
	ghostsPath   string
	src          string
}

func main() {
	opts := parseFlags()
	if opts.ghostsPath != "" {
		if err := runGhostDump(opts.ghostsPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	transcriptPath, sidecarPath, useSidecar := resolveInputPaths(opts.src)
	if !useSidecar && opts.sessionFlag != "" {
		fmt.Fprintln(os.Stderr, "--session requires a sidecar (.events.jsonl); no sidecar found")
		os.Exit(2)
	}
	cfg := buildReportSettings(opts, transcriptPath)
	report, err := runReplay(transcriptPath, sidecarPath, useSidecar, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// --session names a session inside the sidecar, so a fallback to
	// transcript-only silently replays the whole transcript instead of the one
	// session that was asked for. That is a wrong answer, not a degraded one,
	// so it stays an error — matching the pre-flight check above, which can
	// only test whether a sidecar exists, not whether it can drive a replay.
	if report.SidecarFallback != "" && opts.sessionFlag != "" {
		fmt.Fprintf(os.Stderr, "--session %s: sidecar cannot drive a replay (%s), so the filter cannot be applied\n",
			opts.sessionFlag, report.SidecarFallback)
		os.Exit(2)
	}
	// The fallback note belongs to the CLI, not to runReplay: the byte-identity
	// test drives runReplay directly for ~380 recordings, and printing there
	// buried the CI log under 56 lines of notice.
	if report.SidecarFallback != "" && !opts.quiet {
		fmt.Fprintf(os.Stderr, "note: replaying transcript-only — %s: %s\n", report.SidecarFallback, sidecarPath)
	}
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
	flag.StringVar(&opts.ghostsPath, "ghosts", "", "print a human-readable ghost lifecycle timeline from a lifecycle sidecar (.events.jsonl) and exit")
	flag.Parse()
	// --ghosts is a self-contained text mode driven by its own sidecar path; it
	// takes no positional transcript argument.
	if opts.ghostsPath != "" {
		return opts
	}
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
// directly; otherwise a sidecar is auto-detected next to the transcript,
// under the legacy per-transcript name first and then the recording-folder
// name.
//
// The second probe is the fix for issue #1326. Only the legacy spelling was
// tried, so os.Stat("<recording>/transcript.events.jsonl") failed for every
// one of the recordings in replaydata/ — where the sidecar is always a plain
// events.jsonl beside the transcript — and useSidecar stayed false for the
// whole catalog. Both fixture drivers (tools/replay-fixtures.sh and
// TestFixtureReplayByteIdentity) call through here, so the sidecar replay
// path, applyHookEvent included, had no fixture coverage at all: the hook
// mapping could drift (it did, #1320) without moving a single golden.
//
// The probe deliberately stays within the transcript's own directory rather
// than walking up. A subagent transcript (regressions/*/subagents/agent-*.jsonl)
// sits one level away from its parent recording's sidecar, and that sidecar
// describes the parent session — pairing them would replay a subagent against
// another session's fs events.
func resolveInputPaths(src string) (transcriptPath, sidecarPath string, useSidecar bool) {
	if strings.HasSuffix(src, eventsSidecarExt) {
		return strings.TrimSuffix(src, eventsSidecarExt) + ".jsonl", src, true
	}
	transcriptPath = src
	candidates := []string{strings.TrimSuffix(src, ".jsonl") + eventsSidecarExt}
	// A sidecar named as the transcript would pair with itself. Compare
	// basenames rather than whole paths: filepath.Join cleans its result, so a
	// raw `candidate == src` test passes any non-canonical spelling of the same
	// file straight through — `./events.jsonl`, `dir//events.jsonl` and
	// `dir/./events.jsonl` all reach here from shell completion or a script,
	// and each one used to replay the sidecar against itself and then report an
	// authoritative-looking extended check over the result.
	if filepath.Base(src) != eventsSidecarName {
		candidates = append(candidates, filepath.Join(filepath.Dir(src), eventsSidecarName))
	}
	for _, candidate := range candidates {
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

// runReplay dispatches to the sidecar-driven or transcript-only replay and,
// when a sidecar is present, attaches the extended-check diff. Returned to
// callers so both main() and the byte-identity test share one dispatch.
//
// A sidecar that exists but cannot drive a replay (a *notDrivableError — the
// process-owned-store adapters, which have no watched transcript and so record
// no fswatcher fires) degrades to transcript-only rather than failing, and says
// so in the report's SidecarFallback field. Any other sidecar error is fatal:
// silently replaying a weaker mode is how the gap in issue #1326 stayed
// invisible in the first place.
func runReplay(transcriptPath, sidecarPath string, useSidecar bool, cfg reportSettings) (*replayReport, error) {
	report, sidecarDrove, err := dispatchReplay(transcriptPath, sidecarPath, useSidecar, cfg)
	if err != nil {
		return nil, fmt.Errorf("replay failed: %w", err)
	}
	if !sidecarDrove {
		// Transcript-only, so there are no recorded transitions to diff against.
		return report, nil
	}
	check, err := runExtendedCheck(sidecarPath, report.Transitions)
	if err != nil {
		return nil, fmt.Errorf("extended check failed: %w", err)
	}
	report.ExtendedCheck = check
	return report, nil
}

// dispatchReplay picks the replay mode, applies the not-drivable fallback, and
// reports whether the sidecar actually drove the replay. It owns
// SidecarFallback so the field and the returned flag cannot disagree.
func dispatchReplay(transcriptPath, sidecarPath string, useSidecar bool, cfg reportSettings) (report *replayReport, sidecarDrove bool, err error) {
	if !useSidecar {
		report, err = replay(transcriptPath, cfg)
		return report, false, err
	}
	report, err = replayWithSidecar(transcriptPath, sidecarPath, cfg)
	var notDrivable *notDrivableError
	if !errors.As(err, &notDrivable) {
		return report, err == nil, err
	}
	report, err = replay(transcriptPath, cfg)
	if err == nil {
		report.SidecarFallback = notDrivable.Reason
	}
	return report, false, err
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
		fmt.Fprintf(os.Stderr, " [extended-check: kinds %s ordered %d/%d %s %s]",
			kindsMark, c.OrderedMatches, c.RecordedCount, orderMark, driftSummary(c.TimeDeltas))
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
