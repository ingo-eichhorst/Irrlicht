// PermissionService is the single source of truth for the consent-first
// permission wizard (issue #570). Every read and modification irrlicht
// performs on the user's system is declared as a per-agent permission
// (agent.Permission); this service holds the answered state, exercises
// grants (installs hooks, starts watchers), actively undoes revokes, and
// arbitrates between the macOS and web wizards — the first answer wins
// and a PushTypePermissionsUpdated broadcast dismisses the other surface.
package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/permission"
	"irrlicht/core/pkg/cliprobe"
	"irrlicht/core/pkg/cliversion"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// detectionPollInterval is how often the always-on detection poller checks
// for live agent processes. Detection is the consent-free baseline: it only
// feeds the wizard's "detected" flag and never creates sessions.
const detectionPollInterval = 5 * time.Second

// ErrUnknownPermission is returned by Answer for an agent/permission pair
// that no adapter declares. The HTTP handler maps it to 400.
var ErrUnknownPermission = errors.New("unknown agent or permission")

// WatcherFactory constructs the observe-gated watchers for one agent.
// Called fresh on every grant — watchers hold per-run state (e.g. the
// scanner's tracked-PID map) and are rebuilt rather than restarted.
type WatcherFactory func() []inbound.Watcher

// watcherRegisterer is the slice of *SessionDetector the service needs:
// registering a watcher's event stream with the running detector.
type watcherRegisterer interface {
	AddWatcher(ctx context.Context, w inbound.Watcher)
}

// HasLiveProcessFunc reports whether any running process matches m.
// Production injects processlifecycle.HasLiveProcess.
type HasLiveProcessFunc func(m agent.ProcessMatcher) bool

// PermissionAnswer is one user decision submitted via
// POST /api/v1/permissions/answer.
type PermissionAnswer struct {
	Agent      string `json:"agent"`
	Permission string `json:"permission"`
	Grant      bool   `json:"grant"`
}

// PermissionView is the API projection of one declared permission plus
// its current consent state.
type PermissionView struct {
	Key             string `json:"key"`
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Title           string `json:"title"`
	FeatureUnlocked string `json:"feature_unlocked"`
	Touches         string `json:"touches"`
	Detail          string `json:"detail"`

	// EffectError is the reason the consent effect for the CURRENT state
	// failed, or empty when it succeeded (or the permission declares no
	// closure). It is deliberately a field of its own rather than a fourth
	// State: "granted" and "applied" are two different facts, and a
	// transient filesystem error must never look like a withdrawn consent
	// (#1362). A granted permission carrying an EffectError means the user
	// said yes and the hooks are NOT installed; a denied one means the
	// user said no and the hooks may still BE installed.
	//
	// Not persisted. Start re-runs every GRANTED permission's effect, so an
	// apply failure is recomputed from scratch each boot rather than
	// risking a stale failure outliving the condition that caused it.
	//
	// Known asymmetry: Start does not re-run Remove for denied
	// permissions, so a remove failure is visible only until the next
	// daemon restart, after which the permission reads as a clean "denied"
	// while the modification may still be on disk. That gap is
	// pre-existing — before #1362 a failed Remove left no trace at all —
	// and closing it means reconciling denied effects at Start too, which
	// is a behaviour change beyond this fix.
	EffectError string `json:"effect_error,omitempty"`
}

// AgentPermissions groups one agent's permissions for the API.
type AgentPermissions struct {
	Name        string           `json:"name"`
	DisplayName string           `json:"display_name"`
	Detected    bool             `json:"detected"`
	Permissions []PermissionView `json:"permissions"`
}

// UnappliedGrant is one permission the user GRANTED whose consent effect
// is not in force: the consent stands, the modification is absent. It is
// the element of PermissionsSnapshot.UnappliedGrants — see that field for
// what is and is not counted.
//
// It carries the reason verbatim rather than a classification code. The
// "granted but not applied" family already contains more than one
// diagnosis — a failed install (#1362) and a refusal because the upstream
// CLI is below the declared version floor (#1365), which rides the same
// effect-error path on purpose — and the reason string is what tells them
// apart today, in both wizards. An aggregate that replaced it with a
// single enum would be the collapse this field exists to avoid.
type UnappliedGrant struct {
	Agent            string `json:"agent"`
	AgentDisplayName string `json:"agent_display_name"`
	Key              string `json:"key"`
	Title            string `json:"title"`
	// Reason is the verbatim effect error, identical to the matching
	// PermissionView.EffectError. Duplicated rather than referenced so a
	// passive surface can render the whole list without walking Agents.
	Reason string `json:"reason"`
}

