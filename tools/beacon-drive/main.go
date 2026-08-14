// Command beacon-drive produces the session transitions
// docs/beacon-device-test.md Phase 5 asks a tester to produce on demand: a
// waiting edge, a ready one that survives the hold-down, a flap that cancels
// it, four sessions entering waiting inside twenty seconds, a subagent that
// must stay silent, and a daemon that goes away. Driving those with real
// agents is slow for one transition and impractical for the burst.
//
// It dials the relay AS A DAEMON over the real wire protocol
// (docs/relay-protocol.md) and emits synthetic session frames, so every
// scenario travels the entire production path — hub, transition observer,
// core/domain/notify's policy engine, the dispatcher, RFC 8291 encryption,
// and the POST to the subscription's endpoint. Injecting further downstream
// would be easier and would prove nothing about the path this exists to
// exercise. It reuses irrlichd's own relay.Forwarder for the same reason:
// the hello, the daemon_snapshot and the push envelope are the ones the
// daemon sends, not a second implementation that can drift from them.
//
// Two things a synthetic daemon owes the relay it connects to. Its daemon_id
// is obviously synthetic and cannot collide with a real one (the relay keys
// cached state by (workspace, daemon_id, session_id), and a collision would
// overwrite a real daemon's sessions in that cache), and it deletes the
// sessions it created on the way out, so a device test does not leave
// phantom rows in the dashboard the tester is reading.
//
// Two things a run leaves behind on purpose. Dropping the link at the end is
// a real disconnect, so the §6.4 watchdog fires one "beacon-drive
// (synthetic)" disconnected banner a minute later — the same event Phase 5's
// watchdog row is about, and the next run's connect replaces it silently.
// And the relay's persisted daemon roster (§8.6) holds one entry per daemon
// it has ever seen, which is why -state keeps the identity stable: a fresh id
// per run would leave one roster entry, and one such banner per relay
// restart, behind each time.
//
// By default the run also OBSERVES its own effect: it pairs as a device,
// registers a subscription pointing at a listener of its own, and decrypts
// what the relay delivers. Without that a run is unfalsifiable — the pushes
// land on a phone that may not be in the room, and "nothing happened" reads
// exactly like "everything worked". The observed pushes are checked against
// what §8.4 says the scenario must produce, and a mismatch exits non-zero.
//
//	beacon-drive -relay ws://127.0.0.1:7839 -token <client-token> waiting
//
// docs/mobile-notifications-arc42.md §8.4 is the policy table every
// expectation below is read off; tools/beacon-rig.sh drive <scenario> is the
// wrapper that supplies the rig's own relay URL, token and state file.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
)

const defaultRelayURL = "ws://127.0.0.1:7839"

var (
	relayURL = flag.String("relay", envOr("IRRLICHT_RELAY_URL", defaultRelayURL),
		"relay to dial as a daemon (ws:// or wss://; http(s):// is upgraded)")
	token = flag.String("token", os.Getenv("IRRLICHT_RELAY_TOKEN"),
		"bearer token for the relay — the same client token a dashboard uses; required against an --auth relay")
	statePath = flag.String("state", "",
		"file holding this driver's daemon identity and paired device token, so repeat runs reuse one of each (default: a fresh identity per run)")
	burstN = flag.Int("n", 4,
		"burst scenario: how many sessions enter waiting inside the window")
	observe = flag.Bool("observe", true,
		"pair as a device, subscribe a local listener, and check what the relay actually delivered")
)

func main() {
	flag.Usage = usage
	flag.Parse()
	// flag stops at the first non-flag argument, so `beacon-drive burst -n 6`
	// would otherwise leave "-n 6" sitting in Args as junk — and the rig
	// passes its own flags first, which makes that the natural spelling for
	// everything a tester adds. Take the scenario, then parse the rest.
	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	if len(args) > 1 {
		if err := flag.CommandLine.Parse(args[1:]); err != nil || flag.NArg() != 0 {
			usage()
			os.Exit(2)
		}
	}
	sc, ok := scenarios[args[0]]
	if !ok {
		fail("unknown scenario %q — one of: %s", args[0], strings.Join(scenarioNames(), ", "))
	}

	// A signal cancels the scenario but does NOT skip the deferred cleanup in
	// run: an interrupted device test is exactly when phantom rows and a
	// subscription pointing at a dead port are least welcome.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, sc); err != nil {
		fail("%v", err)
	}
}

