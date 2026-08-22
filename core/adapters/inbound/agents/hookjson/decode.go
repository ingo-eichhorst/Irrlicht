// decode.go holds the one place an inbound hook body may be read (issue #1389).
//
// #1361 made confinement a shared helper and #1380 added
// contracttesting.AssertHookPathConfined so a receiver could not skip it
// silently — but a contract can only fail for an adapter that WIRED it. A
// receiver nobody wired it for is invisible to it, which is exactly how #1361
// shipped its own statusline endpoint unconfined, three lines from the receiver
// it was fixing.
//
// The fix is not another check on the receivers; it is removing the step at
// which one can go wrong. Every receiver does the same two things in the same
// order — decode the body, then confine the transcript path it names — and the
// first is unskippable while the second is optional only because they are two
// calls. DecodeConfined welds them: the confiner is an ARGUMENT to the decode,
// so a receiver cannot reach its payload without having supplied one.
//
// The other half is core/architecture_hookbody_test.go, which fails the build
// if anything under core/adapters/inbound/agents/... reads an inbound request
// body other than this function. That is what turns "the next adapter must
// remember" into "the next adapter cannot forget": the contract polices
// adapters that opted in, the architecture rule polices the ones that did not.
//
// #1488 hangs a second obligation on the same chokepoint, for the same reason
// and by the same route. A receiver's #570 consent had exactly the shape
// confinement had before #1389: assertable once wired, invisible when nobody
// wired it. So the permissions a receiver acts under are now an ARGUMENT to the
// decode too — declared once at NewHandler, published on the handler so a
// contract derives them rather than restating them, and re-checked here so a
// declaration that is never checked drops the payload instead of dispatching.
// See consent.go for what this deliberately does not take over: the ORDER of
// the receiver'"'"'s own gates, which no single call can express.
//
// Rejected, and settled — do not revisit: a mux-level middleware. It would have
// to key on a top-level "transcript_path" JSON field and would FAIL OPEN for
// any future payload that nests or renames it, finding nothing and confining
// nothing while registerHookRoutes still looked guarded. An explicit call that
// fails closed is strictly better than an implicit one that fails open.
package hookjson

import (
	"encoding/json"
	"fmt"
	"net/http"

	"irrlicht/core/ports/outbound"
)

// maxHookBodyBytes bounds an inbound hook envelope. See DecodeConfined.
const maxHookBodyBytes = 1 << 20

