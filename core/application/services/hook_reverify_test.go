package services_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// --- #1372: installed hook entries are re-verified, and repaired, on a
// schedule — and the repair stays behind the same consent as the install.

var reverifyEpoch = time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

// fakeInstall is a hook install whose on-disk state the test drives directly.
// It counts verifies and repairs separately, because the two are gated
// separately and the whole point of scope item 4 is that a revoked permission
// stops BOTH — a test that only counted repairs would pass against a loop that
// went on reading the user's config after consent was withdrawn.
type fakeInstall struct {
	mu        sync.Mutex
	status    agent.HookEntryStatus
	verifyN   int
	repairN   int
	verifyErr error
	repairErr error
	// attempted is what Repair reports back. false models the permission
	// service refusing the write because consent is no longer granted at the
	// moment it took its lock.
	attempted bool
}

func newFakeInstall() *fakeInstall {
	return &fakeInstall{attempted: true}
}

func (f *fakeInstall) verify() (agent.HookEntryStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifyN++
	if f.verifyErr != nil {
		return agent.HookEntryStatus{}, f.verifyErr
	}
	return f.status, nil
}

func (f *fakeInstall) repair(string, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repairN++
	if !f.attempted {
		return false, nil
	}
	if f.repairErr != nil {
		return true, f.repairErr
	}
	f.status = agent.HookEntryStatus{}
	return true, nil
}

func (f *fakeInstall) damage(missing ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = agent.HookEntryStatus{Missing: missing}
}

func (f *fakeInstall) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.verifyN, f.repairN
}

func reverifyAgent(f *fakeInstall) agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: "testagent", DisplayName: "Test Agent"},
		Process:  agent.Process{Match: agent.ExactName{Name: "testagent"}},
		Permissions: []agent.Permission{
			{
				Key:    "hooks",
				Kind:   permission.KindModify,
				Title:  "Install hooks",
				Apply:  func() error { return nil },
				Remove: func() error { return nil },
				Writes: &agent.ManagedUserFile{
					Path:      func() (string, error) { return "/tmp/fake/settings.json", nil },
					Uninstall: func() (bool, error) { return false, nil },
					Verify:    f.verify,
				},
			},
		},
	}
}

// grantSwitch is a consent gate the test flips at will, standing in for the
// user revoking the permission mid-run.
type grantSwitch struct {
	mu      sync.Mutex
	granted bool
	asked   int
}

func (g *grantSwitch) fn(string, string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked++
	return g.granted
}

func (g *grantSwitch) set(v bool) {
	g.mu.Lock()
	g.granted = v
	g.mu.Unlock()
}

func (g *grantSwitch) askCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.asked
}

func newVerifier(f *fakeInstall, g *grantSwitch, log *mockLogger) *services.HookEntryVerifier {
	return services.NewHookEntryVerifier(services.HookEntryReverifyConfig{
		Interval:    5 * time.Minute,
		BackoffBase: 5 * time.Minute,
		BackoffMax:  40 * time.Minute,
		Agents:      []agent.Agent{reverifyAgent(f)},
		Granted:     g.fn,
		Repair:      f.repair,
		Log:         log,
	})
}

func outcome(t *testing.T, v *services.HookEntryVerifier, o services.ReverifyOutcome) uint64 {
	t.Helper()
	return v.Snapshot().Outcomes[o]
}

// The defect itself: entries removed by something outside irrlicht are found
// and put back, with no user gesture.
func TestReverify_RepairsExternallyDeletedEntries(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log)

	f.damage("Stop", "PermissionRequest")
	v.RunPassForTest(reverifyEpoch)

	verifies, repairs := f.counts()
	if verifies != 1 {
		t.Errorf("verify called %d times, want 1", verifies)
	}
	if repairs != 1 {
		t.Fatalf("repair called %d times, want 1 — entries deleted by the agent's own "+
			"settings UI must be re-installed without the user noticing (#1372)", repairs)
	}
	if got := outcome(t, v, services.ReverifyRepaired); got != 1 {
		t.Errorf("repaired counter = %d, want 1", got)
	}

	// And the repair converges: the next pass finds it intact and does nothing.
	v.RunPassForTest(reverifyEpoch.Add(time.Hour))
	if _, repairs := f.counts(); repairs != 1 {
		t.Errorf("repair called %d times after the install was restored, want 1 — a loop "+
			"that repairs an intact install writes to the user's config forever", repairs)
	}
	if got := outcome(t, v, services.ReverifyIntact); got != 1 {
		t.Errorf("intact counter = %d, want 1", got)
	}
}