// PermissionsSnapshot is the GET /api/v1/permissions response body.
type PermissionsSnapshot struct {
	Mode   string             `json:"mode"`
	Agents []AgentPermissions `json:"agents"`

	// UnappliedGrants is the aggregate a surface renders PASSIVELY as
	// "N permissions are granted but not applied" (#1385). Before it,
	// #1362's per-permission EffectError reached only a user who opened
	// the wizard — so a re-apply failure at Start, when every granted
	// effect is re-run at once, was invisible to everyone else. Both the
	// web dashboard and the macOS app read this one daemon-computed list
	// rather than each re-deriving it.
	//
	// It is deliberately a LIST, not a count: the headline aggregates, but
	// a user who clicks through has to be able to tell which permission
	// and why. len() is the count.
	//
	// Scope — a permission is here when it is GRANTED and its last effect
	// failed, and only then:
	//   - a denied permission whose Remove failed is the opposite fault
	//     ("revoked, but not undone") and would make this headline lie;
	//   - a pending permission is not a grant, so it cannot be a GRANTED-
	//     but-unapplied one. Note it is not true that pending never ran an
	//     effect: ReloadFromStore moves a permission whose pair vanished
	//     from the store back to pending and runs Remove for that move, and
	//     recordEffectResult stores the outcome either way.
	//
	// Scope against the other "silently unapplied" diagnoses, which are
	// NOT folded in and must stay distinguishable:
	//   - #1362 install FAILED, and #1365 refused below the version floor:
	//     both are here, told apart by Reason.
	//   - #1372 entries went MISSING and are re-installed on a timer:
	//     reported in the diagnostics bundle's hooks.json
	//     (entry_reverification). A repair that itself fails becomes an
	//     ordinary effect error and does arrive here; a successful repair
	//     never does, because nothing is unapplied afterwards.
	//   - #1368 entries present but nothing ARRIVING: the liveness
	//     watchdog, reported in hooks.json (channels). The entries exist,
	//     so the grant IS applied — counting it here would accuse the
	//     install of a fault it does not have.
	//   - #1425 in-memory consent STALE relative to the store: a
	//     reconciliation, not a standing fault. After ReloadFromStore any
	//     resulting failure arrives here through the ordinary path.
	//
	// Omitted from the JSON when empty, so a healthy daemon's response is
	// byte-identical to what it was before this field existed.
	UnappliedGrants []UnappliedGrant `json:"unapplied_grants,omitempty"`
}

// PermissionService gates every agent-monitoring capability behind user
// consent. Construct with NewPermissionService, then call Start once the
// daemon context exists.
type PermissionService struct {
	agents    []agent.Agent
	store     outbound.PermissionStore
	push      outbound.PushBroadcaster
	log       outbound.Logger
	mode      string
	registrar watcherRegisterer

	// isolatedHome is the directory this daemon was GIVEN as its own
	// (IRRLICHT_HOME), or "" when it was given none. Together with
	// allowSharedConfigWrites it is everything sharedConfigRefusal needs; see
	// that method for what "isolated" means here and why it is not the same
	// question as "is IRRLICHT_HOME set".
	isolatedHome            string
	allowSharedConfigWrites bool

	factories map[string]WatcherFactory
	hasLive   HasLiveProcessFunc

	// detectInterval is detectionPollInterval in production; tests shorten
	// it via SetDetectionPollIntervalForTest before Start.
	detectInterval time.Duration

	// probes overrides process-matcher detection for agents whose presence
	// is detected another way (Gas Town: GT_ROOT directory probe). Set via
	// SetDetectionProbe before Start.
	probes map[string]func() bool

	// storeMu serializes a whole read-modify-write of the consent store
	// against a whole read-adopt of it: Answer's mutate-then-Save and
	// ReloadFromStore's Load-then-adopt. Neither is atomic on its own, and
	// interleaving them can resurrect a grant the user just refused —
	// Answer moves memory to denied and has not saved yet when a reload
	// Loads the still-granted file and writes it back over the top, leaving
	// memory granted, disk denied, and the effect re-applied (#1425/#570).
	//
	// Deliberately coarser and OUTSIDE mu: it is held across file I/O, which
	// mu must never be, so Granted() on the hook hot path never waits on it.
	// Lock order: storeMu before effectMu before mu. Nothing taken under
	// either of the inner two may take storeMu.
	storeMu sync.Mutex

	// effectMu serializes effect execution (hook installs, watcher
	// start/stop). Effects run OUTSIDE mu so the hot Granted() gate never
	// blocks on file I/O; this mutex keeps two concurrent Answer batches
	// from racing the same installer. Lock order: effectMu before mu —
	// nothing under mu may take effectMu.
	effectMu sync.Mutex

	// mu guards the consent state below. Never held across file I/O or
	// broadcasts — Granted() sits on the hook/statusline hot path.
	mu       sync.Mutex
	set      permission.Set
	detected map[string]bool
	watching map[string]context.CancelFunc // agent name → cancel for its running watchers
	// effectErrs maps "agent/key" → the reason that permission's last
	// consent effect failed. Populated and cleared by runClosureEffect,
	// read by Snapshot, and consulted by Answer so a re-grant retries a
	// failed Apply instead of short-circuiting as a no-op (#1362).
	// Deliberately separate from set: it records what the SYSTEM failed to
	// do, never what the USER decided.
	effectErrs map[string]string
	// parent is intentionally stored rather than threaded through as a method
	// parameter (godre:S8242): it is the daemon-lifetime ctx set once by
	// Start, and every watcher startWatching starts later — triggered by an
	// Answer() call arriving on its own short-lived HTTP request — must be
	// bounded by the daemon's lifetime, not by that unrelated request's.
	parent context.Context
}

// pendingEffect is one consent effect collected under mu and executed
// outside it.
type pendingEffect struct {
	agentName string
	perm      agent.Permission
	target    permission.State
}

// SetDetectionPollIntervalForTest overrides the detection poll cadence.
// Call before Start.
func (s *PermissionService) SetDetectionPollIntervalForTest(d time.Duration) {
	s.detectInterval = d
}

