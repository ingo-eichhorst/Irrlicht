package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"irrlicht/core/ports/outbound"
)

const museFixtureBody = `{"is_subs_active":true,"subs_tier_name":"Muse Code Everyday Usage","subs_usage":{"window":{"used_percent":4,"resets_at":5},"weekly":{"used_percent":28,"resets_at":6}}}`

func museFixtureTransport() *fakeTransport {
	return &fakeTransport{
		fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
			return outbound.AccountQuotaResponse{StatusCode: 200, Body: []byte(museFixtureBody)}, nil
		},
	}
}

func TestMuseAccountRefresh_ReturnsSnapshotOnSuccess(t *testing.T) {
	poller := NewAccountPoller()
	transport := museFixtureTransport()
	resolver := fakeResolver{secret: "test-token"}

	snap, err := MuseAccountRefresh(t.Context(), poller, resolver, transport, alwaysGranted, "session-1", time.Unix(100, 0))
	if err != nil {
		t.Fatalf("MuseAccountRefresh: %v", err)
	}
	if snap == nil {
		t.Fatal("snap is nil on success")
	}
	if len(snap.Windows) != 2 {
		t.Fatalf("Windows = %+v, want 2 entries", snap.Windows)
	}
}

func TestMuseAccountRefresh_EmptySessionIDRefused(t *testing.T) {
	poller := NewAccountPoller()
	transport := museFixtureTransport()
	resolver := fakeResolver{secret: "test-token"}
	if _, err := MuseAccountRefresh(t.Context(), poller, resolver, transport, alwaysGranted, "", time.Unix(1, 0)); err == nil {
		t.Fatal("expected an error for an empty sessionID")
	}
}

// TestMuseAccountRefresh_NeverPublishesZeroOnFailure is mutation fixture #3:
// a session whose credential resolver always fails must get (nil, err) from
// MuseAccountRefresh, never a session.RateLimitSnapshot{} zero value that
// would render as "0% used" in a client (issue #2003 §1.4's own
// prohibition, extended here to the Muse-layer composition boundary).
// tools/lib/museaccountapi-zero-quota-on-auth-failure-mutations_test.sh
// mutates the !obs.HasValue branch in museaccountpoll.go to return an empty
// snapshot instead of an error and confirms this test goes red.
func TestMuseAccountRefresh_NeverPublishesZeroOnFailure(t *testing.T) {
	poller := NewAccountPoller()
	transport := &fakeTransport{
		fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
			return outbound.AccountQuotaResponse{}, &outbound.QuotaError{Reason: outbound.QuotaFailureAuthRejected, Detail: "status 401"}
		},
	}
	resolver := fakeResolver{secret: "test-token"}

	snap, err := MuseAccountRefresh(t.Context(), poller, resolver, transport, alwaysGranted, "session-1", time.Unix(1, 0))
	if err == nil {
		t.Fatal("expected an error on an auth-rejected fetch")
	}
	if snap != nil {
		t.Fatalf("snap = %+v, want nil — a failed fetch must never publish a snapshot", snap)
	}
}

func TestMuseAccountRefresh_ResolverFailureNeverPublishesZero(t *testing.T) {
	poller := NewAccountPoller()
	transport := museFixtureTransport()
	resolver := fakeResolver{err: errors.New("credential file missing")}

	snap, err := MuseAccountRefresh(t.Context(), poller, resolver, transport, alwaysGranted, "session-1", time.Unix(1, 0))
	if err == nil {
		t.Fatal("expected an error when the credential resolver fails")
	}
	if snap != nil {
		t.Fatalf("snap = %+v, want nil", snap)
	}
	if transport.callCount() != 0 {
		t.Fatalf("transport was called %d time(s), want 0 — a failed resolve must never reach the network", transport.callCount())
	}
}

// TestMuseAccountQuotaKey_TwoSessionsNeverCollide is mutation fixture #4:
// two DIFFERENT sessions must never share one AccountPoller cache entry when
// Muse has no confirmed provider-issued account identifier — sharing one
// entry is exactly how session B would see session A's quota reading
// without either session's identity ever being confirmed as the same
// account (issue #2007 §1.3, epic #1977 line 107).
// tools/lib/museaccountapi-share-unknown-identity-mutations_test.sh mutates
// MuseAccountQuotaKey to return a fixed constant regardless of sessionID and
// confirms this test goes red.
func TestMuseAccountQuotaKey_TwoSessionsNeverCollide(t *testing.T) {
	if MuseAccountQuotaKey("session-a") == MuseAccountQuotaKey("session-b") {
		t.Fatal("MuseAccountQuotaKey produced the same key for two different sessions")
	}

	poller := NewAccountPoller()
	transport := museFixtureTransport()
	resolver := fakeResolver{secret: "test-token"}

	if _, err := MuseAccountRefresh(t.Context(), poller, resolver, transport, alwaysGranted, "session-a", time.Unix(1, 0)); err != nil {
		t.Fatalf("session-a MuseAccountRefresh: %v", err)
	}
	if _, err := MuseAccountRefresh(t.Context(), poller, resolver, transport, alwaysGranted, "session-b", time.Unix(1, 0)); err != nil {
		t.Fatalf("session-b MuseAccountRefresh: %v", err)
	}
	if got := transport.callCount(); got != 2 {
		t.Fatalf("transport was called %d time(s), want 2 — two sessions with no confirmed shared account must never dedupe into one poll", got)
	}
}

func TestMuseAccountRefresh_NoCachedValueYetIsAnError(t *testing.T) {
	poller := NewAccountPoller()
	transport := &fakeTransport{
		fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
			return outbound.AccountQuotaResponse{}, context.DeadlineExceeded
		},
	}
	resolver := fakeResolver{secret: "test-token"}
	snap, err := MuseAccountRefresh(t.Context(), poller, resolver, transport, alwaysGranted, "session-1", time.Unix(1, 0))
	if err == nil || snap != nil {
		t.Fatalf("snap=%v err=%v, want nil snap and a non-nil error", snap, err)
	}
}