func TestReverify_IntactInstallIsNeverWritten(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log)

	for i := 0; i < 5; i++ {
		v.RunPassForTest(reverifyEpoch.Add(time.Duration(i) * time.Hour))
	}
	if _, repairs := f.counts(); repairs != 0 {
		t.Errorf("repair called %d times on an intact install, want 0", repairs)
	}
	if got := outcome(t, v, services.ReverifyIntact); got != 5 {
		t.Errorf("intact counter = %d, want 5", got)
	}
	if row := v.Snapshot().Targets[0]; !row.Watched {
		t.Errorf("a granted, healthy target reads watched:false: %+v — the revoke test's "+
			"watched assertion would pass vacuously against a row that is never true", row)
	}
}

// --- scope item 4: a revoked permission stops the loop -------------------

// The load-bearing test. A background repair must never become a way to write
// to a config whose consent the user withdrew — that would break #570 more
// thoroughly than the bug it fixes.
//
// It asserts on the VERIFY count as well as the repair count. Reading the
// user's settings file is itself one of the things the permission's Touches
// text discloses, so a loop that keeps reading after a revoke is already
// outside consent even if it never writes again.
func TestReverify_RevokedPermissionStopsTheLoop(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log)

	// Establish that the loop is live and would otherwise repair.
	f.damage("Stop")
	v.RunPassForTest(reverifyEpoch)
	if _, repairs := f.counts(); repairs != 1 {
		t.Fatalf("precondition: granted loop did not repair (repairs=%d) — the revoke "+
			"assertion below would pass vacuously", repairs)
	}
	verifiesAtRevoke, repairsAtRevoke := f.counts()

	g.set(false)
	f.damage("Stop", "PermissionRequest")
	for i := 1; i <= 10; i++ {
		v.RunPassForTest(reverifyEpoch.Add(time.Duration(i) * time.Hour))
	}

	verifies, repairs := f.counts()
	if verifies != verifiesAtRevoke {
		t.Errorf("verify was called %d more times after the permission was revoked "+
			"(%d -> %d); re-verification READS the user's config, which the revoked "+
			"permission is exactly what withdrew consent for (#570/#1372)",
			verifies-verifiesAtRevoke, verifiesAtRevoke, verifies)
	}
	if repairs != repairsAtRevoke {
		t.Errorf("repair was called %d more times after the permission was revoked "+
			"(%d -> %d); a background repair must never write to a config the user "+
			"withdrew consent for", repairs-repairsAtRevoke, repairsAtRevoke, repairs)
	}
	if got := outcome(t, v, services.ReverifyNoConsent); got != 10 {
		t.Errorf("skipped_no_consent counter = %d, want 10 — the skip must be COUNTED, "+
			"not merely silent, or a bundle cannot distinguish 'not watched' from "+
			"'watched and healthy'", got)
	}
	if g.askCount() < 11 {
		t.Errorf("consent was consulted only %d times across 11 passes; it must be "+
			"re-read every pass, never cached", g.askCount())
	}
	// The published row must follow the consent, not lag it. Watched is derived
	// from the last outcome rather than stored, so this also pins that
	// derivation: a row reading watched:true beside last_outcome
	// "skipped_no_consent" would be an assertion no pass ever made.
	row := v.Snapshot().Targets[0]
	if row.Watched {
		t.Errorf("row still reads watched:true after the permission was revoked: %+v", row)
	}
	if row.LastOutcome != string(services.ReverifyNoConsent) {
		t.Errorf("last_outcome = %q, want %q", row.LastOutcome, services.ReverifyNoConsent)
	}
	if row.Repairs != 1 {
		t.Errorf("repairs = %d, want the 1 from before the revoke — a revoke must not erase "+
			"the lifetime record of what was already done", row.Repairs)
	}
}

// The narrower race: consent is granted when the loop reads, and gone by the
// time the permission service takes its lock to write. The service refuses
// (attempted=false) and the loop must treat that as a consent refusal, not as a
// failed repair to be counted, logged and backed off from.
func TestReverify_RevocationRacingTheWriteIsARefusalNotAFailure(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log)

	f.damage("Stop")
	f.mu.Lock()
	f.attempted = false // the service declines: state is no longer granted
	f.mu.Unlock()

	v.RunPassForTest(reverifyEpoch)

	if got := outcome(t, v, services.ReverifyNoConsent); got != 1 {
		t.Errorf("skipped_no_consent = %d, want 1: a refused write is a consent outcome", got)
	}
	if got := outcome(t, v, services.ReverifyRepaired); got != 0 {
		t.Errorf("repaired = %d, want 0: nothing was written", got)
	}
	if got := outcome(t, v, services.ReverifyRepairFailed); got != 0 {
		t.Errorf("repair_failed = %d, want 0: declining is not failing, and counting it "+
			"as a failure would put a fabricated error in the diagnostics bundle", got)
	}
	if errs := log.errorSnapshot(); len(errs) != 0 {
		t.Errorf("a consent refusal logged at error level: %v", errs)
	}
}