// PermissionServiceDeps bundles NewPermissionService's dependencies.
// Factories may be nil (demo mode: no watchers ever start); HasLive may be
// nil (no detection poller — nothing is ever "detected").
type PermissionServiceDeps struct {
	Agents    []agent.Agent
	Store     outbound.PermissionStore
	Push      outbound.PushBroadcaster
	Log       outbound.Logger
	Mode      string
	Registrar watcherRegisterer
	Factories map[string]WatcherFactory
	HasLive   HasLiveProcessFunc

	// IsolatedHome is IRRLICHT_HOME, "" when unset, and
	// AllowSharedConfigWrites is IRRLICHT_ALLOW_SHARED_CONFIG_WRITES=1. Both
	// feed the grant-all shared-config guard (#1449) and are inert in ask mode.
	IsolatedHome            string
	AllowSharedConfigWrites bool
}

// newPermissionService allocates a PermissionService with every field the
// service OWNS already usable: the five maps it writes to, and the detection
// cadence (time.NewTicker panics on a zero interval, so the zero value is not
// merely nil-unsafe, it is unstartable). Dependencies are left to the caller.
//
// This is the ONLY place a PermissionService composite literal may appear, and
// that is enforced mechanically rather than by memory —
// TestPermissionServiceIsNeverBuiltByBareLiteral scans this package's source
// for any other one. A bypass is worth catching at the literal because it does
// not fail there: it fails later, in whichever effect first writes a map, with
// a stack pointing at recordEffectResult and nothing at all pointing at the
// construction that actually caused it (#1400). Every field added here of map,
// or channel type re-arms that, which is why the rule is about the
// construction site rather than about any one field.
func newPermissionService() *PermissionService {
	return &PermissionService{
		detectInterval: detectionPollInterval,
		probes:         make(map[string]func() bool),
		set:            permission.Set{},
		detected:       make(map[string]bool),
		watching:       make(map[string]context.CancelFunc),
		effectErrs:     make(map[string]string),
	}
}

// NewPermissionService loads the persisted consent state and returns the
// service.
func NewPermissionService(deps PermissionServiceDeps) *PermissionService {
	set, err := deps.Store.Load()
	if err != nil {
		deps.Log.LogError("permissions", "", fmt.Sprintf("failed to load permission state (treating all as pending): %v", err))
		set = nil
	}
	s := newPermissionService()
	s.agents = deps.Agents
	s.store = deps.Store
	s.push = deps.Push
	s.log = deps.Log
	s.mode = deps.Mode
	s.registrar = deps.Registrar
	s.factories = deps.Factories
	s.hasLive = deps.HasLive
	s.isolatedHome = deps.IsolatedHome
	s.allowSharedConfigWrites = deps.AllowSharedConfigWrites
	// permission.Set is itself map-backed and Put writes to it, so a nil Set —
	// which a store may return alongside a nil error — is the same nil-map trap
	// one level down. Keep the allocator's empty one rather than adopting it.
	if set != nil {
		s.set = set
	}
	return s
}

// effectKey is the composite key for the effectErrs map.
func effectKey(agentName, permKey string) string {
	return agentName + "/" + permKey
}

// SetDetectionProbe overrides process-matcher detection for one agent —
// used for adapters detected by filesystem presence rather than a live
// process (Gas Town's GT_ROOT). Call before Start.
func (s *PermissionService) SetDetectionProbe(agentName string, probe func() bool) {
	s.probes[agentName] = probe
}

// Start exercises every granted permission (re-applies modify effects so
// stale hook entries are upgraded, starts watchers for observe grants) and
// launches the detection poller. In grant-all mode every declared
// permission is granted in memory first — without persisting, so a later
// ask-mode daemon still sees the user's real answers. Effects run
// synchronously so a grant-all daemon is fully monitoring before the
// caller proceeds (recording fixtures must not race the grant).
//
// The detection poller only runs in ask mode and only while some
// permission is still pending: at runtime pending-ness can only decrease
// (answers move to granted/denied, never back), so once nothing is
// pending there is no wizard to trigger and the per-agent process scans
// would be pure waste. Grant-all suppresses the wizard entirely, so it
// never polls.
func (s *PermissionService) Start(ctx context.Context) {
	s.mu.Lock()
	s.parent = ctx
	if s.mode == config.PermissionModeGrantAll {
		for _, a := range s.agents {
			for _, p := range a.Permissions {
				s.set.Put(a.Identity.Name, p.Key, permission.StateGranted)
			}
		}
		s.log.LogInfo("permissions", "", "IRRLICHT_PERMISSION_MODE=grant-all — all permissions granted, wizard suppressed")
	}
	var effects []pendingEffect
	for _, a := range s.agents {
		for _, p := range a.Permissions {
			if s.set.Get(a.Identity.Name, p.Key) == permission.StateGranted {
				effects = append(effects, pendingEffect{a.Identity.Name, p, permission.StateGranted})
			}
		}
	}
	runPoller := s.hasLive != nil && s.mode != config.PermissionModeGrantAll && s.anyPendingLocked()
	s.mu.Unlock()

	s.runEffects(effects)

	if runPoller {
		go s.runDetectionLoop(ctx)
	}
}

// anyPendingLocked reports whether any declared permission is still
// unanswered. Caller holds s.mu.
func (s *PermissionService) anyPendingLocked() bool {
	for _, a := range s.agents {
		for _, p := range a.Permissions {
			if s.set.Get(a.Identity.Name, p.Key) == permission.StatePending {
				return true
			}
		}
	}
	return false
}

// Granted reports whether the agent/key permission is currently granted.
// Used as the consent gate by the hook/statusline HTTP handlers.
func (s *PermissionService) Granted(agentName, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set.Get(agentName, key) == permission.StateGranted
}

