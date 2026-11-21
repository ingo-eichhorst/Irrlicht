package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/vibe"
	"irrlicht/core/adapters/outbound/chromecookies"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/adapters/outbound/mistralvibe"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/permission"
	"irrlicht/core/domain/session"
	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

// Issue #2012's daemon wiring. Nothing here dials: the one test that builds
// the real transport never polls through it, and every poll below goes
// through a fake transport.

const vibeFixturePassword = "irrlicht-test-safe-storage-password" // chromecookies/testdata/make_fixture.py

// fakeChromeDir is a Chrome user data dir whose Default profile holds the
// committed fixture store.
func fakeChromeDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Local State"), []byte(`{"profile":{"last_used":"Default"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := os.ReadFile("../../adapters/outbound/chromecookies/testdata/Cookies-v24")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Default", "Cookies"), store, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// keychainStandIn counts reads of the stand-in for the "Chrome Safe Storage"
// item. With fail set every read fails, as an unanswered dialog does.
type keychainStandIn struct {
	reads atomic.Int64
	fail  bool
}

func (k *keychainStandIn) source(context.Context) (chromecookies.Key, error) {
	k.reads.Add(1)
	if k.fail {
		return chromecookies.Key{}, chromecookies.ErrKeychain
	}
	return chromecookies.DeriveKey(vibeFixturePassword)
}

// testVibeAPI is the daemon's wiring over chromeDir and the stand-in
// Keychain; api.browser.Key reads through the grant's key cache.
func testVibeAPI(chromeDir string, kc *keychainStandIn) mistralVibeAPI {
	keys := chromecookies.NewKeyCache(kc.source)
	browser := mistralvibe.Browser{
		UserDataDir: func() (string, error) { return chromeDir, nil },
		Key:         keys.Key,
	}
	return newMistralVibeAPI(e2eLog{}, browser, keys)
}

func TestMistralVibeAPI_SweeperIsBuiltWhenTheTransportIs(t *testing.T) {
	api := mistralVibeEffects(e2eLog{})
	if api.transport == nil {
		t.Fatal("mistralVibeEffects built no transport for the fixed destination")
	}
	if api.sweeper(nil, nil, nil, e2eLog{}) == nil {
		t.Fatal("sweeper() returned nil with a transport; the daemon would start no sweep")
	}
	if (mistralVibeAPI{}).sweeper(nil, nil, nil, e2eLog{}) != nil {
		t.Fatal("sweeper() built a sweep with no transport to poll through")
	}
}

// countingVibeAPI is the daemon's wiring over a fake Chrome holding the
// committed fixture store, counting every read of the Chrome profile (Local
// State) and of the Keychain stand-in, with the transport swapped for one that
// never leaves the machine.
func countingVibeAPI(t *testing.T) (mistralVibeAPI, *atomic.Int64, *keychainStandIn) {
	t.Helper()
	kc := &keychainStandIn{}
	chrome := fakeChromeDir(t)
	var profileReads atomic.Int64
	keys := chromecookies.NewKeyCache(kc.source)
	browser := mistralvibe.Browser{
		UserDataDir: func() (string, error) { profileReads.Add(1); return chrome, nil },
		Key:         keys.Key,
	}
	api := newMistralVibeAPI(e2eLog{}, browser, keys)
	api.transport = vibeOKTransport{}
	return api, &profileReads, kc
}

func vibePermissionService(t *testing.T, api mistralVibeAPI, mode string) *services.PermissionService {
	t.Helper()
	perms := services.NewPermissionService(services.PermissionServiceDeps{
		Agents: []agent.Agent{mistralvibe.PermissionDeclaration(api.Start, api.Stop)},
		Store:  filesystem.NewPermissionStore(t.TempDir()),
		Push:   services.NewPushService(),
		Log:    e2eLog{},
		Mode:   mode,
	})
	perms.Start(context.Background())
	return perms
}

func answerVibe(t *testing.T, perms *services.PermissionService, grant bool) {
	t.Helper()
	if err := perms.Answer([]services.PermissionAnswer{{Agent: mistralvibe.Name, Permission: mistralvibe.PermissionKey, Grant: grant}}); err != nil {
		t.Fatalf("Answer(%v): %v", grant, err)
	}
}