// End-to-end, through the REAL PermissionService and a REAL file on disk. The
// fake-based test above proves the loop asks; this proves the thing it asks
// actually refuses, and that the bytes on disk are untouched.
//
// This is the arm that would catch a loop wired to the adapter's Apply closure
// directly instead of through the service — which would pass every assertion in
// the fake-based test while writing to a revoked config.
func TestReverify_RevokedPermissionNeverTouchesTheFileOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	cfg := hookjson.Config{
		Path:       path,
		Sentinel:   "localhost:7837/api/v1/hooks/testagent",
		Events:     []string{"Stop"},
		MatcherFor: func(string) string { return "" },
		Entry: func() map[string]interface{} {
			return map[string]interface{}{"type": "http", "url": "http://localhost:7837/api/v1/hooks/testagent"}
		},
		IsCanonical: func(h map[string]interface{}) bool {
			u, _ := h["url"].(string)
			return u == "http://localhost:7837/api/v1/hooks/testagent"
		},
		WriteFile: func(p string, d []byte) error { return os.WriteFile(p, d, 0o600) },
	}

	decl := agent.Agent{
		Identity: agent.Identity{Name: "testagent", DisplayName: "Test Agent"},
		Process:  agent.Process{Match: agent.ExactName{Name: "testagent"}},
		Permissions: []agent.Permission{{
			Key:    "hooks",
			Kind:   permission.KindModify,
			Title:  "Install hooks",
			Apply:  func() error { _, err := hookjson.EnsureInstalled(cfg); return err },
			Remove: func() error { _, err := hookjson.Uninstall(cfg); return err },
			Writes: &agent.ManagedUserFile{
				Path:      func() (string, error) { return path, nil },
				Uninstall: func() (bool, error) { return hookjson.Uninstall(cfg) },
				Verify:    func() (agent.HookEntryStatus, error) { return hookjson.Verify(cfg) },
			},
		}},
	}

	log := &mockLogger{}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents: []agent.Agent{decl},
		Store:  &mockPermStore{},
		Push:   &mockPush{},
		Log:    log,
	})
	if err := svc.Answer([]services.PermissionAnswer{{Agent: "testagent", Permission: "hooks", Grant: true}}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	v := services.NewHookEntryVerifier(services.HookEntryReverifyConfig{
		Interval:    time.Minute,
		BackoffBase: time.Minute,
		BackoffMax:  time.Minute,
		Agents:      []agent.Agent{decl},
		Granted:     svc.Granted,
		Repair:      svc.RepairGrantedHookInstall,
		Log:         log,
	})

	// Granted: an external clobber is repaired.
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatalf("clobber: %v", err)
	}
	v.RunPassForTest(reverifyEpoch)
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(restored), "hooks/testagent") {
		t.Fatalf("precondition: granted loop did not restore the entries; file is %s", restored)
	}

	// Revoked: the same clobber is left exactly as the user's agent left it.
	if err := svc.Answer([]services.PermissionAnswer{{Agent: "testagent", Permission: "hooks", Grant: false}}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	clobbered := `{"theme":"dark","hooks":{}}`
	if err := os.WriteFile(path, []byte(clobbered), 0o600); err != nil {
		t.Fatalf("re-clobber: %v", err)
	}
	for i := 1; i <= 5; i++ {
		v.RunPassForTest(reverifyEpoch.Add(time.Duration(i) * time.Hour))
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != clobbered {
		t.Errorf("the re-verification loop wrote to a config whose permission was REVOKED.\n"+
			"want (untouched): %s\ngot:              %s\n"+
			"This is the #570 violation #1372 must not introduce: a background repair "+
			"that outlives consent is worse than the bug it fixes.", clobbered, after)
	}

	// Belt and braces: the service's own entry point refuses too, whatever the
	// loop does with the answer.
	attempted, err := svc.RepairGrantedHookInstall("testagent", "hooks")
	if attempted {
		t.Errorf("RepairGrantedHookInstall attempted a write on a denied permission (err=%v)", err)
	}
}

// A permission that was never granted at all must not be read even once. The
// revoke test above starts from granted; this one starts from pending, which is
// the state a fresh install sits in and the one #570 is strictest about.
func TestReverify_PendingPermissionIsNeverRead(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: false}, &mockLogger{}
	v := newVerifier(f, g, log)
	f.damage("Stop")

	for i := 0; i < 3; i++ {
		v.RunPassForTest(reverifyEpoch.Add(time.Duration(i) * time.Minute))
	}
	if verifies, repairs := f.counts(); verifies != 0 || repairs != 0 {
		t.Errorf("verify=%d repair=%d on a never-granted permission, want 0/0", verifies, repairs)
	}
}

