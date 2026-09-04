package push

import "testing"

// TestPruneDropsDeliveryHealthOfRevokedTokens: the health map outliving the
// SUBSCRIPTION is the design (a phone pruned on 410 keeps its last failure
// visible, which is the state the §8.3 panel exists to explain). Outliving
// the device TOKEN is not: `token revoke <device>` is the one gesture that
// ends a phone's relationship with the relay, and an entry keyed by an id
// that no longer names anything is unreachable state that only grows.
func TestPruneDropsDeliveryHealthOfRevokedTokens(t *testing.T) {
	svc, clk, _ := newTestService(t)
	at := clk.now().Unix()
	if err := svc.SetSubscription("live", testSubscription(t, "https://push.example/v2/live")); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSubscription("revoked", testSubscription(t, "https://push.example/v2/revoked")); err != nil {
		t.Fatal(err)
	}
	svc.SetDeliveryStatus("live", DeliveryStatus{At: at, OK: true})
	svc.SetDeliveryStatus("revoked", DeliveryStatus{At: at, OK: true})
	// A token whose address was pruned on 410 but whose token still exists.
	svc.SetDeliveryStatus("gone-address", DeliveryStatus{At: at, OK: false, Detail: "410"})

	svc.Prune(func(id string) bool { return id != "revoked" })

	if st, ok := svc.DeliveryStatus("revoked"); ok {
		t.Fatalf("a revoked token kept its delivery health %+v — nothing can ever read or clear it again", st)
	}
	if _, ok := svc.DeliveryStatus("live"); !ok {
		t.Fatal("the prune dropped a live token's delivery health")
	}
	if _, ok := svc.DeliveryStatus("gone-address"); !ok {
		t.Fatal("the prune dropped the last failure of a token whose address is gone but whose identity is not — that verdict is what the §8.3 panel explains")
	}
}
