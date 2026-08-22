// hooks_mutations_test.go is the committed mutation evidence for
// sessionIDShape's exclusion of hermes' messaging-gateway sessions — the
// fixture docs/testing-philosophy.md and AGENTS.md ask for in place of a
// paragraph in a PR body that nothing re-runs (#1773).
//
// TestSessionIDShape_RejectsGatewayMintedIDs and
// TestHookReceiver_GatewaySurfaceApprovalIsQuiet (hooks_test.go) are LOCKS:
// they pin behaviour that must not change, so neither has a "before the fix"
// to run red. What earns them is the opposite proof — widen the regex by
// exactly the one-character-class change that would admit a gateway-minted
// id, and confirm both assertions flip. If they didn't flip, the two tests
// above would be passing for a reason unrelated to hex length, which is
// exactly the failure #1773 diagnosed in the comments this same issue fixes:
// an assertion (or a comment) that looks like it is discriminating on the
// right thing but isn't.
package hermes

import (
	"net/http"
	"regexp"
	"testing"
)

// widenedSessionIDShape is sessionIDShape with the trailing hex run widened
// from exactly 6 to 6-or-8 — the change that would let a gateway-minted id
// (`uuid.uuid4().hex[:8]`, gateway/session.py:2831,:3354) through the same
// gate that today only admits a CLI/TUI-minted id
// (`uuid.uuid4().hex[:6]`, agent/agent_init.py:1647, cli.py:5437,
// tui_gateway/server.py:7359).
var widenedSessionIDShape = regexp.MustCompile(`^[0-9]{8}_[0-9]{6}_[0-9a-f]{6,8}$`)

// gatewayMintedSessionID is a well-formed gateway-shaped id: hermes'
// date/time prefix plus 8 hex characters, the shape tools/approval.py:126-133
// forwards for a gateway approval since v0.20.5.
const gatewayMintedSessionID = "20260802_233614_c97a8d12"

// TestSessionIDShapeMutation_WidenedRegexAdmitsGatewayID is (a)'s mutation
// half. It first re-asserts the precondition — the shipped regex rejects the
// gateway mint, which is TestSessionIDShape_RejectsGatewayMintedIDs itself —
// then requires the widened regex to admit the exact same string. A widened
// regex that still rejected it would mean this corpus does not model the
// drift it claims to.
func TestSessionIDShapeMutation_WidenedRegexAdmitsGatewayID(t *testing.T) {
	if sessionIDShape.MatchString(gatewayMintedSessionID) {
		t.Fatalf("precondition failed: the shipped sessionIDShape already accepts %q — "+
			"TestSessionIDShape_RejectsGatewayMintedIDs is not red-able and this mutation "+
			"cannot demonstrate anything", gatewayMintedSessionID)
	}
	if !widenedSessionIDShape.MatchString(gatewayMintedSessionID) {
		t.Fatalf("mutation is inert: widening the hex run to {6,8} still rejects %q, so it "+
			"cannot stand in for the drift this test protects against", gatewayMintedSessionID)
	}
}

// TestHookReceiverMutation_WidenedShapeDispatchesGatewayApproval is (b)'s
// mutation half. It swaps sessionIDShape for the widened regex for the
// duration of this test only (restored via defer — this package runs no
// t.Parallel tests, so no other test observes the swap), replays the exact
// gateway envelope TestHookReceiver_GatewaySurfaceApprovalIsQuiet asserts is
// quiet, and requires it to dispatch instead.
//
// This is the row that proves TestHookReceiver_GatewaySurfaceApprovalIsQuiet
// is actually pinned on hex length and not passing by accident (e.g. because
// the routing key in extra.session_key also fails to match some OTHER way):
// under the widened regex, the top-level gateway id becomes acceptable and
// payload.sessionID() picks it (it checks session_id before
// extra.session_key), so the dispatch count must go from 0 to 1.
func TestHookReceiverMutation_WidenedShapeDispatchesGatewayApproval(t *testing.T) {
	original := sessionIDShape
	sessionIDShape = widenedSessionIDShape
	defer func() { sessionIDShape = original }()

	hermesHome(t)
	h, target := newReceiver(t)

	body := `{"hook_event_name":"` + HookEventPreApprovalRequest +
		`","session_id":"` + gatewayMintedSessionID +
		`","extra":{"session_key":"whatsapp:4915112345678","surface":"gateway"}}`
	rec := post(t, h, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if n := target.totalCalls(); n != 1 {
		t.Fatalf("dispatched %d times under the widened shape, want exactly 1 — if this is not "+
			"1, TestHookReceiver_GatewaySurfaceApprovalIsQuiet's zero-dispatch assertion is not "+
			"actually discriminating on hex length", n)
	}
}
