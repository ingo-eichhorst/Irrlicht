// Package hookbeacon implements `irrlichd hook-post <adapter>` — the hook
// delivery mechanism for agent CLIs that can run a command but cannot POST to a
// URL themselves.
//
// Claude Code installs `type: "http"` entries and the daemon receives them
// directly; that is the shape #1161 moved to precisely so delivery stops
// depending on `curl` being on the agent's PATH. Codex has no HTTP hook type, so
// it still shells out to `curl`. gemini-cli's HookType is {Command, Runtime} and
// Runtime is a Go-style function pointer that is not expressible in JSON, so it
// cannot use HTTP either. This subcommand is what those adapters install instead
// of a `curl` line: a binary irrlicht ships, invoked by absolute path.
//
// # Why exit code 0 is the entire point
//
// The hooks that matter fire BEFORE a tool call and they fail CLOSED. The Phase C
// audit for #1355 live-verified that gemini-cli's BeforeTool and BeforeAgent
// hooks turn an exit code >= 2 with text on stdout into decision "deny", after
// which the scheduler kills the tool with ToolErrorType.POLICY_VIOLATION;
// Copilot's preToolUse has denied on hook error since 1.0.57. A `curl` against a
// daemon that is not running exits 7.
//
// So the naive command does not merely fail to report session state — it breaks
// the user's tool calls whenever irrlicht is not running, which is strictly worse
// than installing no monitoring at all. Post therefore returns 0 on every path,
// by construction rather than by care: daemon down, connection refused, deadline
// exceeded, malformed stdin, no stdin at all, an adapter argument that is not a
// safe path segment, a daemon that answers 500. Every one of those is a row in
// the matrix in beacon_test.go, because "it exits 0" is a claim about the paths
// nobody exercises, and inspection is not evidence for those.
//
// # Why Options has no Stdout
//
// A hook's stdout is a control channel: gemini-cli parses a JSON decision from
// it, and Claude Code reads a JSON decision from it too. The beacon must never
// write there, and the way to guarantee that is to not hold the handle — the
// same structural argument hookjson.IgnoreUnknownEvent makes for not taking an
// http.ResponseWriter. Diagnostics go to stderr, which no CLI reads as a verdict.
//
// # Why the daemon address is resolved here and not at install time
//
// deliver calls daemonaddr.ClientURL in the beacon process, on the tool call that
// fired it. An installed entry therefore carries no port at all, which makes the
// whole #1178 stale-port class impossible rather than fixing it once more: there
// is no port in the config that can go stale, so a dev daemon on :7838, a
// restarted daemon on an ephemeral port and a production daemon on :7837 are all
// reached by the same never-rewritten entry. ClientURL, not LocalURL — the beacon
// is a separate short-lived process spawned by the agent CLI and inherits none of
// the daemon's environment, so it must be allowed to read the address file the
// running daemon published.
package hookbeacon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"irrlicht/core/pkg/daemonaddr"
)

// Subcommand is the argv verb that selects the beacon. It is a positional token
// rather than a flag so that irrlichd's dispatch can key on os.Args[1] exactly,
// instead of scanning the whole command line the way hasFlag does — see
// LegacyGuardToken for why that precision is load-bearing.
const Subcommand = "hook-post"

// endpointPrefix is the route prefix every hook receiver is registered under.
// EndpointPath composes it with the adapter segment rather than each adapter
// exporting a second constant; TestEndpointPathMatchesInstalledReceivers pins it
// against the two receivers that exist so the convention cannot drift silently.
const endpointPrefix = "/api/v1/hooks/"

// DefaultTimeout bounds everything the beacon does — reading stdin, dialing,
// writing the body, reading the response.
//
// One second, because every gemini-cli fire site awaits the hook and the config
// default timeout is 60s: a wedged receiver would stall the user's prompt for a
// full minute. The Phase C audit measured a hook sleeping 5s delaying the
// approval dialog by 5.24s, i.e. the cost is paid in full and in the foreground.
// The daemon is on loopback, so a second is several orders of magnitude more than
// a healthy POST needs and still imperceptible when it is spent in full.
//
// Note for whoever installs this: gemini-cli's config `timeout` is in
// MILLISECONDS, not seconds. Upstream's own `hooks migrate` command gets this
// wrong by 1000x, so a value copied from a Claude Code config reads as 5ms rather
// than 5s and the hook is killed before it can connect.
const DefaultTimeout = time.Second

