package processlifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"irrlicht/core/internal/costreport"
)

// probecost_darwin_test.go is the generator #1572 asks for: run it and it prints
// the pasteable block of figures this package's doc comments quote.
//
//	IRRLICHT_MEASURE_PROBE_COSTS=1 go test \
//	    ./core/adapters/inbound/agents/processlifecycle/ \
//	    -run TestMeasureProbeCosts -v
//
// It is an explicitly-skipped test rather than a `-tags measure` file, and that
// is a decision rather than a shortcut. Behind a build tag the generator is not
// compiled by any gate, so it rots exactly like the figures it exists to
// refresh — and, worse, the REFUSAL it owes would rot with it, since nothing
// would type-check either. As an ordinary test file it is compiled, and its plan
// tripwire and its refusal proof are GRADED on every `go test ./core/...`, at
// the cost of one skipped test; only the shellouts are opt-in. test.yml's
// go-test job is pinned to macOS, so both run in CI rather than only here.
//
// Knobs, all read once in TestMeasureProbeCosts:
//
//	IRRLICHT_MEASURE_PROBE_COSTS=1     run at all (otherwise skipped)
//	IRRLICHT_MEASURE_PROBE_SAMPLES=N   override every plan's n (see probeCostPlan.samples)
//	IRRLICHT_MEASURE_TMUX_SOCKET=path  opt-in: measure tmux against THIS socket
//
// The tmux knob is opt-in and has no default on purpose. `tmux -S <path>
// list-clients` against a path with no server STARTS one, so a generator that
// picked a socket for you would be a measurement with a side effect on a machine
// that may be running someone's session. Refusing is the honest default, and it
// is one of the refusals this generator is graded on.

const (
	measureProbeCostsEnv   = "IRRLICHT_MEASURE_PROBE_COSTS"
	measureProbeSamplesEnv = "IRRLICHT_MEASURE_PROBE_SAMPLES"
	measureTmuxSocketEnv   = "IRRLICHT_MEASURE_TMUX_SOCKET"

	// cheapProbeSamples is #1544's n, kept for the probes that cost single-digit
	// milliseconds so a regenerated figure is comparable with the one it
	// replaces rather than differing for a reason nobody can separate from the
	// machine.
	cheapProbeSamples = 300
	// scanProbeSamples is for the probes that scan the whole process table —
	// `lsof <path>` with no -p costs ~0.4s a call on this machine, so 300 of
	// them is two minutes for one row. #1572's stated value is that a figure is
	// producible "in a minute"; a default nobody waits for is a generator nobody
	// runs. n is printed in every row, so a smaller one is disclosed rather than
	// hidden, and IRRLICHT_MEASURE_PROBE_SAMPLES forces any n an author wants.
	scanProbeSamples = 20
	// probeWarmupCalls are discarded before sampling. dyld's shared-cache work
	// and the first page-in of a binary land on call one, and every figure in
	// this package's doc comments says "warm".
	probeWarmupCalls = 5
)

// probeCostPlan is how ONE probeKind is measured, or the named reason this
// machine cannot measure it.
//
// The two are one type rather than two lists because they must be enumerated
// together: a kind that quietly disappears from the report is the failure #1572
// is about, wearing the shape of a shorter block.
type probeCostPlan struct {
	kind probeKind
	// note records WHAT was probed. A per-call figure without its subject is
	// half a measurement — `lsof` against a file nobody holds and `lsof` against
	// a busy transcript are the same exec and not the same cost.
	note string
	// call performs ONE call and reports whether the CHILD ANSWERED. It is nil
	// exactly when unavailable is set.
	call func(ctx context.Context) bool
	// samples is this plan's n. Per-plan rather than global because these probes
	// differ by two orders of magnitude in cost.
	samples int
	// unavailable is why this kind carries no figure on this machine. Non-empty
	// exactly when call is nil.
	unavailable string
}