// DecodeConfined decodes an inbound hook body into p and confines the
// transcript path that body names, in one step neither half of which can be
// taken alone.
//
// It reports whether the payload survived. On false the response has ALREADY
// been written and the caller must return without dispatching: an undecodable
// body answers 400, and a refused path answers through RejectPath — 2xx, with
// the refusal reported by the log and the confiner's counters rather than by a
// status code on the user's critical path (#1361, #1364).
//
// get returns the path to confine. It is a function rather than a field name
// because the path is not always a field: copilot's Notification envelope
// carries none at all and synthesizes one from a caller-supplied session id
// against the adapter's own root. Synthesized or not, it is caller-influenced
// and goes through the same confiner.
//
// set writes the confined path back into the payload, and is mandatory rather
// than optional on purpose. The confined path being AVAILABLE is not the
// property worth having — every receiver already had that. The property worth
// having is that the caller's raw string is GONE from the payload by the time
// the receiver dispatches, so a downstream line that reaches for
// payload.TranscriptPath out of habit cannot pick the unconfined one back up.
// That is the #1361 defect shape, and leaving set nil would leave the door
// open.
//
// consent names the permissions this receiver acts under, and every one of them
// must be granted before the body is read. This is a BACKSTOP, not the
// receiver's gate: the receiver still runs its own checks, in its own order,
// because that order is load-bearing in two directions this function cannot see
// (the channel key ahead of ObserveHookReceipt so a consent-denied request
// counts no receipt, the transcript key behind it so a hooks-granted /
// transcripts-denied install is not reported dead by #1368's watchdog). What
// the backstop adds is the case those checks cannot cover: a receiver that
// DECLARES a permission and then never checks it. That is #1466 exactly —
// claudecode read transcripts with "transcripts" denied for the whole of its
// life — and it is why the keys travel with the decode rather than only with
// the contract that grades it (issue #1488). It also closes the window where
// consent is revoked between the receiver's check and this read.
//
// A zero Consent declares nothing and is refused, so a receiver cannot reach a
// payload by passing an empty one; NewHandler and RequireConsent between them
// make the honest spelling the only short one, and the keyless spelling does
// not compile at all.
func DecodeConfined[T any](
	w http.ResponseWriter,
	r *http.Request,
	log outbound.Logger,
	component string,
	consent Consent,
	c *PathConfiner,
	p *T,
	get func(*T) string,
	set func(*T, string),
) bool {
	if !consentAllowsRead(w, log, component, consent) {
		return false
	}

	// Fail closed on a receiver wired without a confiner. Returning "not
	// decoded" drops the payload with a 2xx, the same shape every other refusal
	// takes, rather than dispatching an unconfined path because the guard
	// itself was missing.
	//
	// Checked AFTER consent and BEFORE the read, so the three refusals keep the
	// order TestDecodeConfined_RefusesBeforeReadingAnything pins: a denied
	// request costs no decode, no path resolution and no confiner counter.
	if c == nil {
		log.LogError(component, "", "hook body dropped: receiver has no path confiner")
		w.WriteHeader(http.StatusOK)
		return false
	}

	if !readBody(w, r, p) {
		return false
	}

	raw := get(p)
	confined, reason := c.Confine(raw)
	if reason != RejectNone {
		RejectPath(w, log, component, raw, reason)
		return false
	}
	set(p, confined)

	// Postcondition: get and set must name the SAME location, or the payload
	// still holds the caller's raw string while this function reports success.
	//
	// This is not defensive clutter — it is the one failure this design could
	// otherwise not detect, and it was found green by review. Passing behaviour
	// as two closures puts the obligation at each call site, where no checker
	// can see it: a receiver whose set writes a field its get never reads (or a
	// no-op set, which is a one-character edit away) forwards the unconfined
	// path, and every test in the tree still passes — including
	// AssertHookPathConfined, which asserts THAT the receiver dispatched, never
	// WHICH spelling it dispatched. Verifying it here fails closed once, for
	// every present and future receiver, instead of asking four contract
	// wirings to each notice.
	if got := get(p); got != confined {
		log.LogError(component, "", fmt.Sprintf(
			"hook body dropped: payload still names %q after confinement to %q — "+
				"this receiver's get/set pair do not address the same field", got, confined))
		w.WriteHeader(http.StatusOK)
		return false
	}
	return true
}