// maxPayloadBytes caps what the beacon will read from stdin. An over-cap body is
// dropped whole rather than truncated: a truncated JSON object is a body the
// daemon must reject anyway, and sending it would spend the deadline to produce a
// 400 instead of a counted, named local refusal.
const maxPayloadBytes = 1 << 20

// Reason names why a delivery did not happen. It never changes the exit code —
// Post returns 0 for all of these — so it exists to make a refusal observable
// rather than to influence the caller, which is the same trade hookjson's
// RejectReason makes.
type Reason string

const (
	// ReasonNone is the zero value: the payload was delivered and the daemon
	// answered 2xx.
	ReasonNone Reason = ""

	// ReasonMissingAdapter is an invocation with no adapter segment at all.
	ReasonMissingAdapter Reason = "missing_adapter"

	// ReasonInvalidAdapter is an adapter segment that is not a safe single
	// path element — it would otherwise be pasted into a URL path.
	ReasonInvalidAdapter Reason = "invalid_adapter"

	// ReasonUnreadableStdin is a read error on stdin. A partial read counts
	// here too: half a JSON object is not worth a POST.
	ReasonUnreadableStdin Reason = "unreadable_stdin"

	// ReasonEmptyPayload is stdin closed with nothing (or only whitespace) on
	// it, including the case where the CLI attached no stdin at all.
	ReasonEmptyPayload Reason = "empty_payload"

	// ReasonPayloadTooLarge is a body over maxPayloadBytes.
	ReasonPayloadTooLarge Reason = "payload_too_large"

	// ReasonDaemonUnreachable is any transport failure: connection refused
	// because no daemon is running, a dead unix path, DNS, a reset.
	ReasonDaemonUnreachable Reason = "daemon_unreachable"

	// ReasonDeadlineExceeded is the beacon's own deadline firing, whether the
	// stall was stdin or the receiver.
	ReasonDeadlineExceeded Reason = "deadline_exceeded"

	// ReasonPanicked is a panic recovered inside Post. It should never be
	// seen; it is counted so that if it ever is, it is not silent.
	ReasonPanicked Reason = "panicked"

	// ReasonDaemonRejected is a non-2xx response. The daemon is up and heard
	// us, so this is a bug on one side or the other rather than an outage —
	// which is why it is counted apart from ReasonDaemonUnreachable.
	ReasonDaemonRejected Reason = "daemon_rejected"
)

// reasons is every reason in a fixed order, so each has a stable counter slot.
// ReasonNone is deliberately absent — it is not a refusal.
var reasons = [...]Reason{
	ReasonMissingAdapter, ReasonInvalidAdapter, ReasonUnreadableStdin,
	ReasonEmptyPayload, ReasonPayloadTooLarge, ReasonDaemonUnreachable,
	ReasonDeadlineExceeded, ReasonDaemonRejected, ReasonPanicked,
}

// counts holds one counter per reason, indexed by the reasons order — the same
// fixed array of atomics hookjson.PathConfiner and hookjson's splicer fallbacks
// use, for the same reason: instrumentation that serializes exactly when
// refusals arrive together would blunt what it measures.
//
// Package scope rather than per-instance because a beacon process delivers
// exactly once. In that process the counters are barely more than a test seam;
// they earn their place because Post is also callable in-process, and because a
// refusal that is only ever written to a stderr the agent may discard is
// indistinguishable from no refusal.
var counts [len(reasons)]atomic.Uint64

// adapterSegment is the accepted shape of the adapter argument: a lowercase DNS
// -style label. It admits every adapter id in the registry (claudecode, codex,
// gemini-cli, copilot, aider) and admits nothing that changes the meaning of the
// URL path it is pasted into — no "/", no "..", no "%", no empty string, no
// control characters, no whitespace.
var adapterSegment = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// EndpointPath returns the daemon route a given adapter's hook payloads are
// POSTed to.
func EndpointPath(adapter string) string { return endpointPrefix + adapter }

// IsInvocation reports whether args (os.Args[1:]) selects the beacon.
//
// Two accepted forms, both anchored at a fixed position: the bare verb, and the
// verb behind LegacyGuardToken — which is the form Command actually installs,
// because irrlichd releases up to v0.3.2 only ever looked at argv[1] and a
// trailing guard would miss them (see LegacyGuardToken).
//
// Position-anchored rather than scanned. irrlichd's own flag dispatch is a
// permissive whole-command-line scan (hasFlag), and inheriting that here would
// make any daemon invocation that happened to contain the word "hook-post"
// deliver a hook instead of starting — the beacon has to be more precise than
// the chain it is spliced into, not equally loose.
func IsInvocation(args []string) bool {
	_, ok := verbArgs(args)
	return ok
}