// probeCostPlans returns one plan per declared probeKind.
//
// Every unavailable reason here is CHECKED rather than asserted — an absent
// environment variable, an absent socket path — because "this machine has no
// kitty" is precisely the kind of dismissal AGENTS.md requires evidence for, and
// a generator that guessed would print the wrong refusal on the machine that
// could have answered.
func probeCostPlans(tb testing.TB) []probeCostPlan {
	tb.Helper()
	self := os.Getpid()

	// A file this process holds open, so lsof.writer measures a hit rather than
	// the (differently shaped) miss. Under t.TempDir(), removed by the harness.
	writable := filepath.Join(tb.TempDir(), "held-open.jsonl")
	fh, err := os.Create(writable)
	if err != nil {
		tb.Fatalf("create the file lsof.writer is measured against: %v", err)
	}
	tb.Cleanup(func() { _ = fh.Close() })

	// A synthetic herdr session directory: herdrClientPIDs stats the client log
	// before probing, so without the file it returns before starting a child and
	// the row would report zero samples for a reason unrelated to lsof.
	herdrDir := tb.TempDir()
	if err := os.WriteFile(filepath.Join(herdrDir, herdrClientLogName), []byte("synthetic\n"), 0o600); err != nil {
		tb.Fatalf("create the synthetic herdr client log: %v", err)
	}
	herdrSocket := filepath.Join(herdrDir, "herdr.sock")

	plans := []probeCostPlan{
		{
			kind:    probePSProcInfo,
			note:    "ps -o ppid=,comm= -p <self>",
			samples: cheapProbeSamples,
			call: func(ctx context.Context) bool {
				_, _, err := readProcInfo(ctx, self)
				return err == nil
			},
		},
		{
			kind:    probePSTTY,
			note:    "ps -o tty= -p <self>",
			samples: cheapProbeSamples,
			call: func(ctx context.Context) bool {
				_, probed := processTTY(ctx, self)
				return probed
			},
		},
		{
			kind:    probePlutilBundleID,
			note:    "plutil -extract CFBundleIdentifier on /System/Library/CoreServices/Finder.app",
			samples: cheapProbeSamples,
			call: func(ctx context.Context) bool {
				// bundleIDVia, never bundleIDForAppPath: the latter is memoized
				// (#1544), so sample two onwards would measure a map lookup and
				// the row would report a figure for something that starts no
				// child. That is the same "a memo now also hides how often the
				// probe runs" hazard probecount.go's memoHits exists for.
				_, err := bundleIDVia(ctx, "/System/Library/CoreServices/Finder.app", plutilBundleIDCmd)
				return err == nil
			},
		},
		{
			kind:    probeLsofCWD,
			note:    "lsof -a -p <self> -d cwd -Fn",
			samples: scanProbeSamples,
			call: func(context.Context) bool {
				_, err := newObserver().CWDOf(self)
				return err == nil
			},
		},
		{
			kind:    probeLsofWriter,
			note:    "lsof <a file this process holds open>",
			samples: scanProbeSamples,
			call: func(context.Context) bool {
				_, err := newObserver().WriterOf(writable)
				return err == nil
			},
		},
		{
			kind:    probeLsofHerdrClients,
			note:    "lsof <synthetic herdr-client.log, no clients attached>",
			samples: scanProbeSamples,
			call: func(ctx context.Context) bool {
				_, probed := herdrClientPIDs(ctx, herdrSocket)
				return probed
			},
		},
		{
			kind:    probePgrepDiscover,
			note:    "pgrep -x launchd",
			samples: scanProbeSamples,
			call: func(context.Context) bool {
				_, err := runPgrep("-x", "launchd")
				return err == nil
			},
		},
	}

	// kitten.window needs a live kitty remote-control socket.
	// kittyWindowIDForPIDVia returns ("", true) without starting a child when the
	// socket is empty, so measuring it without one would report a figure for a
	// function that never execs — the 0.0ms failure with extra steps.
	if socket := os.Getenv("KITTY_LISTEN_ON"); socket != "" {
		plans = append(plans, probeCostPlan{
			kind:    probeKittenWindow,
			note:    "kitten @ --to $KITTY_LISTEN_ON ls",
			samples: scanProbeSamples,
			call: func(ctx context.Context) bool {
				_, probed := kittyWindowIDForPID(ctx, socket, self)
				return probed
			},
		})
	} else {
		plans = append(plans, probeCostPlan{
			kind:        probeKittenWindow,
			unavailable: "no KITTY_LISTEN_ON in this environment, so there is no kitty remote-control socket to ask",
		})
	}

	// tmux.list_clients is opt-in for the side-effect reason in this file's
	// header, and the named socket is STATTED rather than trusted: pointing the
	// knob at a path with no server would start one.
	switch socket := os.Getenv(measureTmuxSocketEnv); {
	case socket == "":
		plans = append(plans, probeCostPlan{
			kind: probeTmuxClients,
			unavailable: measureTmuxSocketEnv + " is unset — measuring this starts a tmux server when the socket has none, " +
				"so it is opt-in rather than defaulted",
		})
	case !tmuxSocketExists(socket):
		plans = append(plans, probeCostPlan{
			kind:        probeTmuxClients,
			unavailable: fmt.Sprintf("%s=%q does not exist, and asking would START a server there", measureTmuxSocketEnv, socket),
		})
	default:
		plans = append(plans, probeCostPlan{
			kind:    probeTmuxClients,
			note:    "tmux -S " + socket + " list-clients",
			samples: scanProbeSamples,
			call: func(ctx context.Context) bool {
				_, probed := tmuxClientPIDs(ctx, socket)
				return probed
			},
		})
	}
	return plans
}

func tmuxSocketExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestEveryProbeKindHasACostPlan is the tripwire that keeps this generator
// covered by EXISTING rather than by someone remembering. It runs on every
// `go test ./core/...`, unskipped — the measurement is opt-in, the coverage
// obligation is not.
//
// A tenth probeKind added to probecount.go with no plan here fails the build,
// which is the same reason TestEveryProbeSiteDeclaresItsOwnKind exists one file
// over: a kind absent from a report is indistinguishable from a kind that costs
// nothing.
func TestEveryProbeKindHasACostPlan(t *testing.T) {
	plans := probeCostPlans(t)

	planned := map[probeKind]int{}
	for _, p := range plans {
		planned[p.kind]++
		if (p.call == nil) != (p.unavailable != "") {
			t.Errorf("%s: a plan must either measure or say why it cannot, never both and never neither "+
				"(call!=nil: %v, unavailable: %q)", p.kind, p.call != nil, p.unavailable)
		}
		if p.call == nil {
			continue
		}
		if p.note == "" {
			t.Errorf("%s: a measurable plan carries no note, so its figure would arrive without its subject", p.kind)
		}
		if p.samples <= 0 {
			t.Errorf("%s: a measurable plan declares n=%d — a plan that samples nothing reports a refusal for a "+
				"reason unrelated to the machine", p.kind, p.samples)
		}
	}
	for _, kind := range allProbeKinds {
		switch planned[kind] {
		case 1:
		case 0:
			t.Errorf("probeKind %q starts a child process and has no cost plan — add one to probeCostPlans, "+
				"or the generator's block will be silently short by one row", kind)
		default:
			t.Errorf("probeKind %q has %d plans; one row per kind, or the block reports the same question twice",
				kind, planned[kind])
		}
	}
	declared := map[probeKind]bool{}
	for _, kind := range allProbeKinds {
		declared[kind] = true
	}
	for kind := range planned {
		if !declared[kind] {
			t.Errorf("cost plan names %q, which allProbeKinds does not declare — the plan has outlived its probe", kind)
		}
	}
}

// TestMeasureOneProbeRefusesWhenTheProbeBinaryIsMissing is the refusal #1572
// names first, driven end to end rather than described: a generator that cannot
// find its binary must SAY SO, naming what it could not do, instead of printing
// a figure.
//
// It execs a path that does not exist rather than stubbing the answer, because
// the two failure modes this must survive are "the loop collected nothing" and
// "the loop collected the duration of the failure" — and only a real
// never-answering child exercises the second. A row that reported ~0.5ms here
// would be the exact shape of the defect: the cost of a fork that failed,
// rendered as the cost of the probe.
//
// Unskipped, so the refusal is graded on every run while the measurement is
// opt-in. It starts no successful child and takes microseconds.
func TestMeasureOneProbeRefusesWhenTheProbeBinaryIsMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "irrlicht-1572-no-such-probe-binary")
	if _, err := os.Stat(missing); err == nil {
		t.Fatalf("%s exists, so this case cannot reproduce a missing binary", missing)
	}

	t.Run("a binary that is not there", func(t *testing.T) {
		row := measureOneProbe(t, probeCostPlan{
			kind:    probePlutilBundleID,
			note:    "a path that does not exist",
			samples: 3,
			call: func(ctx context.Context) bool {
				return exec.CommandContext(ctx, missing).Run() == nil
			},
		})
		line := row.Render(20)
		if !strings.Contains(line, costreport.Marker) {
			t.Fatalf("a probe whose binary is missing produced a figure instead of a refusal: %q", line)
		}
		for _, want := range []string{"3 call(s) failed to answer", "not the probe's cost"} {
			if !strings.Contains(line, want) {
				t.Errorf("the refusal does not say what it could not do (missing %q): %q", want, line)
			}
		}
		if strings.Contains(line, "median") || strings.Contains(line, "n=") {
			t.Errorf("the refusal carries figure-shaped text a reader could quote: %q", line)
		}
	})

	t.Run("the vacuity guard: a probe that DOES answer still produces a figure", func(t *testing.T) {
		// Without this, every assertion above is satisfied by a measureOneProbe
		// that refuses unconditionally.
		row := measureOneProbe(t, probeCostPlan{
			kind:    probePSTTY,
			note:    "a call that answers without a child",
			samples: 3,
			call:    func(context.Context) bool { time.Sleep(time.Millisecond); return true },
		})
		line := row.Render(20)
		if strings.Contains(line, costreport.Marker) {
			t.Fatalf("a probe that answered every call was reported unmeasurable: %q", line)
		}
		if !strings.Contains(line, "n=3") {
			t.Errorf("the figure does not carry its sample count: %q", line)
		}
	})

	t.Run("a partial answer keeps the figure and discloses the discards", func(t *testing.T) {
		// The middle case, which neither of the two above reaches: some calls
		// answered. The figure is real and the discards must be visible, or a
		// row taken over 1 of 300 calls reads exactly like one taken over 300.
		n := 0
		row := measureOneProbe(t, probeCostPlan{
			kind:    probeLsofCWD,
			note:    "one call in three answers",
			samples: 3,
			call: func(context.Context) bool {
				n++
				time.Sleep(time.Millisecond)
				return n%3 == 0
			},
		})
		line := row.Render(20)
		if strings.Contains(line, costreport.Marker) {
			t.Fatalf("a probe that answered once in three was reported unmeasurable: %q", line)
		}
		if !strings.Contains(line, "2 of 3 calls did not answer") {
			t.Errorf("the row hides how much of its sample it discarded: %q", line)
		}
	})
}