// --- scope item 3: rate-limit and log ------------------------------------

// A repair loop fighting a live CLI that rewrites the same file every few
// seconds must back off, not spin. The damage here is never fixed — repair
// reports success and the next verify finds it broken again, which is exactly
// what a CLI holding a stale in-memory snapshot does.
func TestReverify_BacksOffWhenTheRepairKeepsLosing(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log)

	// Re-break the install after every repair.
	var passes int
	at := reverifyEpoch
	// Drive one pass per minute for four hours. Without back-off that is 240
	// writes to the user's config.
	for i := 0; i < 240; i++ {
		f.damage("Stop")
		v.RunPassForTest(at)
		at = at.Add(time.Minute)
		passes++
	}

	_, repairs := f.counts()
	if repairs >= 20 {
		t.Errorf("%d repairs across %d passes — the loop is spinning against a live "+
			"writer instead of backing off (#1372 scope item 3)", repairs, passes)
	}
	if repairs < 4 {
		t.Errorf("only %d repairs across four hours; the back-off must be a ceiling, "+
			"not a latch — a config clobbered once an hour still has to be fixed", repairs)
	}
	if got := outcome(t, v, services.ReverifyBackoff); got == 0 {
		t.Errorf("skipped_backoff counter is 0; a deferral that is not counted is " +
			"indistinguishable from a pass that never ran")
	}
}

// Back-off is per-episode, not permanent: once the install is found intact the
// cadence returns to normal, so the next clobber is caught promptly.
func TestReverify_BackoffResetsAfterAnIntactPass(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log)

	at := reverifyEpoch
	for i := 0; i < 3; i++ {
		f.damage("Stop")
		v.RunPassForTest(at)
		at = at.Add(time.Hour) // long enough to clear each deferral
	}
	if got := v.Snapshot().Targets[0].ConsecutiveRepairs; got != 3 {
		t.Fatalf("consecutive repairs = %d, want 3", got)
	}

	// One clean pass.
	v.RunPassForTest(at)
	row := v.Snapshot().Targets[0]
	if row.ConsecutiveRepairs != 0 {
		t.Errorf("consecutive repairs = %d after an intact pass, want 0", row.ConsecutiveRepairs)
	}
	if row.BackoffSeconds != 0 {
		t.Errorf("backoff still %ds after an intact pass; the episode is over", row.BackoffSeconds)
	}
}

// Repeated repairs are themselves a signal, so the first one of an episode is
// reported — and only the first, plus one storm line. A channel that stays
// under attack must not write a log line per pass forever, the same argument
// #1364 and #1368 make.
func TestReverify_LogsOncePerEpisodeNotOncePerRepair(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log)

	at := reverifyEpoch
	for i := 0; i < 12; i++ {
		f.damage("Stop")
		v.RunPassForTest(at)
		at = at.Add(2 * time.Hour) // clear every deferral, so every pass repairs
	}

	var repaired []string
	for _, line := range append(log.errorSnapshot(), log.infoSnapshot()...) {
		if strings.Contains(line, "#1372") {
			repaired = append(repaired, line)
		}
	}
	if len(repaired) == 0 {
		t.Fatalf("12 repairs produced no #1372 log line at all; repeated repairs are the " +
			"signal this feature exists to surface")
	}
	if len(repaired) > 3 {
		t.Errorf("12 repairs produced %d log lines; expected the first of the episode plus "+
			"at most a storm notice:\n%s", len(repaired), strings.Join(repaired, "\n"))
	}
	joined := strings.Join(repaired, "\n")
	if !strings.Contains(joined, "Stop") {
		t.Errorf("the log line does not name the affected event; a repair notice with no "+
			"events in it cannot be acted on:\n%s", joined)
	}
}

