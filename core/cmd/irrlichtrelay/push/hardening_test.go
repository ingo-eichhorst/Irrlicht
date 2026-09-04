package push

// Hardening pins added after review: the two remedy tests were seen red
// before the per-file advice existed, and the rewrite test exists because
// replacing writeFileAtomic's body with a plain os.WriteFile left the whole
// suite green.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCorruptVAPIDErrorCarriesItsRemedy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, vapidFilename), []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewService(dir, nil)
	if err == nil {
		t.Fatal("NewService accepted a corrupt VAPID identity")
	}
	if !strings.Contains(err.Error(), "re-pairing every phone") {
		t.Fatalf("corrupt-VAPID error must carry its own remedy (restore or accept re-pairing), got: %v", err)
	}
}

func TestCorruptRegistryErrorCarriesItsRemedy(t *testing.T) {
	// A corrupt registry has a CHEAP remedy — delete it, phones
	// re-subscribe on next open (§8.6) — and steering the operator toward
	// the VAPID advice (which warns of re-pairing every phone) steers them
	// away from it.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, registryFilename), []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewService(dir, nil)
	if err == nil {
		t.Fatal("NewService accepted a corrupt subscription registry")
	}
	if !strings.Contains(err.Error(), "re-subscribe") {
		t.Fatalf("corrupt-registry error must name the cheap remedy (delete; phones re-subscribe), got: %v", err)
	}
	if strings.Contains(err.Error(), "re-pairing every phone") {
		t.Fatalf("corrupt-registry error carries the VAPID remedy, which is wrong for this file: %v", err)
	}
}

func TestRegistryRewriteReplacesTheFile(t *testing.T) {
	// os.SameFile across a rewrite must be false: temp+rename allocates a
	// new inode, a plain truncating write reuses it. Atomicity is
	// load-bearing here — a crash mid-write leaves truncated JSON, which is
	// deliberately fatal at the next start (the corrupt-file refusal above).
	svc, _, dir := newTestService(t)
	path := filepath.Join(dir, registryFilename)
	if err := svc.SetSubscription("t1", testSubscription(t, "https://push.example/v2/a")); err != nil {
		t.Fatal(err)
	}
	fi1, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSubscription("t1", testSubscription(t, "https://push.example/v2/b")); err != nil {
		t.Fatal(err)
	}
	fi2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(fi1, fi2) {
		t.Fatal("registry rewrite reused the inode — not temp+rename atomic")
	}
}

func TestPruneLoopPrunesOnItsTicker(t *testing.T) {
	// The revocation coupling in production runs through PruneLoop, not
	// through direct Prune calls; this drives the ticker and the stop
	// channel. Bounded polling, no fixed sleeps.
	svc, _, _ := newTestService(t)
	if err := svc.SetSubscription("gone", testSubscription(t, "https://push.example/v2/a")); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		svc.PruneLoop(stop, time.Millisecond, func(string) bool { return false })
		close(done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for len(svc.Subscriptions()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("PruneLoop never pruned the orphaned subscription")
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("PruneLoop did not stop on its stop channel")
	}
}