// TestMeasureProbeCosts is the generator. Skipped unless asked for.
func TestMeasureProbeCosts(t *testing.T) {
	if os.Getenv(measureProbeCostsEnv) == "" {
		t.Skipf("set %s=1 to regenerate the per-probe cost figures this package's doc comments quote "+
			"(see costFigureAnchors in probecost_test.go for which ones)", measureProbeCostsEnv)
	}
	override := 0
	if raw := os.Getenv(measureProbeSamplesEnv); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			t.Fatalf("%s=%q is not a positive integer — refusing rather than silently measuring a default, "+
				"because a figure taken at an n nobody chose is the drift this generator exists to stop",
				measureProbeSamplesEnv, raw)
		}
		override = n
	}

	plans := probeCostPlans(t)
	started := time.Now()
	rows := make([]costreport.Row, 0, len(plans))
	for _, p := range plans {
		if p.call == nil {
			rows = append(rows, costreport.Refused(string(p.kind), p.unavailable))
			continue
		}
		if override > 0 {
			p.samples = override
		}
		rows = append(rows, measureOneProbe(t, p))
	}
	elapsed := time.Since(started)

	block, err := costreport.Render(
		"processlifecycle probe costs — regenerated by TestMeasureProbeCosts (#1572), warm",
		[]string{
			fmt.Sprintf("%s/%s, %d CPU, %s; whole run %s",
				runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.Version(), elapsed.Round(time.Millisecond)),
			"min is the machine's floor and is the figure to compare across machines: median and p99 move with",
			"whatever else is running, which is why nothing here is a threshold (#1572).",
		},
		rows,
	)
	if err != nil {
		// The measurement broke rather than the machine being fast. Fail rather
		// than print — a block that reached nothing must never be pasteable.
		t.Fatalf("%v", err)
	}
	t.Logf("\n\n%s\n", block)
}

// measureOneProbe runs one plan and turns it into a row.
//
// A call that did not ANSWER contributes no sample. That is the same admission
// rule ancestryReads and bundleIDMemo state — memoize answers, never a
// non-answer — applied to a measurement: the duration of a `plutil` killed by
// shelloutTimeout is the duration of the CEILING, and folding it into a median
// would report the timeout as the cost of the probe.
func measureOneProbe(tb testing.TB, p probeCostPlan) costreport.Row {
	tb.Helper()
	ctx := context.Background()
	for i := 0; i < probeWarmupCalls; i++ {
		p.call(ctx)
	}
	var (
		ms         []float64
		unanswered int
	)
	for i := 0; i < p.samples; i++ {
		start := time.Now()
		answered := p.call(ctx)
		d := time.Since(start)
		if !answered {
			unanswered++
			continue
		}
		ms = append(ms, float64(d.Nanoseconds())/1e6)
	}
	if len(ms) == 0 {
		return costreport.Refused(string(p.kind), fmt.Sprintf(
			"all %d call(s) failed to answer — a non-answer's duration is the ceiling, not the probe's cost, so this row has nothing to report",
			p.samples))
	}
	note := p.note
	if unanswered > 0 {
		note += fmt.Sprintf("; %d of %d calls did not answer and were discarded", unanswered, p.samples)
	}
	return costreport.Measured(string(p.kind), ms, note)
}
