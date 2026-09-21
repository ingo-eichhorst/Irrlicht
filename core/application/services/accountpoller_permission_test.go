package services

import (
	"context"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

// TestAccountAPIPermissionKey_Value is a LOCK, not red-first evidence
// (AGENTS.md: "Locks ... pass by construction — say which ones those are
// rather than presenting their green as red-first proof"). It pins the exact
// literal string a future provider ticket (#2007) and any daemon-wide
// projection over it will hardcode, so a rename here is a reviewable diff in
// THIS test rather than a silent drift.
func TestAccountAPIPermissionKey_Value(t *testing.T) {
	if agent.AccountAPIPermissionKey != "account-api" {
		t.Fatalf("agent.AccountAPIPermissionKey = %q, want %q", agent.AccountAPIPermissionKey, "account-api")
	}
}

// testProviderAccountAPIPermission builds the shape a real provider ticket
// is expected to declare (see agent.AccountAPIPermissionKey's doc comment):
// a standalone pseudo-adapter Agent value — never a second Permission on the
// provider's own coding-agent adapter, where declaration.go's own doc
// comment says it would silently never be consulted — with one
// permission.KindObserve permission whose Remove wipes this provider's
// poller state. Apply is a no-op: Poll's own live Granted() check is what
// enforces consent on every call, so there is nothing to start eagerly on
// grant (unlike gastown's watcher, this poller runs on demand, not on a
// background timer — #2003 adds no scheduler; a later ticket that adds
// proactive background polling would give Apply something to start).
func testProviderAccountAPIPermission(poller *AccountPoller, provider string) agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: provider + "-account-api"},
		Permissions: []agent.Permission{{
			Key:    agent.AccountAPIPermissionKey,
			Kind:   permission.KindObserve,
			Apply:  func() error { return nil },
			Remove: func() error { poller.Revoke(provider); return nil },
		}},
	}
}

// TestAccountAPIPermission_GateContract is #2003's contracttesting wiring:
// docs/testing-contracts.md requires a new permission to be driven through
// AssertPermissionGated. This ticket adds no real provider adapter for the
// production registry walks (agents.All() / consentCatalog()) to find, so
// this test exercises the contract directly against the mechanism #2007
// plugs a real provider into — a test-only Agent built by
// testProviderAccountAPIPermission above, standing in for exactly the shape
// that ticket is expected to produce.
//
// It combines both of docs/testing-contracts.md's permission-gating shapes,
// because this permission genuinely has both: a LIVE per-request gate (Poll
// checks Granted() on every call — driven here by a real
// contracttesting.ConsentGate) AND an install-type effect (Remove wipes the
// poller's cache — driven by the same SetState transition, exactly as
// processlifecycle's TestKittyPermission_GateContract drives Apply/Remove
// for its own install-type permission).
func TestAccountAPIPermission_GateContract(t *testing.T) {
	const provider = "test-provider"
	poller := NewAccountPoller()
	decl := testProviderAccountAPIPermission(poller, provider)
	perm := decl.Permissions[0]

	gate := contracttesting.NewConsentGate()
	transport := &fakeTransport{fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		return outbound.AccountQuotaResponse{StatusCode: 200, Body: []byte("ok")}, nil
	}}

	// exercise resets the call counter and runs exactly one Poll, so Observe
	// (reading the counter right after) reflects only THIS Exercise — never
	// a cumulative count carried over from an earlier arm.
	exercise := func() {
		transport.mu.Lock()
		transport.calls = 0
		transport.mu.Unlock()
		_, _ = poller.Poll(context.Background(), PollRequest{
			Key:            AccountQuotaKey{Provider: provider, Account: "acct-1", Scope: "default"},
			Granted:        func() bool { return gate.Granted("", perm.Key) },
			Resolver:       fakeResolver{secret: "s"},
			Transport:      transport,
			DestinationKey: "primary",
		})
	}

	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		Key: perm.Key,
		// This declaration exports exactly one permission (see
		// agent.AccountAPIPermissionKey's own doc comment and
		// processlifecycle's identical note on its own lone kitty
		// permission), so the key held open beside it is foreign, with no
		// closure of OUR OWN to drive: the key-isolation arm is inert and
		// repeats the revoked arm exactly. Wired anyway because the contract
		// admits no opt-out — a flag an install-type wiring could take is one
		// a live-gate wiring could take too.
		OtherKeys: []string{agent.HooksPermissionKey},
		SetState: contracttesting.OnlyKey(perm.Key, func(state permission.State) {
			gate.SetState(perm.Key, state)
			switch state {
			case permission.StateGranted:
				if err := perm.Apply(); err != nil {
					t.Errorf("Apply: %v", err)
				}
			case permission.StateDenied:
				if err := perm.Remove(); err != nil {
					t.Errorf("Remove: %v", err)
				}
			}
		}),
		Exercise: exercise,
		Observe:  func() bool { return transport.callCount() > 0 },
	})
}
