package contracttesting

import (
	"testing"

	"irrlicht/core/domain/permission"
)

// PermissionGate wires one declared permission's consent-gated call site
// into AssertPermissionGated. SetState drives the permission to the given
// state through whatever mechanism the call site actually checks — a
// mutable fake ConsentGranter for a live per-request check (an HTTP handler,
// input forwarding), or the permission's real Apply/Remove closures for an
// install-type permission (a hook file, a config block). Exercise performs
// the action the permission is supposed to gate. Observe reports whether
// the gated effect is currently present.
type PermissionGate struct {
	SetState func(state permission.State)
	Exercise func()
	Observe  func() bool
}

// AssertPermissionGated runs the issue #797 contract against g: no
// observable effect while the permission is pending, the effect observable
// once granted, and the effect undone after a subsequent revoke. Each
// state transition is followed by an Exercise + Observe pair, so a call
// site that forgot to check consent is caught even when the underlying
// resource (a hook entry, a config block) was left behind by an earlier
// grant.
func AssertPermissionGated(t *testing.T, g PermissionGate) {
	t.Helper()
	t.Run("pending_no_effect", func(t *testing.T) {
		g.SetState(permission.StatePending)
		g.Exercise()
		if g.Observe() {
			t.Error("effect observed while permission is pending — call site is not consent-gated")
		}
	})
	t.Run("granted_effect_observable", func(t *testing.T) {
		g.SetState(permission.StateGranted)
		g.Exercise()
		if !g.Observe() {
			t.Error("effect not observed after granting permission")
		}
	})
	t.Run("revoked_effect_undone", func(t *testing.T) {
		g.SetState(permission.StateDenied)
		g.Exercise()
		if g.Observe() {
			t.Error("effect still observed after revoking a previously granted permission")
		}
	})
}