func TestReverify_RepairFailureIsCountedAndSurfaced(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log)

	f.damage("Stop")
	f.mu.Lock()
	f.repairErr = errors.New("settings.json is malformed")
	f.mu.Unlock()

	v.RunPassForTest(reverifyEpoch)

	if got := outcome(t, v, services.ReverifyRepairFailed); got != 1 {
		t.Errorf("repair_failed = %d, want 1", got)
	}
	row := v.Snapshot().Targets[0]
	if !strings.Contains(row.LastError, "malformed") {
		t.Errorf("LastError = %q, want the underlying reason", row.LastError)
	}
	// It must also back off: hammering a repair that cannot succeed is the same
	// spin the live-writer case produces, by a different route.
	if row.BackoffSeconds == 0 {
		t.Errorf("no back-off after a failed repair; the loop would retry every pass")
	}
}

func TestReverify_UnreadableConfigIsItsOwnOutcomeAndIsNotRepaired(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log)

	f.mu.Lock()
	f.verifyErr = errors.New("invalid character '}' looking for beginning of object key string")
	f.mu.Unlock()

	v.RunPassForTest(reverifyEpoch)

	if got := outcome(t, v, services.ReverifyUnreadable); got != 1 {
		t.Errorf("unreadable = %d, want 1", got)
	}
	if _, repairs := f.counts(); repairs != 0 {
		t.Errorf("repair called %d times on an unreadable config, want 0 — EnsureInstalled "+
			"refuses to overwrite malformed JSON too, so the loop would spin", repairs)
	}
}

// An adapter that declares a hook install but no Verify is skipped rather than
// crashing the loop for every other adapter. The registry tripwire
// (TestEveryHookInstallDeclaresAVerifier) is what stops that being permanent.
func TestReverify_AdapterWithoutAVerifierIsSkipped(t *testing.T) {
	decl := agent.Agent{
		Identity: agent.Identity{Name: "noverify", DisplayName: "No Verify"},
		Process:  agent.Process{Match: agent.ExactName{Name: "noverify"}},
		Permissions: []agent.Permission{{
			Key: "hooks", Kind: permission.KindModify, Title: "Install hooks",
			Apply:  func() error { return nil },
			Writes: &agent.ManagedUserFile{Path: func() (string, error) { return "/tmp/x", nil }},
		}},
	}
	f := newFakeInstall()
	v := services.NewHookEntryVerifier(services.HookEntryReverifyConfig{
		Agents:  []agent.Agent{decl, reverifyAgent(f)},
		Granted: func(string, string) bool { return true },
		Repair:  f.repair,
		Log:     &mockLogger{},
	})
	f.damage("Stop")
	v.RunPassForTest(reverifyEpoch)

	if got := len(v.Snapshot().Targets); got != 1 {
		t.Errorf("snapshot has %d targets, want 1 — an adapter with no Verify is not a "+
			"target, and publishing it as one would report a permanent zero", got)
	}
	if _, repairs := f.counts(); repairs != 1 {
		t.Errorf("the declaring adapter was not repaired (repairs=%d); one adapter's "+
			"missing declaration must not disable the loop for the rest", repairs)
	}
}

// The snapshot omits zero counters, per the package idiom (#1361/#1364): a map
// full of zeros reads as an all-clear from a loop that may never have run.
func TestReverify_SnapshotOmitsZeroOutcomes(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log)
	v.RunPassForTest(reverifyEpoch)

	got := v.Snapshot().Outcomes
	if _, present := got[services.ReverifyRepaired]; present {
		t.Errorf("Outcomes carries a zero %q entry: %v", services.ReverifyRepaired, got)
	}
	if got[services.ReverifyIntact] != 1 {
		t.Errorf("Outcomes[%q] = %d, want 1", services.ReverifyIntact, got[services.ReverifyIntact])
	}
}

// Every declaring target gets a row whether or not anything happened to it —
// the same argument #1368's Snapshot makes: a missing row is indistinguishable
// from a bundle collected before the feature existed.
func TestReverify_SnapshotPublishesARowPerTargetBeforeAnyPass(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log)

	rows := v.Snapshot().Targets
	if len(rows) != 1 {
		t.Fatalf("got %d rows before any pass, want 1", len(rows))
	}
	if rows[0].Adapter != "testagent" || rows[0].Permission != "hooks" {
		t.Errorf("row = %+v, want adapter testagent / permission hooks", rows[0])
	}
	if rows[0].ConfigPath == "" {
		t.Errorf("row carries no config path; the bundle reader has to be told WHICH file " +
			"is being repaired, or the finding is not actionable")
	}
}

