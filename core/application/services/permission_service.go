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
}

// AgentPermissions groups one agent's permissions for the API.
type AgentPermissions struct {
	Name        string           `json:"name"`
	DisplayName string           `json:"display_name"`
	Detected    bool             `json:"detected"`
	Permissions []PermissionView `json:"permissions"`
}

// PermissionsSnapshot is the GET /api/v1/permissions response body.
type PermissionsSnapshot struct {
	Mode   string             `json:"mode"`
	Agents []AgentPermissions `json:"agents"`
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
	factories map[string]WatcherFactory
	hasLive   HasLiveProcessFunc

	// detectInterval is detectionPollInterval in production; tests shorten
	// it via SetDetectionPollIntervalForTest before Start.
	detectInterval time.Duration

	// probes overrides process-matcher detection for agents whose presence
	// is detected another way (Gas Town: GT_ROOT directory probe). Set via
	// SetDetectionProbe before Start.
	probes map[string]func() bool

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
}

// NewPermissionService loads the persisted consent state and returns the
// service.
func NewPermissionService(deps PermissionServiceDeps) *PermissionService {
	set, err := deps.Store.Load()
	if err != nil {
		deps.Log.LogError("permissions", "", fmt.Sprintf("failed to load permission state (treating all as pending): %v", err))
		set = permission.Set{}
	}
	return &PermissionService{
		agents:         deps.Agents,
		store:          deps.Store,
		push:           deps.Push,
		log:            deps.Log,
		mode:           deps.Mode,
		registrar:      deps.Registrar,
		factories:      deps.Factories,
		hasLive:        deps.HasLive,
		detectInterval: detectionPollInterval,
		probes:         make(map[string]func() bool),
		set:            set,
		detected:       make(map[string]bool),
		watching:       make(map[string]context.CancelFunc),
	}
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

// Snapshot returns the full consent state for GET /api/v1/permissions.
func (s *PermissionService) Snapshot() PermissionsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := PermissionsSnapshot{Mode: s.mode}
	for _, a := range s.agents {
		if len(a.Permissions) == 0 {
			continue
		}
		ap := AgentPermissions{
			Name:        a.Identity.Name,
			DisplayName: a.Identity.DisplayName,
			Detected:    s.detected[a.Identity.Name],
		}
		for _, p := range a.Permissions {
			ap.Permissions = append(ap.Permissions, PermissionView{
				Key:             p.Key,
				Kind:            string(p.Kind),
				State:           string(s.set.Get(a.Identity.Name, p.Key)),
				Title:           p.Title,
				FeatureUnlocked: p.FeatureUnlocked,
				Touches:         p.Touches,
				Detail:          p.Detail,
			})
		}
		out.Agents = append(out.Agents, ap)
	}
	return out
}

// Answer applies a batch of user decisions: state is recorded, modify
// effects are applied/undone, observe watchers are started/stopped, the
// set is persisted, and a permissions_updated broadcast dismisses the
// wizard on whichever surface didn't answer. Re-answering with the same
// state is a no-op, so the losing surface's duplicate submission is
// harmless. Returns ErrUnknownPermission (wrapped) when any entry names
// an undeclared agent/permission pair; nothing is applied in that case.
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
	for i, ans := range answers {
		target := permission.StateDenied
		if ans.Grant {
			target = permission.StateGranted
		}
		if s.set.Get(ans.Agent, ans.Permission) == target {
			continue
		}
		s.set.Put(ans.Agent, ans.Permission, target)
		effects = append(effects, pendingEffect{ans.Agent, perms[i], target})
	}
	var toSave permission.Set
	if len(effects) > 0 && s.mode != config.PermissionModeGrantAll {
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
// (the Gas Town orchestrator). Effect errors are logged, never propagated
// — the recorded state stands and effects are re-applied on the next
// daemon start.
func (s *PermissionService) runEffects(effects []pendingEffect) {
	if len(effects) == 0 {
		return
	}
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
		s.runClosureEffect(e)
		if e.perm.Kind == permission.KindObserve {
			if e.target == permission.StateGranted {
				s.startWatching(e.agentName)
			} else {
				s.stopWatching(e.agentName)
			}
		}
	}
}

// runClosureEffect runs the effect's Apply/Remove closure, if any.
func (s *PermissionService) runClosureEffect(e pendingEffect) {
	effect, verb := e.perm.Apply, "apply"
	if e.target != permission.StateGranted {
		effect, verb = e.perm.Remove, "remove"
	}
	if effect == nil {
		return
	}
	if err := effect(); err != nil {
		s.log.LogError("permissions", "", fmt.Sprintf("failed to %s %s/%s: %v", verb, e.agentName, e.perm.Key, err))
		return
	}
	s.log.LogInfo("permissions", "", fmt.Sprintf("%s/%s: %sd", e.agentName, e.perm.Key, verb))
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
