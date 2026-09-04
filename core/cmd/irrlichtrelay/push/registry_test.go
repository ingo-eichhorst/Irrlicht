package push

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSetReplaceDeleteSubscription(t *testing.T) {
	svc, _, _ := newTestService(t)

	if err := svc.SetSubscription("t1", testSubscription(t, "https://push.example/v2/a")); err != nil {
		t.Fatalf("set t1: %v", err)
	}
	replacement := testSubscription(t, "https://push.example/v2/b")
	if err := svc.SetSubscription("t1", replacement); err != nil {
		t.Fatalf("replace t1: %v", err)
	}
	entries := svc.Subscriptions()
	if len(entries) != 1 {
		t.Fatalf("after replace: %d entries, want 1 (one subscription per token id)", len(entries))
	}
	if got := entries[0].Subscription.Endpoint; got != replacement.Endpoint {
		t.Fatalf("after replace endpoint = %q, want the replacement %q", got, replacement.Endpoint)
	}

	if err := svc.SetSubscription("t0", testSubscription(t, "https://push.example/v2/c")); err != nil {
		t.Fatalf("set t0: %v", err)
	}
	entries = svc.Subscriptions()
	if len(entries) != 2 || entries[0].TokenID != "t0" || entries[1].TokenID != "t1" {
		t.Fatalf("entries not sorted by token id: %+v", entries)
	}

	if err := svc.DeleteSubscription("t1"); err != nil {
		t.Fatalf("delete t1: %v", err)
	}
	if entries = svc.Subscriptions(); len(entries) != 1 || entries[0].TokenID != "t0" {
		t.Fatalf("after delete: %+v, want only t0", entries)
	}
	// Idempotent: deleting the deleted is a quiet no-op.
	if err := svc.DeleteSubscription("t1"); err != nil {
		t.Fatalf("second delete t1: %v", err)
	}
}

func TestSetSubscriptionRejectsInvalid(t *testing.T) {
	svc, _, _ := newTestService(t)
	bad := testSubscription(t, "https://push.example/v2/a")
	// A 33-byte SEC1-compressed point: only the 65-byte uncompressed form is
	// legal on the Web Push wire.
	bad.Keys.P256dh = base64.RawURLEncoding.EncodeToString(append([]byte{0x02}, make([]byte, 32)...))
	err := svc.SetSubscription("t1", bad)
	if err == nil {
		t.Fatal("SetSubscription accepted a 33-byte p256dh")
	}
	if !strings.Contains(err.Error(), "p256dh") {
		t.Fatalf("error %q does not name the p256dh field", err)
	}
	if got := svc.Subscriptions(); len(got) != 0 {
		t.Fatalf("registry stored a dud: %+v", got)
	}
}

func TestRegistryFileModeAndAtomicWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes not meaningful on Windows")
	}
	svc, _, dir := newTestService(t)
	if err := svc.SetSubscription("t1", testSubscription(t, "https://push.example/v2/a")); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(filepath.Join(dir, registryFilename))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("registry file mode = %o, want 600", perm)
	}

	// Atomic temp+rename leaves nothing else behind: the dir holds exactly
	// the two push files.
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range names {
		got = append(got, e.Name())
	}
	if len(got) != 2 || got[0] != registryFilename || got[1] != vapidFilename {
		t.Fatalf("data dir holds %v, want exactly [%s %s]", got, registryFilename, vapidFilename)
	}

	// The on-disk shape is the versioned envelope.
	data, err := os.ReadFile(filepath.Join(dir, registryFilename))
	if err != nil {
		t.Fatal(err)
	}
	var f registryFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("registry file does not parse: %v\n%s", err, data)
	}
	if f.Version != 1 || len(f.Subscriptions) != 1 {
		t.Fatalf("registry file = %+v, want version 1 with one subscription", f)
	}
}

func TestRegistryPersistsAcrossRestart(t *testing.T) {
	svc, _, dir := newTestService(t)
	sub := testSubscription(t, "https://push.example/v2/a")
	if err := svc.SetSubscription("t1", sub); err != nil {
		t.Fatal(err)
	}
	want := svc.Subscriptions()

	restarted, err := NewService(dir, newTestClock().now)
	if err != nil {
		t.Fatalf("NewService after restart: %v", err)
	}
	got := restarted.Subscriptions()
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("after restart: %+v, want %+v", got, want)
	}
}

// TestNoopPruneWritesNothing pins the write-only-on-change rule
// (docs/mobile-notifications-arc42.md §8.6): a prune that drops nothing must
// not rewrite the file. The atomic write goes through temp+rename, so a
// rewrite always produces a NEW file — os.SameFile is the deterministic
// tell, with mtime and content as belt and braces.
func TestNoopPruneWritesNothing(t *testing.T) {
	svc, _, dir := newTestService(t)
	if err := svc.SetSubscription("t1", testSubscription(t, "https://push.example/v2/a")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, registryFilename)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	contentBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if n := svc.Prune(func(string) bool { return true }); n != 0 {
		t.Fatalf("no-op Prune dropped %d", n)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("no-op prune replaced the registry file (temp+rename happened)")
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("no-op prune touched the registry file: mtime %v → %v", before.ModTime(), after.ModTime())
	}
	contentAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contentAfter) != string(contentBefore) {
		t.Fatal("no-op prune changed the registry content")
	}
}

func TestPruneDropsOrphans(t *testing.T) {
	svc, _, dir := newTestService(t)
	if err := svc.SetSubscription("revoked", testSubscription(t, "https://push.example/v2/a")); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSubscription("kept", testSubscription(t, "https://push.example/v2/b")); err != nil {
		t.Fatal(err)
	}

	if n := svc.Prune(func(id string) bool { return id == "kept" }); n != 1 {
		t.Fatalf("Prune dropped %d, want 1", n)
	}
	entries := svc.Subscriptions()
	if len(entries) != 1 || entries[0].TokenID != "kept" {
		t.Fatalf("after prune: %+v, want only \"kept\"", entries)
	}

	// The drop is persisted, not merely in-memory.
	data, err := os.ReadFile(filepath.Join(dir, registryFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "revoked") {
		t.Fatalf("pruned entry still on disk:\n%s", data)
	}
}