// run wires the synthetic daemon and the observer, executes one scenario,
// and reports its verdict. Everything it builds is torn down on the way out,
// including on a failure or an interrupt.
func run(ctx context.Context, sc scenario) (err error) {
	st, err := loadState(*statePath)
	if err != nil {
		return err
	}

	e := &env{scenario: sc, burst: *burstN, start: time.Now(), marks: make(map[string]time.Duration)}
	e.daemon = newDaemon(*relayURL, *token, st.DaemonID, st.DaemonLabel)

	if *observe {
		o, oerr := newObserver(*relayURL, *token, st, e.start)
		if oerr != nil {
			return fmt.Errorf("%w\n     (a run that cannot see what the relay delivered verifies nothing;"+
				" pass -observe=false to drive the transitions anyway)", oerr)
		}
		e.observer = o
		defer o.close()
	} else {
		info("observation is off — this run will drive %s's transitions and verify NOTHING", sc.name)
	}
	if err := saveState(*statePath, st); err != nil {
		return err
	}
	// Stamped whatever the run does, and later than the last candidate
	// rather than earlier: it is what the NEXT run waits out, and erring
	// short there is what mis-grades that run (see env.clearBurstWindow).
	defer func() {
		st.LastRun = time.Now().Unix()
		if serr := saveState(*statePath, st); serr != nil && err == nil {
			err = serr
		}
	}()

	info("scenario %s — %s", sc.name, sc.doc)
	info("daemon %s (%q) → %s", e.daemon.id, e.daemon.label, *relayURL)
	defer func() {
		if cerr := e.daemon.cleanup(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	e.clearBurstWindow(ctx, st.LastRun)
	if err := sc.run(ctx, e); err != nil {
		return err
	}
	if e.observer == nil {
		info("drove %s; no observation was configured, so nothing was checked", sc.name)
		return nil
	}
	return e.verdict()
}

// env is what a scenario is handed: the synthetic daemon it drives, the
// observer that grades it, and the knobs it reads. start is the origin of
// every stamp printed or asserted in this run — the observer stamps arrivals
// against it too, so a scenario can measure a hold-down from its own edge.
type env struct {
	scenario scenario
	daemon   *daemon
	observer *observer // nil with -observe=false
	burst    int
	start    time.Time
	marks    map[string]time.Duration
}

// verdict prints every push the relay delivered during the run and reports
// whether the set is the one §8.4 prescribes for this scenario.
func (e *env) verdict() error {
	// One snapshot, split once: reading the two halves separately would let a
	// push that lands between the reads appear in neither.
	got, background := e.observer.partition()
	if len(got) == 0 && len(background) == 0 {
		info("no push was delivered")
	}
	for _, p := range background {
		info("ignored   %s", p)
	}
	for _, p := range got {
		info("delivered %s", p)
	}
	// A delivery that could not be read is a finding of its own, and it must
	// not be graded as a push of some kind: every expectation below reads
	// fields that decoding failed to fill, so it would fail for the wrong
	// reason or, worse, pass because the zero value happened to match.
	for _, p := range got {
		if p.Err != nil {
			return fmt.Errorf("a delivered push could not be read: %w", p.Err)
		}
	}
	if err := e.scenario.want(e, got); err != nil {
		return fmt.Errorf("%s did not produce what §8.4 prescribes: %w", e.scenario.name, err)
	}
	ok("%s produced exactly what §8.4 prescribes", e.scenario.name)
	return nil
}

// --- process-level output, matching tools/beacon-rig.sh's vocabulary ---

func info(format string, a ...any) {
	fmt.Printf("\033[36m==>\033[0m %s\n", fmt.Sprintf(format, a...))
}

func ok(format string, a ...any) {
	fmt.Printf("\033[32m  ok\033[0m %s\n", fmt.Sprintf(format, a...))
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "\033[31mFAIL\033[0m %s\n", fmt.Sprintf(format, a...))
	os.Exit(1)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func scenarioNames() []string {
	names := make([]string, 0, len(scenarios))
	for name := range scenarios {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, "beacon-drive — drive synthetic session transitions through a live relay,\n")
	fmt.Fprintf(out, "as a daemon, for docs/beacon-device-test.md Phase 5.\n\n")
	fmt.Fprintf(out, "usage: beacon-drive [flags] <scenario>\n\nscenarios:\n")
	for _, name := range scenarioNames() {
		sc := scenarios[name]
		fmt.Fprintf(out, "  %-11s %s\n", name, sc.doc)
		fmt.Fprintf(out, "  %-11s takes about %s\n", "", sc.duration.Round(time.Second))
	}
	fmt.Fprintf(out, "\nflags:\n")
	flag.PrintDefaults()
}
