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
// Rejected, and settled — do not revisit: a mux-level middleware. It would have
// to key on a top-level "transcript_path" JSON field and would FAIL OPEN for
// any future payload that nests or renames it, finding nothing and confining
// nothing while registerHookRoutes still looked guarded. An explicit call that
// fails closed is strictly better than an implicit one that fails open.
package hookjson

import (
	"encoding/json"
	"net/http"

	"irrlicht/core/ports/outbound"
)

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
// Deliberately NOT handled here: the consent gates. Both the "hooks" gate and
// the receipt observation must happen before this call (a body we then refuse
// still proves the channel is alive, #1368), and the adapters that additionally
// gate "transcripts" check it before calling in. Folding consent into the
// decode would make this function the place four different obligations are
// re-decided per adapter, which is the shape #1364's IgnoreUnknownEvent
// deliberately avoids by taking no ResponseWriter.
func DecodeConfined[T any](
	w http.ResponseWriter,
	r *http.Request,
	log outbound.Logger,
	component string,
	c *PathConfiner,
	p *T,
	get func(*T) string,
	set func(*T, string),
) bool {
	// Fail closed on a receiver wired without a confiner. Returning "not
	// decoded" drops the payload with a 2xx, the same shape every other refusal
	// takes, rather than dispatching an unconfined path because the guard
	// itself was missing.
	if c == nil {
		log.LogError(component, "", "hook body dropped: receiver has no path confiner")
		w.WriteHeader(http.StatusOK)
		return false
	}

	if err := json.NewDecoder(r.Body).Decode(p); err != nil {
		http.Error(w, "bad request: invalid JSON", http.StatusBadRequest)
		return false
	}

	raw := get(p)
	confined, reason := c.Confine(raw)
	if reason != RejectNone {
		RejectPath(w, log, component, raw, reason)
		return false
	}
	set(p, confined)
	return true
}