// InvocationArgs returns the arguments after the verb, for whichever accepted
// form was used.
func InvocationArgs(args []string) []string {
	rest, _ := verbArgs(args)
	return rest
}

// verbArgs is the single place the accepted verb positions are spelled out, so
// IsInvocation and InvocationArgs cannot drift into disagreeing about what
// counts as a beacon invocation — selectAction asks the first and main() asks
// the second, about the same command line.
func verbArgs(args []string) ([]string, bool) {
	if len(args) > 1 && args[0] == LegacyGuardToken && args[1] == Subcommand {
		return args[2:], true
	}
	if len(args) > 0 && args[0] == Subcommand {
		return args[1:], true
	}
	return nil, false
}

// Options is one beacon invocation.
type Options struct {
	// Args is the argument list AFTER the subcommand verb.
	Args []string

	// Stdin carries the hook payload. A nil Stdin is a refusal, not a panic:
	// an agent CLI is free to invoke a hook with no stdin attached.
	Stdin io.Reader

	// Stderr receives one diagnostic line per refusal. A nil Stderr silences
	// it — the counter still moves.
	Stderr io.Writer

	// Timeout overrides DefaultTimeout. Zero or negative means the default.
	Timeout time.Duration
}

// Post delivers one hook payload and returns the process exit code.
//
// It ALWAYS returns 0. That is not a description of the current implementation,
// it is the contract this package exists to hold — see the package doc for what
// a non-zero exit does to a gemini-cli tool call — and it is why this function
// returns an int at all rather than nothing: an exit code that is a value can be
// asserted, and beacon_test.go asserts it once per failure mode.
//
// Post is the ONE place the exit code and the reporting of a refusal are decided,
// so deliver can return a plain Reason and no code path anywhere else in the
// package has to remember the rule. That is the shape hookjson.RejectPath uses
// for the receiver side of the same problem.
func Post(opts Options) (exitCode int) {
	// A panic would exit 2 and read as "deny" to a fail-closed pre-tool hook,
	// which is the one way "returns 0 by construction" could still fail in
	// production. Recovering is not defensive clutter here: it is the contract.
	// The panic is still counted and reported, so it cannot hide.
	defer func() {
		if r := recover(); r != nil {
			count(ReasonPanicked)
			report(opts.Stderr, ReasonPanicked, fmt.Sprint(r))
			exitCode = 0
		}
	}()

	reason, detail := deliver(opts)
	if reason != ReasonNone {
		count(reason)
		report(opts.Stderr, reason, detail)
	}
	return 0
}

// deliver performs the delivery and returns why it did not happen, plus a short
// human detail for the stderr line. It never decides an exit code.
func deliver(opts Options) (Reason, string) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	// One deadline over the whole operation, taken before stdin is touched.
	// Bounding only the HTTP call would leave the worst stall unbounded: a CLI
	// that opens the pipe and neither writes nor closes it blocks io.ReadAll
	// forever, and the agent is awaiting this process.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	adapter, reason := adapterFrom(opts.Args)
	if reason != ReasonNone {
		// Drain before giving up. Exiting while the CLI is still writing the
		// payload hands it EPIPE on the hook's stdin, and a fail-closed
		// pre-tool hook may read a child-stdin write error as a hook failure —
		// which would defeat the exit code we were careful about. ASSUMED: not
		// verified against a live gemini-cli or Copilot. The drain runs under
		// the same deadline, so it cannot itself become the stall.
		readPayload(ctx, opts.Stdin)
		return reason, strings.Join(opts.Args, " ")
	}

	body, reason, detail := readPayload(ctx, opts.Stdin)
	if reason != ReasonNone {
		return reason, detail
	}

	// Address resolution is deliberately outside ctx: daemonaddr.ClientURL is
	// an os.Lstat plus a 64-byte bounded read of a local file, with no network
	// step, so there is nothing here for a deadline to rescue that a hung
	// filesystem would not also hang the deadline's own goroutine on.
	return send(ctx, daemonaddr.ClientURL(EndpointPath(adapter)), body)
}

