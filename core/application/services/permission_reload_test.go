package services_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/permission"
	"irrlicht/core/ports/outbound"
)

// The reload path (#1425). The daemon reads permissions.json exactly once, at
// startup, so consent changed by ANOTHER process — today `irrlichd
// --uninstall-hooks` — was invisible to it until this existed.
//
// The end-to-end proof over two real processes is
// TestUninstallHooksAgainstLiveDaemon in core/cmd/irrlichd. These cover the
// semantics that test cannot reach from outside: what the reload refuses to
// do, and what it leaves alone.

// grantedStore returns a store already holding one granted permission, the
// state a daemon boots into when the user has answered the wizard.
func grantedStore(agentName, key string) *mockPermStore {
	set := permission.Set{}
	set.Put(agentName, key, permission.StateGranted)
	return &mockPermStore{set: set}
}

// reloadService builds a started service over the given store.
func reloadService(t *testing.T, store outbound.PermissionStore, c *effectCounter, mode string) (*services.PermissionService, *mockPush) {
	t.Helper()
	push := &mockPush{}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{testAgentDecl(c)},
		Store:     store,
		Push:      push,
		Log:       &mockLogger{},
		Mode:      mode,
		Registrar: &mockRegistrar{},
	})
	svc.Start(context.Background())
	return svc, push
}

// TestReloadAdoptsAnExternalDenial is the unit-level shape of #1425: the store
// says denied, the running service still says granted, and a reload has to
// close that gap — running the Remove closure, exactly as a restart would.
func TestReloadAdoptsAnExternalDenial(t *testing.T) {
	c := &effectCounter{}
	store := grantedStore("testagent", "config")
	svc, push := reloadService(t, store, c, config.PermissionModeAsk)

	if !svc.Granted("testagent", "config") {
		t.Fatal("precondition: a stored grant must be granted at startup")
	}
	appliedAtBoot, _ := c.counts()

	// Another process writes the opt-out.
	denied := permission.Set{}
	denied.Put("testagent", "config", permission.StateDenied)
	store.writeExternally(denied)

	changed, err := svc.ReloadFromStore()
	if err != nil {
		t.Fatalf("ReloadFromStore: %v", err)
	}
	if changed != 1 {
		t.Errorf("reload reported %d changes, want 1", changed)
	}
	if svc.Granted("testagent", "config") {
		t.Error("the service still reports granted after the store was set to denied — in-memory consent is stale (#1425)")
	}
	applied, removed := c.counts()
	if applied != appliedAtBoot {
		t.Errorf("reload re-ran Apply: applied %d → %d", appliedAtBoot, applied)
	}
	if removed != 1 {
		t.Errorf("Remove ran %d times, want 1 — a revocation must undo its effect", removed)
	}
	if n := push.count(outbound.PushTypePermissionsUpdated); n != 1 {
		t.Errorf("broadcast %d permissions_updated, want 1 — the wizards must re-render", n)
	}
}

// TestReloadAdoptsAnExternalGrant is the other direction, and the reason the
// reload is defined as "what a restart would do" rather than "revocations
// only": a restart would honour a stored grant, so a reload that ignored one
// would leave the two processes disagreeing in the opposite direction.
func TestReloadAdoptsAnExternalGrant(t *testing.T) {
	c := &effectCounter{}
	store := &mockPermStore{}
	svc, _ := reloadService(t, store, c, config.PermissionModeAsk)

	if svc.Granted("testagent", "config") {
		t.Fatal("precondition: an empty store grants nothing")
	}

	granted := permission.Set{}
	granted.Put("testagent", "config", permission.StateGranted)
	store.writeExternally(granted)

	if _, err := svc.ReloadFromStore(); err != nil {
		t.Fatalf("ReloadFromStore: %v", err)
	}
	if !svc.Granted("testagent", "config") {
		t.Error("a stored grant was not adopted")
	}
	if applied, _ := c.counts(); applied != 1 {
		t.Errorf("Apply ran %d times, want 1", applied)
	}
}