// TestMistralVibeAPI_SweeperIsGatedOnTheDaemonsGrant drives the REAL
// sweeper() wiring against a real ask-mode PermissionService (QA round 1,
// finding 2): pending reads nothing from the browser, granted reads and binds
// the reading, revoked reads nothing and unbinds it. The grant the sweep
// checks is the one mistralvibe_effects.go wires
// (perms.Granted(mistralvibe.Name, mistralvibe.PermissionKey)), not a test
// stand-in.
func TestMistralVibeAPI_SweeperIsGatedOnTheDaemonsGrant(t *testing.T) {
	api, profileReads, _ := countingVibeAPI(t)
	perms := vibePermissionService(t, api, config.PermissionModeAsk)
	repo := &vibeGateRepo{}
	sw := api.sweeper(repo, repo, perms, e2eLog{})
	ctx := context.Background()

	sw.RunOnce(ctx)
	if n := profileReads.Load(); n != 0 {
		t.Fatalf("pending: the sweep read the Chrome profile %d time(s) before anyone granted the permission", n)
	}

	answerVibe(t, perms, true)
	sw.RunOnce(ctx)
	if profileReads.Load() == 0 || !repo.bound() {
		t.Fatalf("granted: %d profile read(s), bound=%v, want a read and a bound reading", profileReads.Load(), repo.bound())
	}

	answerVibe(t, perms, false)
	reads := profileReads.Load()
	sw.RunOnce(ctx)
	if profileReads.Load() != reads {
		t.Fatalf("revoked: the sweep read the Chrome profile %d more time(s)", profileReads.Load()-reads)
	}
	if repo.bound() {
		t.Fatal("revoked: the reading stayed on the session")
	}
}

// TestMistralVibeAPI_GrantAllBootReadsNothing is QA round 1 finding 7, now
// checking what its name says: a grant-all daemon boot (the recording rig,
// ir:test-mac, build-dev's suggested `IRRLICHT_PERMISSION_MODE=grant-all`)
// leaves the browser-session permission unanswered, and the real sweep then
// reads neither the Chrome profile nor the Keychain, even with a vibe session
// live and a profile signed in to Mistral. The permission's own Start/Stop
// effects read nothing either.
func TestMistralVibeAPI_GrantAllBootReadsNothing(t *testing.T) {
	api, profileReads, kc := countingVibeAPI(t)
	perms := vibePermissionService(t, api, config.PermissionModeGrantAll)
	if perms.Granted(mistralvibe.Name, mistralvibe.PermissionKey) {
		t.Fatal("a grant-all boot granted the browser-session permission on the user's behalf")
	}
	repo := &vibeGateRepo{}
	sw := api.sweeper(repo, repo, perms, e2eLog{})
	for range 3 {
		sw.RunOnce(context.Background())
	}
	for range 3 {
		mustRun(t, api.Start)
		mustRun(t, api.Stop)
	}
	if pollUntil(t, 300*time.Millisecond, 10*time.Millisecond, func() bool { return kc.reads.Load() > 0 || profileReads.Load() > 0 }) {
		t.Fatalf("a grant-all boot read the browser: %d Chrome profile read(s), %d Keychain read(s), want none", profileReads.Load(), kc.reads.Load())
	}
	if repo.bound() {
		t.Fatal("a grant-all boot bound a Mistral reading")
	}
}

// blockingVibeTransport holds every fetch until its context ends and records
// that it ended.
type blockingVibeTransport struct {
	entered   chan struct{}
	once      sync.Once
	cancelled atomic.Int64
}

func (b *blockingVibeTransport) Fetch(ctx context.Context, _ outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
	b.once.Do(func() { close(b.entered) })
	<-ctx.Done()
	b.cancelled.Add(1)
	return outbound.AccountQuotaResponse{}, ctx.Err()
}

// TestMistralVibeAPI_RevokeCancelsTheFetchInFlight is a target of #2012
// §7.3 ("keep polling after the browser-session permission is revoked"): a
// request already on its way when the permission goes off is cancelled by
// the revoke, not left to finish.
func TestMistralVibeAPI_RevokeCancelsTheFetchInFlight(t *testing.T) {
	api := testVibeAPI(fakeChromeDir(t), &keychainStandIn{})
	transport := &blockingVibeTransport{entered: make(chan struct{})}
	browser, err := api.browser.Session()
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	var granted atomic.Bool
	granted.Store(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = services.MistralVibeRefresh(context.Background(), api.poller, browser, transport, granted.Load, "v1")
	}()
	select {
	case <-transport.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("after 5s: the fetch never started")
	}
	granted.Store(false)
	mustRun(t, api.Stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("after 5s: the revoke left the request in flight running")
	}
	if transport.cancelled.Load() != 1 {
		t.Fatal("the in-flight request was not cancelled by the revoke")
	}
}