// HookChannelReady reports whether agentName's hook channel is EXPECTED to
// deliver: it declares at least one hook-install permission that is granted and
// whose apply effect did not fail (issue #1368).
//
// This is the daemon's best knowledge that our entries are in the agent's
// config, and it is deliberately built from what the daemon DID rather than
// from re-reading the file. Re-reading would cost a JSON parse per turn on the
// classify path, and it would answer a different question anyway — #1372's
// question, "are the entries still there", which is a separate diagnosis with a
// separate fix.
//
// The effect-failure term is load-bearing, not defensive. A granted permission
// whose Apply failed means the user said yes and the hooks are NOT installed
// (#1362), so this must read false — see hook_liveness.go's header for why
// convicting that channel of silence would report a known failure in weaker
// words and against the wrong issue.
func (s *PermissionService) HookChannelReady(agentName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.agents {
		if a.Identity.Name != agentName {
			continue
		}
		for _, p := range a.Permissions {
			// Narrowed to the hooks key, not merely "declares a managed
			// file": #1383 widened Writes to every shared user-owned file a
			// permission writes, so claudecode's statusline and instruction
			// blocks (and kitty's remote-control patch) now declare one too.
			// Asking only "is some managed file granted" would report the
			// hook channel ready on an adapter whose hooks were denied.
			if p.Key != agent.HooksPermissionKey || p.Writes == nil {
				continue
			}
			if s.set.Get(agentName, p.Key) == permission.StateGranted && !s.effectFailedLocked(agentName, p.Key) {
				return true
			}
		}
	}
	return false
}

// RepairGrantedHookInstall re-runs one hook permission's install effect out of
// band, for the periodic entry re-verification loop (issue #1372).
//
// It reports whether the effect was ATTEMPTED, and if so whether it succeeded.
// attempted=false is the consent refusal — the permission is not currently
// granted, is unknown, or declares no hook install — and it is not an error:
// declining to write is the correct outcome, not a failure to be retried.
//
// There are two consent checks on this path and they cover different windows.
// The pre-check below is the one that refuses every ordinary revoked caller —
// it is a real gate, not decoration, and deleting it is visible as a red test.
// But it can only be as fresh as the instant it was asked, and the write that
// follows may wait on a mutex first. So the write itself is not performed here:
// it goes through runEffects, exactly as a wizard answer does, which means the
// background repair inherits every guarantee the interactive path already had
// and cannot drift from it:
//
//   - runEffects takes effectMu, so a repair cannot race a concurrent Answer
//     batch into the same installer;
//   - under effectMu it re-reads the recorded state and SKIPS any effect whose
//     target no longer matches. That third read is what makes revocation
//     authoritative rather than eventually noticed: consent withdrawn at any
//     point before the closure runs means the closure does not run, and the
//     skip is logged. It is the only one of the three that a user revoking
//     WHILE a repair waits on the lock can be caught by, and
//     TestRunEffects_SkipsAGrantEffectWhoseStateIsNoLongerGranted pins it;
//   - runClosureEffect consults the declared version floor (#1365) before
//     applying, so a repair cannot install into a CLI too old for the entries;
//   - the outcome lands in effectErrs, so a repair that FAILS surfaces as the
//     permission's EffectError and is retried by the user re-answering — the
//     #1362 mechanism, not a second one. That also drops HookChannelReady to
//     false, which correctly disarms #1368's liveness watchdog for an adapter
//     whose install is known broken.
//
// The pre-check below also keeps the common case — nothing granted, nothing to
// do — off effectMu entirely, so an unanswered wizard costs the loop one map
// read per pass rather than a lock and a log line.
func (s *PermissionService) RepairGrantedHookInstall(agentName, key string) (bool, error) {
	s.mu.Lock()
	perm, found := s.hookPermission(agentName, key)
	granted := found && s.set.Get(agentName, key) == permission.StateGranted
	s.mu.Unlock()
	if !granted || perm.Apply == nil {
		return false, nil
	}

	if ran := s.runEffects([]pendingEffect{{agentName: agentName, perm: perm, target: permission.StateGranted}}); ran == 0 {
		// Gate 3 fired: consent went away while this repair waited on effectMu,
		// so nothing was written. Report it as NOT ATTEMPTED, which is what the
		// caller means by that word — the alternative is worse than cosmetic.
		// effectErrs is untouched by a skip, so the read below would return ""
		// (or, worse, a stale error from a previous attempt) and the loop would
		// bank a repair that never happened: a lifetime counter, a log line
		// blaming something outside irrlicht for a deletion, and an armed
		// back-off, all describing a write the service refused to make.
		return false, nil
	}

	// Read the outcome out of the same slot the interactive path writes, rather
	// than having runEffects hand it back: one recorded truth, which is what the
	// wizard renders and what HookChannelReady reads.
	s.mu.Lock()
	reason := s.effectErrs[effectKey(agentName, key)]
	s.mu.Unlock()
	if reason != "" {
		return true, errors.New(reason)
	}
	return true, nil
}