// TestReloadNeverGrantsWhatTheStoreDoesNotSay is the #570 guard, in the three
// ways the reload could otherwise open something the user never agreed to.
func TestReloadNeverGrantsWhatTheStoreDoesNotSay(t *testing.T) {
	t.Run("an emptied store revokes rather than opens", func(t *testing.T) {
		c := &effectCounter{}
		store := grantedStore("testagent", "config")
		svc, _ := reloadService(t, store, c, config.PermissionModeAsk)

		// The whole file gone — what a truncation or a deleted
		// permissions.json looks like from here.
		store.writeExternally(permission.Set{})

		if _, err := svc.ReloadFromStore(); err != nil {
			t.Fatalf("ReloadFromStore: %v", err)
		}
		if svc.Granted("testagent", "config") {
			t.Error("an empty store left a permission granted — an absent entry must read as pending, not as consent")
		}
		if _, removed := c.counts(); removed != 1 {
			t.Errorf("Remove ran %d times, want 1 — a permission that stopped saying yes must have its effect undone", removed)
		}
	})

	t.Run("an undeclared key in the store grants nothing", func(t *testing.T) {
		c := &effectCounter{}
		store := &mockPermStore{}
		svc, _ := reloadService(t, store, c, config.PermissionModeAsk)

		// A hand-edited or forward-dated permissions.json naming pairs this
		// binary does not declare.
		junk := permission.Set{}
		junk.Put("testagent", "not-a-declared-key", permission.StateGranted)
		junk.Put("not-an-agent", "config", permission.StateGranted)
		store.writeExternally(junk)

		changed, err := svc.ReloadFromStore()
		if err != nil {
			t.Fatalf("ReloadFromStore: %v", err)
		}
		if changed != 0 {
			t.Errorf("reload adopted %d undeclared permission(s), want 0", changed)
		}
		if svc.Granted("testagent", "not-a-declared-key") || svc.Granted("not-an-agent", "config") {
			t.Error("an undeclared pair was granted from the store")
		}
		if applied, _ := c.counts(); applied != 0 {
			t.Errorf("Apply ran %d times for undeclared permissions, want 0", applied)
		}
	})

	t.Run("a store read failure leaves the in-memory state alone", func(t *testing.T) {
		// An unparseable permissions.json must not be read as "nobody granted
		// anything" — that would revoke silently — nor as consent. The state
		// in memory stands and the caller is told.
		c := &effectCounter{}
		store := &failingLoadStore{
			set: grantedStore("testagent", "config").set,
			err: errors.New("permissions.json is not valid JSON"),
		}
		// NewPermissionService loads cleanly; the file goes bad afterwards, so
		// the flip has to happen between construction and the reload. That is
		// why this cannot use reloadService, which does both.
		svc := services.NewPermissionService(services.PermissionServiceDeps{
			Agents:    []agent.Agent{testAgentDecl(c)},
			Store:     store,
			Push:      &mockPush{},
			Log:       &mockLogger{},
			Mode:      config.PermissionModeAsk,
			Registrar: &mockRegistrar{},
		})
		store.failing = true
		svc.Start(context.Background())

		if !svc.Granted("testagent", "config") {
			t.Fatal("precondition: the stored grant was loaded at construction")
		}
		if _, err := svc.ReloadFromStore(); err == nil {
			t.Error("an unreadable store reported success")
		}
		if !svc.Granted("testagent", "config") {
			t.Error("an unreadable store revoked a grant — a failed read must change nothing")
		}
		if _, removed := c.counts(); removed != 0 {
			t.Errorf("a failed reload ran Remove %d times, want 0", removed)
		}
	})
}

// failingLoadStore serves a set until `failing` is flipped, then fails every
// Load — an unparseable or unreadable permissions.json that went bad after the
// daemon had already read it.
type failingLoadStore struct {
	set     permission.Set
	err     error
	failing bool
}

func (s *failingLoadStore) Load() (permission.Set, error) {
	if s.failing {
		return nil, s.err
	}
	return s.set.Clone(), nil
}
func (s *failingLoadStore) Save(permission.Set) error { return nil }

// TestReloadIsANoOpInGrantAllMode pins the one mode where the store is NOT the
// truth. Grant-all's grants are in-memory only and deliberately never
// persisted, so adopting the store would revoke every one of them and leave a
// recording or demo daemon monitoring nothing.
func TestReloadIsANoOpInGrantAllMode(t *testing.T) {
	c := &effectCounter{}
	store := &mockPermStore{}
	svc, push := reloadService(t, store, c, config.PermissionModeGrantAll)

	if !svc.Granted("testagent", "config") {
		t.Fatal("precondition: grant-all grants every declared permission at Start")
	}
	_, removedAtBoot := c.counts()

	changed, err := svc.ReloadFromStore()
	if err != nil {
		t.Fatalf("ReloadFromStore: %v", err)
	}
	if changed != 0 {
		t.Errorf("reload changed %d permission(s) in grant-all mode, want 0", changed)
	}
	if !svc.Granted("testagent", "config") {
		t.Error("reload revoked a grant-all permission — the empty store is not the truth in this mode")
	}
	if _, removed := c.counts(); removed != removedAtBoot {
		t.Errorf("reload ran Remove in grant-all mode: %d → %d", removedAtBoot, removed)
	}
	if n := push.count(outbound.PushTypePermissionsUpdated); n != 0 {
		t.Errorf("broadcast %d permissions_updated for a no-op reload, want 0", n)
	}
}