// TestMistralVibeAPI_GrantRetriesAFailedKeychainRead (review of #2012,
// finding 4): a failed Keychain read is kept so the dialog is not raised on
// every poll, and granting the permission again is the retry
// (chromecookies.KeyCache's doc).
func TestMistralVibeAPI_GrantRetriesAFailedKeychainRead(t *testing.T) {
	kc := &keychainStandIn{fail: true}
	api := testVibeAPI(t.TempDir(), kc)
	for range 2 {
		if _, err := api.browser.Key(t.Context()); err == nil {
			t.Fatal("the failing Keychain stand-in succeeded")
		}
	}
	if kc.reads.Load() != 1 {
		t.Fatalf("a failed Keychain read was retried %d time(s) before a new grant, want none", kc.reads.Load()-1)
	}
	mustRun(t, api.Start)
	_, _ = api.browser.Key(t.Context())
	if kc.reads.Load() != 2 {
		t.Fatalf("after a new grant: %d Keychain reads, want 2 — granting again did not retry the failed read", kc.reads.Load())
	}
}

// TestMistralVibeAPI_RevokeDropsTheKey (review of #2012, finding 4): a
// revoke drops the derived cookie key from memory, so a key read once is
// not kept while the permission is off.
func TestMistralVibeAPI_RevokeDropsTheKey(t *testing.T) {
	kc := &keychainStandIn{}
	api := testVibeAPI(t.TempDir(), kc)
	if _, err := api.browser.Key(t.Context()); err != nil {
		t.Fatal(err)
	}
	mustRun(t, api.Stop)
	if _, err := api.browser.Key(t.Context()); err != nil {
		t.Fatal(err)
	}
	if kc.reads.Load() != 2 {
		t.Fatalf("%d Keychain reads across a revoke, want 2 — the revoke kept the derived key cached", kc.reads.Load())
	}
}

// vibeGateRepo is a one-session repository whose listed reading follows the
// writes the sweep makes, so a not-granted tick sees what it must clear.
type vibeGateRepo struct {
	mu   sync.Mutex
	snap *session.RateLimitSnapshot
}

func (r *vibeGateRepo) ListAll() ([]*session.SessionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []*session.SessionState{{SessionID: "v1", Adapter: vibe.AdapterName,
		Metrics: &session.SessionMetrics{RateLimit: r.snap}}}, nil
}

func (r *vibeGateRepo) SetProviderRateLimit(_, _ string, snap *session.RateLimitSnapshot) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap = snap
	return true
}

func (r *vibeGateRepo) bound() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snap != nil && r.snap.Provider == session.ProviderMistral
}

// vibeOKTransport answers every fetch with the 2026-11-21 live body.
type vibeOKTransport struct{}

func (vibeOKTransport) Fetch(context.Context, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
	return outbound.AccountQuotaResponse{StatusCode: 200,
		Body: []byte(`[{"result":{"data":{"json":{"usage_percentage":0.081567,"payg_enabled":false,"reset_at":"2026-12-01T00:00:00Z"}}}}]`)}, nil
}

// TestMistralVibePermission_GateContract drives the REAL effects behind
// "mistral-vibe-browser-session/browser-session" (newMistralVibeAPI over a
// fake Chrome holding the committed fixture store) through the shared consent
// contract. The observed effect is what /api/v1/sessions shows: a vibe session
// carrying the Mistral reading. Exercise is one sweep tick. The sweeper here is
// built by hand over an atomic.Bool that SetState drives, so this checks the
// effects and the sweep's own gate, not the daemon's grant wiring;
// TestMistralVibeAPI_SweeperIsGatedOnTheDaemonsGrant covers that through the
// real sweeper() and a real PermissionService.
//
// Seen red by tools/lib/mistralvibe-mutations_test.sh case D9 (unbind no
// longer clears: revoked_effect_undone fails).
func TestMistralVibePermission_GateContract(t *testing.T) {
	api := testVibeAPI(fakeChromeDir(t), &keychainStandIn{})
	repo := &vibeGateRepo{}
	var granted atomic.Bool
	sweeper := services.NewMistralVibeSweeper(services.MistralVibeSweeperDeps{
		Sessions:  repo,
		Writer:    repo,
		Poller:    api.poller,
		Browser:   func() (services.MistralBrowserSession, error) { return api.browser.Session() },
		Transport: vibeOKTransport{},
		Granted:   granted.Load,
		Adapter:   vibe.AdapterName,
		Log:       e2eLog{},
		Interval:  time.Minute,
	})
	perm := mistralvibe.PermissionDeclaration(api.Start, api.Stop).Permissions[0]

	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		Key: perm.Key,
		// One permission on this declaration, so the held-open key is a
		// foreign one and the key-isolation arm repeats the revoked arm
		// (contracttesting.OnlyKey's doc). Wired because the contract admits
		// no opt-out.
		OtherKeys: []string{agent.HooksPermissionKey},
		SetState: contracttesting.OnlyKey(perm.Key, func(state permission.State) {
			granted.Store(state == permission.StateGranted)
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
		Exercise: func() { sweeper.RunOnce(context.Background()) },
		Observe:  repo.bound,
	})
}