// DecodeSealed decodes an inbound hook body that names NO filesystem path at
// all, into p.
//
// It reports whether the payload survived, on the same terms as DecodeConfined:
// on false the response has ALREADY been written and the caller must return
// without dispatching.
//
// # Why a second entry point rather than a confiner over an empty string
//
// It exists for issue #1719's opencode receiver, whose Source is an
// agent.ProcessOwnedStore: session state lives in one SQLite database, there is
// no file per session, and the "transcript path" the daemon keys a session on
// is a string the daemon COMPOSES from its own store resolver
// (<db>-wal?session=<id>). There is nothing a caller could name that the daemon
// would open, so the honest payload carries a session id and stops there.
//
// Handing DecodeConfined an empty path instead would not be the same thing. It
// would answer RejectEmptyPath, drop every hook, and log a confinement refusal
// for traffic that is behaving correctly — a mechanism reporting a defect it
// invented. And a confiner over a self-derived path would be worse still: the
// input is our own resolver's output, not a caller's, so resolution could only
// fail (a store not yet created, a WAL checkpointed away between events) and
// each failure would silently drop a legitimate hook to satisfy a guard that
// was protecting nothing.
//
// # What is NOT relaxed
//
// Everything on this path except the confinement is identical to
// DecodeConfined, and deliberately so: the same consent backstop in the same
// place (#1488), the same 1 MiB bound on an unauthenticated local endpoint, and
// the same single body-reading function — readBody, which
// core/architecture_hookbody_test.go pins as the ONE place under
// core/adapters/inbound/agents/... an inbound body may be read. That rule is
// what makes this a second ENTRY POINT rather than a second door: a receiver
// still cannot reach a payload without going through the chokepoint, and a
// receiver that DOES carry a caller-supplied path still cannot reach it without
// supplying a confiner.
//
// The obligation this shifts rather than removes is "prove nothing
// caller-supplied travels", and it is graded from outside by
// contracttesting.AssertHookPathConfined's PathDaemonDerived route: the two
// routes make contradictory assertions about the same two strings, so a
// receiver that declares the wrong one cannot pass quietly.
func DecodeSealed[T any](
	w http.ResponseWriter,
	r *http.Request,
	log outbound.Logger,
	component string,
	consent Consent,
	p *T,
) bool {
	if !consentAllowsRead(w, log, component, consent) {
		return false
	}
	return readBody(w, r, p)
}

// consentAllowsRead is the #1488 backstop both entry points run before the body
// is touched. It reports whether reading may proceed; on false the response has
// already been written.
func consentAllowsRead(w http.ResponseWriter, log outbound.Logger, component string, consent Consent) bool {
	// Fail closed on a receiver that named no permission. Every hook receiver
	// acts under at least one (#570), so an empty declaration is a wiring that
	// went through Consent{} rather than RequireConsent — it cannot be typed
	// away, and it must not read a body.
	if len(consent.keys) == 0 {
		log.LogError(component, "", "hook body dropped: receiver declared no permission to act under")
		w.WriteHeader(http.StatusOK)
		return false
	}

	// A declared permission that is not granted. Answering 2xx is the same
	// choice every other refusal on this path makes: the refusal is reported by
	// the log, never by a status code on the user's critical path (#1361,
	// #1364).
	//
	// This logs where a receiver's OWN gate is silent, and the asymmetry is
	// deliberate rather than an oversight. Reaching here means one of two
	// things, and both are worth a line: the receiver skipped a check it
	// declared, or consent was revoked in the window between its check and this
	// read. The ordinary denied path never reaches here at all — the receiver
	// answers it quietly, which is what keeps a transcripts-denied user from
	// collecting an error per tool call, and what each adapter's
	// consent-drops-quietly test now pins.
	if key, missing := consent.ungranted(); missing {
		log.LogError(component, "", fmt.Sprintf(
			"hook body dropped: permission %q is not granted at the decode — either this "+
				"receiver skipped the check it declares, or consent was revoked mid-request "+
				"(issue #1488)", key))
		w.WriteHeader(http.StatusOK)
		return false
	}
	return true
}

// readBody is the single place an inbound hook body is read.
//
// It is unexported and both entry points funnel through it, which is what keeps
// core/architecture_hookbody_test.go's Arm B — "this package reads a request
// body in exactly ONE function" — true across #1719's second entry point. A
// second body-reading function here would be a second way to skip whatever the
// entry points are enforcing, which is the #1389 hazard restated.
//
// The bound is the reason this is not simply inlined twice. These endpoints are
// unauthenticated and local, so an unbounded decode is a local process's way to
// make the daemon allocate without limit. 1 MiB is far above any real hook
// envelope — the largest field is a truncated last_assistant_message.
func readBody[T any](w http.ResponseWriter, r *http.Request, p *T) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(p); err != nil {
		http.Error(w, "bad request: invalid JSON", http.StatusBadRequest)
		return false
	}
	return true
}