// adapterFrom takes the adapter segment out of the post-verb arguments.
//
// It skips any token beginning with "-", which is what makes LegacyGuardToken
// inert on a current binary: the installed command line ends in --version, and
// the beacon has to read straight past it. Skipping rather than rejecting also
// means a future flag can be added to the command line without every previously
// installed entry becoming invalid.
func adapterFrom(args []string) (string, Reason) {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if !adapterSegment.MatchString(arg) {
			return "", ReasonInvalidAdapter
		}
		return arg, ReasonNone
	}
	return "", ReasonMissingAdapter
}

// readPayload reads the whole hook body, bounded by both ctx and
// maxPayloadBytes.
//
// The read happens on its own goroutine so that a stdin that never closes loses
// the race against ctx instead of hanging the process. The goroutine is
// deliberately left running on the timeout path: this is a one-shot process
// about to exit, and the alternative — waiting for a reader that by definition
// is not going to return — is the stall being avoided.
func readPayload(ctx context.Context, stdin io.Reader) ([]byte, Reason, string) {
	if stdin == nil {
		return nil, ReasonEmptyPayload, "no stdin attached"
	}

	type result struct {
		body []byte
		err  error
	}
	// Buffered so the goroutine can always finish even after ctx has won.
	done := make(chan result, 1)
	go func() {
		// This goroutine is outside Post's recover: a panic here would take
		// the process down with exit 2 no matter what Post promises, so it
		// carries its own and reports the panic as an unreadable stdin.
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: fmt.Errorf("panic while reading stdin: %v", r)}
			}
		}()
		// One byte over the cap, so an over-cap body is DETECTED here rather
		// than silently truncated into a body the daemon would have to reject.
		body, err := io.ReadAll(io.LimitReader(stdin, maxPayloadBytes+1))
		done <- result{body: body, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, ReasonDeadlineExceeded, "reading stdin"
	case res := <-done:
		switch {
		case res.err != nil:
			return nil, ReasonUnreadableStdin, res.err.Error()
		case len(res.body) > maxPayloadBytes:
			return nil, ReasonPayloadTooLarge, fmt.Sprintf("%d bytes exceeds the %d byte cap", len(res.body), maxPayloadBytes)
		case len(bytes.TrimSpace(res.body)) == 0:
			return nil, ReasonEmptyPayload, "stdin was empty"
		}
		return res.body, ReasonNone, ""
	}
}

// send POSTs the body verbatim.
//
// Verbatim matters: the beacon does not decode, validate or re-encode the
// payload. The adapter id is stamped by the URL path, so there is nothing to add
// to the body, and re-serializing a JSON document is how fields the daemon has
// not been taught about yet get dropped in transit (#1371). A body that is not
// JSON at all is the daemon's 400 to make, and that 400 is still exit 0 here.
func send(ctx context.Context, url string, body []byte) (Reason, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ReasonDaemonUnreachable, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ReasonDeadlineExceeded, err.Error()
		}
		return ReasonDaemonUnreachable, err.Error()
	}
	defer func() {
		// Drained and closed so the connection can be reused... by nobody, this
		// process is exiting. It is here so a receiver that streams a response
		// does not see a client that vanished mid-write.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxPayloadBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ReasonDaemonRejected, fmt.Sprintf("daemon answered %d", resp.StatusCode)
	}
	return ReasonNone, ""
}

// report writes the single stderr line a refusal gets. Never stdout — see the
// package doc.
func report(stderr io.Writer, reason Reason, detail string) {
	if stderr == nil {
		return
	}
	if detail == "" {
		fmt.Fprintf(stderr, "irrlichd %s: not delivered (%s)\n", Subcommand, reason)
		return
	}
	fmt.Fprintf(stderr, "irrlichd %s: not delivered (%s): %s\n", Subcommand, reason, detail)
}

// count records a refusal.
func count(reason Reason) {
	for i, known := range reasons {
		if known == reason {
			counts[i].Add(1)
			return
		}
	}
}

// Reasons returns a snapshot of the per-reason refusal counts, omitting zeros.
// The map is a copy; mutating it does not touch the counters.
func Reasons() map[Reason]uint64 {
	out := make(map[Reason]uint64, len(reasons))
	for i, reason := range reasons {
		if n := counts[i].Load(); n > 0 {
			out[reason] = n
		}
	}
	return out
}

// ReasonCount returns the total number of undelivered payloads.
func ReasonCount() uint64 {
	var total uint64
	for i := range counts {
		total += counts[i].Load()
	}
	return total
}