// hookPermission resolves one agent's HOOK-INSTALL permission by key.
//
// A thin filter over declared() rather than a second registry walk: two copies
// of "find the permission named key on the agent named agentName" would be two
// places to keep in step, and the filter is the only part that is actually
// specific to this caller.
//
// The filter is the point. Restricted to modify-kind permissions under the
// hooks key that declare a managed user file, so RepairGrantedHookInstall can
// never be used to re-run an arbitrary effect by name: the re-verification
// loop is allowed to repair hook installs and nothing else. The hooks-key
// clause is load-bearing since #1383 widened Writes from "the hook config" to
// every managed user file — without it this would also admit the instruction
// and kitty permissions, which is precisely the arbitrary-effect re-run the
// filter exists to prevent.
//
// Takes no lock and needs none — s.agents is written once at construction and
// only read after. Named without the Locked suffix for that reason.
func (s *PermissionService) hookPermission(agentName, key string) (agent.Permission, bool) {
	p, ok := s.declared(agentName, key)
	if !ok || p.Writes == nil || p.Key != agent.HooksPermissionKey || p.Kind != permission.KindModify {
		return agent.Permission{}, false
	}
	return p, true
}

// Snapshot returns the full consent state for GET /api/v1/permissions.
func (s *PermissionService) Snapshot() PermissionsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := PermissionsSnapshot{Mode: s.mode}
	for _, a := range s.agents {
		if len(a.Permissions) == 0 {
			continue
		}
		ap, unapplied := s.agentViewLocked(a)
		out.Agents = append(out.Agents, ap)
		out.UnappliedGrants = append(out.UnappliedGrants, unapplied...)
	}
	return out
}

// agentViewLocked projects one agent's permissions, returning both the
// per-agent view and that agent's contribution to the snapshot-wide
// UnappliedGrants aggregate. The two are built in one pass because they read
// the same two lookups per permission. Caller holds s.mu.
func (s *PermissionService) agentViewLocked(a agent.Agent) (AgentPermissions, []UnappliedGrant) {
	ap := AgentPermissions{
		Name:        a.Identity.Name,
		DisplayName: a.Identity.DisplayName,
		Detected:    s.detected[a.Identity.Name],
	}
	var unapplied []UnappliedGrant
	for _, p := range a.Permissions {
		state := s.set.Get(a.Identity.Name, p.Key)
		reason := s.effectErrs[effectKey(a.Identity.Name, p.Key)]
		ap.Permissions = append(ap.Permissions, PermissionView{
			Key:             p.Key,
			Kind:            string(p.Kind),
			State:           string(state),
			Title:           p.Title,
			FeatureUnlocked: p.FeatureUnlocked,
			Touches:         p.Touches,
			Detail:          p.Detail,
			EffectError:     reason,
		})
		// Granted-only: see UnappliedGrants for why a failed Remove and a
		// pending permission (which CAN carry an effect error) are both
		// excluded.
		if state == permission.StateGranted && reason != "" {
			unapplied = append(unapplied, UnappliedGrant{
				Agent:            a.Identity.Name,
				AgentDisplayName: a.Identity.DisplayName,
				Key:              p.Key,
				Title:            p.Title,
				Reason:           reason,
			})
		}
	}
	return ap, unapplied
}

// Answer applies a batch of user decisions: state is recorded, modify
// effects are applied/undone, observe watchers are started/stopped, the
// set is persisted, and a permissions_updated broadcast dismisses the
// wizard on whichever surface didn't answer. Re-answering with the same
// state is a no-op, so the losing surface's duplicate submission is
// harmless — unless that permission's effect FAILED, in which case the
// identical answer is a deliberate retry and re-runs the closure (#1362).
// Returns ErrUnknownPermission (wrapped) when any entry names an
// undeclared agent/permission pair; nothing is applied in that case.
//
// State mutation happens under s.mu, but effects, persistence, and the
// broadcast run after release — Granted() gates every hook/statusline
// POST and must never wait on file I/O. In grant-all mode nothing is
// persisted: the auto-granted set is in-memory only, and saving it here
// would leak every auto-grant into a later ask-mode daemon's state.
func (s *PermissionService) Answer(answers []PermissionAnswer) error {
	if len(answers) == 0 {
		return nil
	}
	// Held for the whole mutate-then-Save, so a concurrent ReloadFromStore
	// cannot read the pre-answer file and adopt it back over this decision.
	// See the storeMu field comment.
	s.storeMu.Lock()
	defer s.storeMu.Unlock()

	s.mu.Lock()

	// Validate the whole batch before mutating so a malformed entry can't
	// half-apply.
	perms := make([]agent.Permission, len(answers))
	for i, ans := range answers {
		p, ok := s.declared(ans.Agent, ans.Permission)
		if !ok {
			s.mu.Unlock()
			return fmt.Errorf("%w: %s/%s", ErrUnknownPermission, ans.Agent, ans.Permission)
		}
		perms[i] = p
	}

	var effects []pendingEffect
	stateChanged := false
	for i, ans := range answers {
		target := permission.StateDenied
		if ans.Grant {
			target = permission.StateGranted
		}
		moved, run := s.planAnswerLocked(ans.Agent, ans.Permission, target)
		if !run {
			continue
		}
		stateChanged = stateChanged || moved
		effects = append(effects, pendingEffect{ans.Agent, perms[i], target})
	}
	// A pure retry re-runs the effect without moving any state, so there is
	// nothing new to write — persisting a byte-identical set would be waste.
	var toSave permission.Set
	if stateChanged && s.mode != config.PermissionModeGrantAll {
		toSave = s.set.Clone()
	}
	s.mu.Unlock()

	if len(effects) == 0 {
		return nil
	}
	s.runEffects(effects)
	if toSave != nil {
		if err := s.store.Save(toSave); err != nil {
			s.log.LogError("permissions", "", fmt.Sprintf("failed to persist permission state: %v", err))
		}
	}
	s.push.Broadcast(outbound.PushMessage{Type: outbound.PushTypePermissionsUpdated})
	return nil
}