// --- review follow-up ------------------------------------------------------

// An unreadable config must actually be deferred, not merely reported as
// deferred. The single-pass test above cannot see the difference: backoff is
// published from st.backoff, but the skip is decided from st.nextEligible, and
// setting only the first produces a bundle claiming a cooling-off window that
// does not exist while the file is re-parsed on every pass forever.
func TestReverify_UnreadableConfigIsActuallyDeferredNotJustReportedAs(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log) // BackoffBase 5m

	f.mu.Lock()
	f.verifyErr = errors.New("invalid character '}' looking for beginning of object key string")
	f.mu.Unlock()

	// Five passes one minute apart — all well inside the first deferral.
	for i := 0; i < 5; i++ {
		v.RunPassForTest(reverifyEpoch.Add(time.Duration(i) * time.Minute))
	}

	verifies, _ := f.counts()
	if verifies != 1 {
		t.Errorf("the config was re-parsed %d times across five passes inside the back-off "+
			"window, want 1 — a file we cannot parse will not become parseable by being read "+
			"again in a minute", verifies)
	}
	if got := outcome(t, v, services.ReverifyBackoff); got != 4 {
		t.Errorf("skipped_backoff = %d, want 4", got)
	}
	row := v.Snapshot().Targets[0]
	if row.BackoffSeconds == 0 {
		t.Errorf("backoff_seconds = 0 while deferring; the bundle must not understate it")
	}

	// And it escalates: a second unreadable pass after the window must defer
	// for longer, not reset to the base every time.
	v.RunPassForTest(reverifyEpoch.Add(6 * time.Minute))
	if got := v.Snapshot().Targets[0].BackoffSeconds; got <= row.BackoffSeconds {
		t.Errorf("backoff_seconds stayed at %ds after a second unreadable pass (was %ds); "+
			"a permanently broken config must not be re-read at the base cadence forever",
			got, row.BackoffSeconds)
	}
}

// A permanently failing repair — the shape a CLI below the #1365 version floor
// produces, where the version gate refuses every time — must not write an
// error line on every pass for the life of the daemon. Every sibling path in
// the file has a once-per-episode guard; this one is held to the same rule.
func TestReverify_RepairFailureLogsOncePerEpisode(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log)

	f.mu.Lock()
	f.repairErr = errors.New("installed CLI is 2.0.1, which is older than the 2.1.122 required")
	f.mu.Unlock()

	at := reverifyEpoch
	for i := 0; i < 12; i++ {
		f.damage("Stop")
		v.RunPassForTest(at)
		at = at.Add(2 * time.Hour) // clear every deferral, so every pass retries
	}

	var failures []string
	for _, line := range log.errorSnapshot() {
		if strings.Contains(line, "failed to re-install") {
			failures = append(failures, line)
		}
	}
	if len(failures) == 0 {
		t.Fatalf("12 failed repairs produced no error line at all")
	}
	if len(failures) > 1 {
		t.Errorf("12 failed repairs produced %d error lines; a repair that can never succeed "+
			"would write one per pass forever, which is the noise the once-per-episode rule "+
			"exists to prevent (#1364/#1368):\n%s", len(failures), strings.Join(failures, "\n"))
	}
	if got := outcome(t, v, services.ReverifyRepairFailed); got != 12 {
		t.Errorf("repair_failed = %d, want 12 — the COUNTER carries the volume that the log "+
			"deliberately does not", got)
	}
}

// After a successful repair the loop keeps Missing/Stale as the record of what
// the last verdict found, until an intact pass clears them. The diagnostics
// note must not read that residue as "the repair has not yet succeeded".
func TestReverify_ASuccessfulRepairIsNotReportedAsAFailedOne(t *testing.T) {
	f, g, log := newFakeInstall(), &grantSwitch{granted: true}, &mockLogger{}
	v := newVerifier(f, g, log)

	f.damage("Stop")
	v.RunPassForTest(reverifyEpoch)

	row := v.Snapshot().Targets[0]
	if row.LastOutcome != string(services.ReverifyRepaired) {
		t.Fatalf("last_outcome = %q, want %q", row.LastOutcome, services.ReverifyRepaired)
	}
	if len(row.Missing) == 0 {
		t.Fatalf("precondition: the row no longer carries the residue this test is about")
	}
	if row.LastError != "" {
		t.Errorf("a successful repair left last_error = %q", row.LastError)
	}
}
