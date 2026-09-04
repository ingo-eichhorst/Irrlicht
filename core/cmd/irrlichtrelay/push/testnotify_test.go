package push

import (
	"testing"
	"time"
)

// TestClaimTestNotificationIsPerDeviceAndDrains covers the window the "send
// a test notification" button is limited by: one device's claim never
// refuses another's, a refusal says how long is left, and the window drains
// on its own.
func TestClaimTestNotificationIsPerDeviceAndDrains(t *testing.T) {
	clk := time.Unix(1_700_000_000, 0)
	svc, err := NewService(t.TempDir(), func() time.Time { return clk })
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if _, ok := svc.ClaimTestNotification("phone-a"); !ok {
		t.Fatal("the first claim was refused")
	}
	retry, ok := svc.ClaimTestNotification("phone-a")
	if ok {
		t.Fatal("a second claim inside the window was allowed")
	}
	if retry <= 0 || retry > TestNotificationInterval {
		t.Fatalf("retry-after = %v, want a remainder of %v", retry, TestNotificationInterval)
	}
	if _, ok := svc.ClaimTestNotification("phone-b"); !ok {
		t.Fatal("one device's test notification rate-limited another's")
	}

	clk = clk.Add(TestNotificationInterval)
	if _, ok := svc.ClaimTestNotification("phone-a"); !ok {
		t.Fatal("the window never drained")
	}
}

// TestClaimTestNotificationKeepsOnlyTheCurrentWindow pins what makes this
// map safe to key by device token without any coupling to the token prune:
// each claim sweeps the entries that have drained, so the map is bounded by
// the devices that pressed the button in the last interval rather than by
// every device that ever did.
func TestClaimTestNotificationKeepsOnlyTheCurrentWindow(t *testing.T) {
	clk := time.Unix(1_700_000_000, 0)
	svc, err := NewService(t.TempDir(), func() time.Time { return clk })
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, ok := svc.ClaimTestNotification(id); !ok {
			t.Fatalf("claim for %s refused", id)
		}
	}
	clk = clk.Add(TestNotificationInterval)
	if _, ok := svc.ClaimTestNotification("e"); !ok {
		t.Fatal("claim for e refused")
	}

	svc.mu.Lock()
	held := len(svc.testSends)
	svc.mu.Unlock()
	if held != 1 {
		t.Fatalf("the window holds %d entries after draining, want only the live one — it grows with every device that ever tested", held)
	}
}