// ReloadFromStore re-reads the persisted consent store and adopts it as the
// in-memory truth, running the effect for every permission whose state moved
// and broadcasting permissions_updated so both wizards re-render. It reports
// how many permissions changed.
//
// This is the daemon's answer to consent being changed by ANOTHER PROCESS
// (#1425). `irrlichd --uninstall-hooks` is a separate, short-lived process: it
// removes the entries and writes "denied" into permissions.json, but before
// this existed the running daemon had read that store exactly once, at startup
// in NewPermissionService, so its in-memory set still said "granted" —
// and #1372's re-verification loop, which asks that in-memory gate on a timer,
// re-installed the entries within one interval. The user ran a command whose
// entire purpose is "remove these" and they came back.
//
// The semantics are deliberately "what a restart would do, without the
// restart": a daemon starting now would read this same file and believe it, so
// there is no third state to reason about, and nothing here can be reached by
// a caller that could not equally have restarted the daemon.
//
// It does NOT write — the store is the input, never the output — and it is
// serialized against Answer by storeMu, held across both the Load and the
// adopt. Not writing is on its own NOT enough to make the two safe together;
// see the storeMu field comment for the interleaving and the lock order.
//
// #570 is not weakened, in three separate ways. Every "granted" it can produce
// came out of the store, i.e. out of an answer the user actually gave and the
// daemon actually persisted; a pair absent from the store reads as pending via
// Set.Get, so an emptied or truncated file revokes rather than opens; and only
// DECLARED permissions are considered, so an unrecognized key in the file
// grants nothing. Reloading is not re-consenting.
//
// Grant-all mode is a deliberate no-op. Its grants are in-memory only and
// never persisted (see Start), so adopting the store would revoke every one of
// them and leave a recording or demo daemon monitoring nothing.
func (s *PermissionService) ReloadFromStore() (int, error) {
	if s.mode == config.PermissionModeGrantAll {
		return 0, nil
	}
	s.storeMu.Lock()
	defer s.storeMu.Unlock()

	stored, err := s.store.Load()
	if err != nil {
		s.log.LogError("permissions", "", fmt.Sprintf("failed to reload permission state (keeping the state in memory): %v", err))
		return 0, err
	}

	var effects []pendingEffect
	s.mu.Lock()
	for _, a := range s.agents {
		for _, p := range a.Permissions {
			want := stored.Get(a.Identity.Name, p.Key)
			if s.set.Get(a.Identity.Name, p.Key) == want {
				continue
			}
			s.set.Put(a.Identity.Name, p.Key, want)
			// One effect per moved permission, so len(effects) IS the change
			// count — no second running total to keep in step. A move to
			// pending runs the Remove closure and stops watchers, same as a
			// denial: runClosureEffect treats every non-granted target as
			// "remove", the fail-safe direction for a permission that has
			// stopped saying yes.
			effects = append(effects, pendingEffect{a.Identity.Name, p, want})
		}
	}
	s.mu.Unlock()

	if len(effects) == 0 {
		return 0, nil
	}
	s.runEffects(effects)
	s.push.Broadcast(outbound.PushMessage{Type: outbound.PushTypePermissionsUpdated})
	s.log.LogInfo("permissions", "", fmt.Sprintf("reloaded consent from the store: %d permission(s) changed", len(effects)))
	return len(effects), nil
}

// planAnswerLocked records one answer and reports (stateMoved, runEffect).
//
// A same-state re-answer is normally a no-op, so the losing surface's
// duplicate submission is harmless — EXCEPT when this permission's effect
// failed, in which case the identical answer is the user hitting Retry and
// must re-run the closure (#1362). Without that exception a grant whose
// Apply failed could only be retried by revoking and re-granting, the
// workaround the issue rules out. A retry moves no state, so it also tells
// the caller there is nothing new to persist.
//
// Caller holds s.mu.
func (s *PermissionService) planAnswerLocked(agentName, key string, target permission.State) (moved, run bool) {
	if s.set.Get(agentName, key) != target {
		s.set.Put(agentName, key, target)
		return true, true
	}
	return false, s.effectFailedLocked(agentName, key)
}

// declared returns the declaration for the agent/key pair.
func (s *PermissionService) declared(agentName, key string) (agent.Permission, bool) {
	for _, a := range s.agents {
		if a.Identity.Name != agentName {
			continue
		}
		for _, p := range a.Permissions {
			if p.Key == key {
				return p, true
			}
		}
	}
	return agent.Permission{}, false
}

