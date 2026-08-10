package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/permission"
)

var errRemoveBoom = errors.New("could not rewrite settings.json")

// --- #1385: an aggregate "granted but not applied" signal ----------------
//
// #1362 made a failed consent effect visible PER PERMISSION, inside the
// wizard. A startup re-apply failure therefore reached only a user who
// opened Settings. These tests pin the aggregate a passive surface renders
// instead: a top-level list on the permissions snapshot, so the web
// dashboard and the macOS app can say "N permissions are granted but not
// applied" without anybody opening anything.
//
// Like the #1362 file, every assertion reads the snapshot MARSHALLED TO
// JSON rather than a Go field, so the whole file compiles against pre-fix
// main and the red is a behavioural one instead of a compile error. It
// also pins the exact wire contract both UIs decode.

// snapshotJSON is byte-for-byte what GET /api/v1/permissions returns.
func snapshotJSON(t *testing.T, svc *services.PermissionService) map[string]any {
	t.Helper()
	b, err := json.Marshal(svc.Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	return out
}

// unappliedGrants reads the aggregate off the wire form. A missing key
// reads as an empty list, which is what pre-fix main produces — so the
// guard tests below are honestly vacuous there rather than falsely green
// on a field they never saw.
func unappliedGrants(t *testing.T, svc *services.PermissionService) []map[string]any {
	t.Helper()
	raw, ok := snapshotJSON(t, svc)["unapplied_grants"]
	if !ok || raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("unapplied_grants is %T, want a JSON array", raw)
	}
	out := make([]map[string]any, 0, len(list))
	for i, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("unapplied_grants[%d] is %T, want an object", i, e)
		}
		out = append(out, m)
	}
	return out
}

// TestStartupReapplyFailureIsAggregatedOnSnapshot is the #1385 defect test.
// Start re-runs every granted effect on boot; when one fails, #1362 records
// the reason on that permission — but a surface that never opens the wizard
// has nothing to count. This asserts the aggregate exists at the top level
// of the snapshot, which is the only thing a passive indicator can read.
func TestStartupReapplyFailureIsAggregatedOnSnapshot(t *testing.T) {
	f := &applyFailure{failApplyFor: -1}
	store := &mockPermStore{set: permission.Set{
		"testagent": {"hooks": permission.StateGranted},
	}}
	svc := newFailingService(f, store, &mockPush{})
	svc.Start(context.Background())

	if applied, _ := f.calls(); applied != 1 {
		t.Fatalf("apply calls at start = %d, want 1", applied)
	}

	got := unappliedGrants(t, svc)
	if len(got) != 1 {
		t.Fatalf("a startup re-apply failure is invisible to any passive surface: "+
			"unapplied_grants has %d entries, want 1 (snapshot: %v)",
			len(got), snapshotJSON(t, svc))
	}
	e := got[0]
	if e["agent"] != "testagent" {
		t.Errorf("agent = %v, want testagent", e["agent"])
	}
	if e["key"] != "hooks" {
		t.Errorf("key = %v, want hooks", e["key"])
	}
	// The click-through from the aggregate to the specific cause: the
	// headline is a count, but each entry must still name WHICH permission
	// and WHY, so the five "granted but not applied" diagnoses stay
	// distinguishable instead of collapsing into one number.
	if e["agent_display_name"] != "Test Agent" {
		t.Errorf("agent_display_name = %v, want Test Agent", e["agent_display_name"])
	}
	if e["title"] != "Install hooks" {
		t.Errorf("title = %v, want Install hooks", e["title"])
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "settings.json is malformed") {
		t.Errorf("reason = %q, want the verbatim effect error", reason)
	}
}

// TestAnsweredApplyFailureIsAggregatedOnSnapshot covers the other entry
// point into runEffects — an interactive grant — so the aggregate is not
// wired to Start alone.
func TestAnsweredApplyFailureIsAggregatedOnSnapshot(t *testing.T) {
	f := &applyFailure{failApplyFor: -1}
	svc := newFailingService(f, &mockPermStore{}, &mockPush{})
	svc.Start(context.Background())

	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "hooks", Grant: true},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got := unappliedGrants(t, svc); len(got) != 1 {
		t.Fatalf("unapplied_grants has %d entries after a failed grant, want 1", len(got))
	}
}