// TestReloadNeverWritesToTheStore pins the direction of the data flow. The
// store is the reload's input and never its output — a reload that wrote back
// could race Answer's memory-then-disk ordering into a lost update.
func TestReloadNeverWritesToTheStore(t *testing.T) {
	c := &effectCounter{}
	store := grantedStore("testagent", "config")
	svc, _ := reloadService(t, store, c, config.PermissionModeAsk)
	before := store.saveCount()

	denied := permission.Set{}
	denied.Put("testagent", "config", permission.StateDenied)
	store.writeExternally(denied)

	if _, err := svc.ReloadFromStore(); err != nil {
		t.Fatalf("ReloadFromStore: %v", err)
	}
	if after := store.saveCount(); after != before {
		t.Errorf("reload saved to the store: %d → %d", before, after)
	}
}

// TestReloadWithNothingChangedIsSilent keeps the nudge cheap: the CLI sends it
// unconditionally, and a daemon that already agrees with the store must not
// re-run effects or wake both wizards for nothing.
func TestReloadWithNothingChangedIsSilent(t *testing.T) {
	c := &effectCounter{}
	store := grantedStore("testagent", "config")
	svc, push := reloadService(t, store, c, config.PermissionModeAsk)
	appliedAtBoot, removedAtBoot := c.counts()

	changed, err := svc.ReloadFromStore()
	if err != nil {
		t.Fatalf("ReloadFromStore: %v", err)
	}
	if changed != 0 {
		t.Errorf("reload reported %d changes against an unchanged store, want 0", changed)
	}
	applied, removed := c.counts()
	if applied != appliedAtBoot || removed != removedAtBoot {
		t.Errorf("reload re-ran effects: applied %d→%d, removed %d→%d", appliedAtBoot, applied, removedAtBoot, removed)
	}
	if n := push.count(outbound.PushTypePermissionsUpdated); n != 0 {
		t.Errorf("broadcast %d permissions_updated for a no-op reload, want 0", n)
	}
}

// slowSaveStore delays between serving a Load and accepting the Save, widening
// the window in which Answer has moved memory but not yet persisted. That
// window is real in production — Answer runs a `claude --version` probe and a
// settings.json rewrite between its unlock and its Save — this just makes it
// deterministic.
type slowSaveStore struct {
	mu       sync.Mutex
	set      permission.Set
	saveHold time.Duration
}

func (s *slowSaveStore) Load() (permission.Set, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set.Clone(), nil
}

func (s *slowSaveStore) Save(set permission.Set) error {
	time.Sleep(s.saveHold)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.set = set.Clone()
	return nil
}

func (s *slowSaveStore) current() permission.Set {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set.Clone()
}

// TestReloadDoesNotResurrectAConcurrentDenial is the #570 arm of the race a
// reviewer found in the first version of ReloadFromStore.
//
// A reload that reads the store while an Answer has moved memory to denied but
// not yet saved would adopt the stale "granted" straight back — re-running
// Apply on a permission the user had just refused, and leaving memory and disk
// permanently disagreeing. Not writing to the store is not sufficient to
// prevent that; serializing the two whole sequences is.
func TestReloadDoesNotResurrectAConcurrentDenial(t *testing.T) {
	granted := permission.Set{}
	granted.Put("testagent", "config", permission.StateGranted)
	store := &slowSaveStore{set: granted, saveHold: 150 * time.Millisecond}

	c := &effectCounter{}
	svc, _ := reloadService(t, store, c, config.PermissionModeAsk)
	if !svc.Granted("testagent", "config") {
		t.Fatal("precondition: the stored grant is in memory")
	}

	// The user denies it in the wizard...
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := svc.Answer([]services.PermissionAnswer{
			{Agent: "testagent", Permission: "config", Grant: false},
		}); err != nil {
			t.Errorf("Answer: %v", err)
		}
	}()

	// ...while another process nudges the daemon to re-read the store, landing
	// inside Answer's not-yet-saved window.
	time.Sleep(40 * time.Millisecond)
	if _, err := svc.ReloadFromStore(); err != nil {
		t.Fatalf("ReloadFromStore: %v", err)
	}
	wg.Wait()

	if svc.Granted("testagent", "config") {
		t.Error("a reload racing a denial resurrected the grant — the user's refusal was undone (#570)")
	}
	if got := store.current().Get("testagent", "config"); got != permission.StateDenied {
		t.Errorf("store holds %q, want denied", got)
	}
	// The end state must be self-consistent: memory and disk agreeing, and the
	// permission's effect removed rather than re-applied.
	if c.lastEffect() != "remove" {
		t.Errorf("last effect was %q, want remove — a denied permission must not end up applied", c.lastEffect())
	}
}