// runEffects executes consent effects sequentially under effectMu (NOT
// under s.mu — Granted() must never wait on file I/O; effectMu keeps two
// concurrent Answer batches from racing the same installer). Modify-kind
// permissions run their Apply/Remove closures; observe-kind permissions
// start/stop the agent's watchers — and may additionally carry
// Apply/Remove closures when their monitoring isn't watcher-factory based
// (the Gas Town orchestrator). Effect errors are logged and recorded on
// the permission (surfacing in Snapshot as EffectError), never propagated
// — the recorded state stands, the user's consent is never rolled back by
// a failed effect, and effects are re-applied on the next daemon start.
// It returns how many effects it actually RAN, which is not len(effects): an
// effect superseded while the batch waited on effectMu is skipped. Only the
// out-of-band #1372 repair reads the count, and it needs it — from outside,
// "skipped because consent went away" and "applied successfully" are otherwise
// indistinguishable, and the repair loop would bank a phantom re-install.
func (s *PermissionService) runEffects(effects []pendingEffect) int {
	if len(effects) == 0 {
		return 0
	}
	ran := 0
	s.effectMu.Lock()
	defer s.effectMu.Unlock()
	for _, e := range effects {
		// A conflicting answer may have superseded this effect while the
		// batch waited on effectMu: state mutations serialize under mu,
		// effect batches under effectMu, and the two orders can differ
		// when both surfaces answer the same permission near-
		// simultaneously. Skip stale effects so the executed side effects
		// always converge to the recorded state — the superseding answer
		// carries its own effect.
		s.mu.Lock()
		current := s.set.Get(e.agentName, e.perm.Key)
		s.mu.Unlock()
		if current != e.target {
			s.log.LogInfo("permissions", "", fmt.Sprintf("%s/%s: skipping stale %s effect (state is now %s)", e.agentName, e.perm.Key, e.target, current))
			continue
		}
		ran++
		s.runClosureEffect(e)
		if e.perm.Kind == permission.KindObserve {
			if e.target == permission.StateGranted {
				s.startWatching(e.agentName)
			} else {
				s.stopWatching(e.agentName)
			}
		}
	}
	return ran
}

// runClosureEffect runs the effect's Apply/Remove closure, if any, and
// records the outcome in effectErrs so the two wizards can render "granted
// but NOT applied, because <reason>" instead of a bare "granted" (#1362).
// The error is still logged and still not propagated — the recorded
// consent stands either way — but it is no longer the only trace.
//
// A declared upstream-version floor (agent.ManagedUserFile.Version, issue #1365)
// is checked here rather than inside each adapter's Apply closure, so an
// adapter joins the scheme by declaring a version string instead of by copying
// a function. It deliberately produces an ordinary effect error rather than a
// separate "skipped" concept, so #1362's surfacing carries it for free: the
// wizard renders "granted but NOT applied, because <the CLI is too old>", and
// because planAnswerLocked re-runs the effect when a re-answer finds a
// recorded failure, the refusal's own advice — upgrade the CLI and grant again
// — is literally the gesture that retries it.
func (s *PermissionService) runClosureEffect(e pendingEffect) {
	effect, verb := e.perm.Apply, "apply"
	if e.target != permission.StateGranted {
		effect, verb = e.perm.Remove, "remove"
	}
	// A nil closure records success: nothing can have failed, so any error
	// left over from the opposite verb is stale.
	var reason string
	if effect != nil {
		// Only granting is gated. Removing our entries from a config file is
		// always safe and always wanted, including on a CLI too old to have
		// understood them in the first place.
		err := error(nil)
		if e.target == permission.StateGranted {
			// Home guard before version gate: the version gate may shell out to
			// probe the CLI, and there is no reason to pay for that on an
			// install that is about to be refused anyway.
			if err = s.sharedConfigRefusal(e.perm); err == nil {
				err = s.hookVersionRefusal(e.perm)
			}
		}
		if err == nil {
			err = effect()
		}
		if err != nil {
			reason = err.Error()
			s.log.LogError("permissions", "", fmt.Sprintf("failed to %s %s/%s: %v", verb, e.agentName, e.perm.Key, err))
		} else {
			s.log.LogInfo("permissions", "", fmt.Sprintf("%s/%s: %sd", e.agentName, e.perm.Key, verb))
		}
	}
	s.recordEffectResult(e, reason)
}

// recordEffectResult stores this permission's effect outcome; the empty
// string means "succeeded", which is also how a missing key reads, so both
// consumers need no special case. Callers hold effectMu, never mu — and
// call this only AFTER the closure returns, so no file I/O is ever in
// flight while s.mu is held.
func (s *PermissionService) recordEffectResult(e pendingEffect, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.effectErrs[effectKey(e.agentName, e.perm.Key)] = reason
}

// effectFailed reports whether the permission's last consent effect failed.
// Caller holds s.mu.
func (s *PermissionService) effectFailedLocked(agentName, permKey string) bool {
	return s.effectErrs[effectKey(agentName, permKey)] != ""
}

// hookVersionRefusal resolves the installed upstream CLI version for a
// permission that declares a floor and returns a non-nil error when the
// install must not proceed. It returns nil for every permission that declares
// no floor, which is every non-hook permission and any hook adapter that has
// not opted in.
//
// Resolution prefers the adapter's own passive signal over running anything:
// Codex reads cli_version out of the transcript it is already tailing, which
// costs nothing and works when the binary is not on the daemon's PATH. The
// probe is the fallback for adapters with no such signal.
func (s *PermissionService) hookVersionRefusal(p agent.Permission) error {
	if p.Writes == nil || p.Writes.Version == nil || p.Writes.Version.Min == "" {
		return nil
	}
	gate := p.Writes.Version

	installed := ""
	if gate.Observed != nil {
		installed = gate.Observed()
	}
	if s.shouldConfirmByProbe(gate, installed) {
		probed, err := cliprobe.Probe(s.effectContext(), gate.Probe)
		if err != nil {
			// Unknown version: fail open, but say so. A probe that could not
			// run is the state most likely to be misread later as "the gate
			// checked and approved".
			s.log.LogInfo("permissions", "", fmt.Sprintf(
				"%s: version probe failed (%v); installing anyway — an unreadable version is not an old one", p.Key, err))
		} else {
			installed = probed
		}
	}

	if allowed, why := gate.Permits(installed); !allowed {
		return errors.New(why)
	}
	return nil
}