// TestSucceedingGrantIsNotAggregated is the VACUITY GUARD, not a defect
// test: an aggregate that counted every granted permission would pass the
// two tests above while being useless. It is trivially green on pre-fix
// main (no field at all) and only becomes meaningful once the field exists.
func TestSucceedingGrantIsNotAggregated(t *testing.T) {
	f := &applyFailure{failApplyFor: 0} // never fails
	svc := newFailingService(f, &mockPermStore{}, &mockPush{})
	svc.Start(context.Background())

	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "hooks", Grant: true},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !svc.Granted("testagent", "hooks") {
		t.Fatal("precondition: the grant should have been recorded")
	}
	if got := unappliedGrants(t, svc); len(got) != 0 {
		t.Fatalf("a healthy grant is reported as unapplied: %v", got)
	}
}

// TestRetrySuccessClearsTheAggregate pins that the indicator is
// "dismissible by fixing" — the #1385 constraint that it must not nag. The
// user retries, the install succeeds, and the signal goes away on its own
// with no dismiss gesture anywhere.
func TestRetrySuccessClearsTheAggregate(t *testing.T) {
	f := &applyFailure{failApplyFor: 1} // fail once, then succeed
	svc := newFailingService(f, &mockPermStore{}, &mockPush{})
	svc.Start(context.Background())

	ans := []services.PermissionAnswer{{Agent: "testagent", Permission: "hooks", Grant: true}}
	if err := svc.Answer(ans); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got := unappliedGrants(t, svc); len(got) != 1 {
		t.Fatalf("precondition: the first grant should have failed; got %d entries", len(got))
	}
	// Re-answering identically is #1362's retry.
	if err := svc.Answer(ans); err != nil {
		t.Fatalf("retry Answer: %v", err)
	}
	if got := unappliedGrants(t, svc); len(got) != 0 {
		t.Fatalf("the aggregate survived a successful retry: %v", got)
	}
}

// TestFailedRemoveIsNotAnUnappliedGrant is a SCOPE LOCK, green by
// construction once the field exists. "Granted but not applied" is a claim
// about a grant; a denied permission whose Remove failed is the opposite
// fault ("revoked, but not undone") and the wizard already words it
// differently. Folding it into this count would make the headline lie.
func TestFailedRemoveIsNotAnUnappliedGrant(t *testing.T) {
	f := &applyFailure{failApplyFor: 0, removeErr: errRemoveBoom}
	svc := newFailingService(f, &mockPermStore{}, &mockPush{})
	svc.Start(context.Background())

	grant := []services.PermissionAnswer{{Agent: "testagent", Permission: "hooks", Grant: true}}
	if err := svc.Answer(grant); err != nil {
		t.Fatalf("Answer(grant): %v", err)
	}
	revoke := []services.PermissionAnswer{{Agent: "testagent", Permission: "hooks", Grant: false}}
	if err := svc.Answer(revoke); err != nil {
		t.Fatalf("Answer(revoke): %v", err)
	}
	if svc.Granted("testagent", "hooks") {
		t.Fatal("precondition: the permission should read denied")
	}
	if got := unappliedGrants(t, svc); len(got) != 0 {
		t.Fatalf("a failed Remove was counted as a granted-but-unapplied permission: %v", got)
	}
}

// TestUnappliedAggregateDoesNotChangeConsent is a LOCK on #1385's third
// constraint: surfacing a failure is not a re-prompt. Reading the snapshot
// must move nothing about Granted/state, and must not widen needsWizard —
// the pending-driven signal the two surfaces use to decide whether to pop
// the wizard open. Green by construction; it exists so a later change that
// wires the aggregate into wizard presentation fails here.
func TestUnappliedAggregateDoesNotChangeConsent(t *testing.T) {
	f := &applyFailure{failApplyFor: -1}
	store := &mockPermStore{set: permission.Set{
		"testagent": {"hooks": permission.StateGranted, "transcripts": permission.StateGranted},
	}}
	svc := newFailingService(f, store, &mockPush{})
	svc.Start(context.Background())

	if len(unappliedGrants(t, svc)) == 0 {
		t.Fatal("precondition: the startup re-apply should have failed")
	}
	if !svc.Granted("testagent", "hooks") {
		t.Error("the aggregate rolled back consent")
	}
	// No permission is pending, so no surface may conclude a wizard is due.
	for _, a := range svc.Snapshot().Agents {
		for _, p := range a.Permissions {
			if p.State == string(permission.StatePending) {
				t.Errorf("%s/%s went back to pending — the aggregate re-prompted", a.Name, p.Key)
			}
		}
	}
}