// shouldConfirmByProbe decides whether to spend a process on confirming the
// version. It returns false only for the case that needs no help: a passive
// signal that parses and already clears the floor.
//
// The two cases it does probe are the ones where the passive answer cannot be
// relied on:
//
//   - It is unknown or unparseable. An adapter with no Observed source at all
//     (Claude Code) is always here, and so is one whose source returned
//     something it could not read — falling open without asking the binary
//     that could have answered would make the gate decorative.
//   - It parses and says "too old". A passive signal is the LAST-RUN version,
//     not the installed one, so it can only be stale downward: a user who
//     upgrades and grants before starting a new session would otherwise be
//     refused on a version they no longer run, with nothing installed and the
//     permission still showing green. A false refusal is the one direction
//     this gate must never produce quietly, so it is worth a process to be
//     sure.
func (s *PermissionService) shouldConfirmByProbe(gate *agent.VersionGate, installed string) bool {
	if len(gate.Probe) == 0 {
		return false
	}
	if _, parsed := cliversion.Parse(installed); !parsed {
		return true
	}
	allowed, _ := gate.Permits(installed)
	return !allowed
}

// effectContext is the daemon-lifetime context effects run under, so a
// shutdown mid-probe cancels it rather than leaving a process to time out.
// Start sets parent; a service constructed without one (tests) falls back to
// Background.
//
// Takes s.mu because Start writes parent under it. Callers hold effectMu,
// never mu, so this respects the documented lock order (effectMu before mu)
// — and it is released before the probe runs, so no exec ever happens with
// the hot Granted() gate's mutex held.
func (s *PermissionService) effectContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.parent != nil {
		return s.parent
	}
	return context.Background()
}

// startWatching constructs the agent's watchers fresh, registers their
// event streams with the detector, and starts their Watch loops under a
// per-agent cancellable context. Takes s.mu internally (callers hold
// effectMu, never mu).
func (s *PermissionService) startWatching(agentName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, running := s.watching[agentName]; running {
		return
	}
	factory := s.factories[agentName]
	if factory == nil || s.registrar == nil {
		return
	}
	parent := s.parent
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	for _, w := range factory() {
		s.registrar.AddWatcher(ctx, w)
		go func(w inbound.Watcher) {
			if err := w.Watch(ctx); err != nil && err != context.Canceled {
				s.log.LogError("agent-watcher", "", fmt.Sprintf("%s watcher error: %v", agentName, err))
			}
		}(w)
	}
	s.watching[agentName] = cancel
	s.log.LogInfo("permissions", "", fmt.Sprintf("monitoring started for %s", agentName))
}

// stopWatching cancels the agent's watcher context: Watch loops and
// detector drains exit, so existing sessions stop updating immediately.
// Takes s.mu internally (callers hold effectMu, never mu).
func (s *PermissionService) stopWatching(agentName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cancel, running := s.watching[agentName]
	if !running {
		return
	}
	cancel()
	delete(s.watching, agentName)
	s.log.LogInfo("permissions", "", fmt.Sprintf("monitoring stopped for %s", agentName))
}

// runDetectionLoop polls for live agent processes and broadcasts a
// permissions_updated message whenever any agent's detected flag flips —
// that is what makes the wizard appear when a new agent shows up. The
// loop exits for good once nothing is pending: answers only ever move
// pending → granted/denied at runtime, so with nothing left to prompt
// for, the per-agent process scans would run forever for no consumer.
func (s *PermissionService) runDetectionLoop(ctx context.Context) {
	ticker := time.NewTicker(s.detectInterval)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		done := !s.anyPendingLocked()
		s.mu.Unlock()
		if done {
			s.log.LogInfo("permissions", "", "all permissions answered — detection poller stopped")
			return
		}
		s.pollDetection()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pollDetection runs one detection pass. The (potentially slow) process
// scans happen outside the lock. Agents with a registered probe (Gas
// Town's GT_ROOT directory check) use it instead of the process matcher.
func (s *PermissionService) pollDetection() {
	cur := make(map[string]bool, len(s.agents))
	for _, a := range s.agents {
		if probe, ok := s.probes[a.Identity.Name]; ok {
			cur[a.Identity.Name] = probe()
			continue
		}
		cur[a.Identity.Name] = s.hasLive(a.Process.Match)
	}
	s.mu.Lock()
	changed := false
	for name, live := range cur {
		if s.detected[name] != live {
			s.detected[name] = live
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.push.Broadcast(outbound.PushMessage{Type: outbound.PushTypePermissionsUpdated})
	}
}

// ObserveGranted reports whether the agent's observe-kind permission is
// granted. This is the per-adapter consent gate for transcript reads that
// happen OUTSIDE the watcher path — the detector's startup seed and its
// stale-session refresh both re-read persisted sessions' transcripts and
// must honor consent too. Unknown adapters (or agents with no observe
// permission) read as not granted: consent-first defaults closed.
func (s *PermissionService) ObserveGranted(agentName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.agents {
		if a.Identity.Name != agentName {
			continue
		}
		for _, p := range a.Permissions {
			if p.Kind == permission.KindObserve {
				return s.set.Get(agentName, p.Key) == permission.StateGranted
			}
		}
	}
	return false
}
