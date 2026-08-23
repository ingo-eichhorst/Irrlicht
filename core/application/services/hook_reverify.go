// hook_reverify.go implements the periodic hook-entry re-verification loop
// (issue #1372).
//
// irrlicht's hook model was "install once on consent, then assume it is there".
// That assumption is false for any agent whose own UI rewrites its config.
// gemini-cli's settings writer is sync-by-omission — applyKeyDiff deletes every
// key on disk that is absent from the snapshot the CLI took at startup,
// recursively — and #1355's Phase C audit verified it live: with gemini
// running, an externally added top-level key AND an externally added
// hooks.PreCompress entry were both deleted by a single /theme change. One of
// the write paths that can do it, saveModelChange, is not user-initiated.
// Claude Code and Codex have the same exposure to a hand-edit or a future
// in-app settings surface, and it has never been checked.
//
// # The three hook diagnoses, and why this one is separate
//
// The user-visible symptom is identical in all three cases — "irrlicht stopped
// detecting waiting" — so the whole value of these features is that they
// produce DIFFERENT evidence. Collapsing any two would put the reader back
// where they started:
//
//   - install FAILED — the entries were never written. Surfaced as the
//     permission's EffectError, retried by re-answering (#1362/#1365). This
//     loop's repair failures route into that same slot rather than inventing a
//     second one.
//   - entries PRESENT, nothing arriving — the channel is dead for some other
//     reason (a port mismatch the CLI does not report, a CLI that does not run
//     our hooks). #1368's liveness watchdog demotes it to the transcript tier.
//     It reads receipts and turns, and deliberately never re-reads the file.
//   - entries GONE — this file. It re-reads the file and knows nothing about
//     receipts. #1368's hooks.json note already points here by number, because
//     "silent" and "removed" look the same from inside the watchdog.
//
// # Consent
//
// Re-verification READS a user config file, and the repair WRITES one. Both are
// exactly what the hooks permission's disclosure covers, so both stay behind
// it. Three independent refusals, deliberately not one:
//
//  1. this loop consults Granted before it reads — every pass, never cached, and
//     before the back-off check, so a target sitting in a long cooling-off
//     window still notices its consent went away;
//  2. PermissionService.RepairGrantedHookInstall re-reads the recorded state
//     before it queues anything; and
//  3. runEffects re-reads it a third time under effectMu, immediately before
//     running the closure.
//
// The third is what makes revocation authoritative rather than eventually
// noticed: gates 1 and 2 can only be as fresh as the instant they were asked,
// and the write that follows may wait on a mutex behind an in-flight wizard
// answer. A background repair that outlives consent would break #570 more
// thoroughly than the bug it fixes, so the loop owns no write path of its own —
// it can only ask the service, and the service can say no.
//
// Gates 1 and 3 have each been individually deleted and seen to turn a test
// red — see hook_reverify_gate_internal_test.go for gate 3, the one that only
// a race can reach. Gate 2 cannot be shown that way: gate 3 re-derives the
// identical fact from the same recorded state immediately before the closure
// runs, so no write-observing test can ever tell "gate 2 refused" from "gate 2
// let it through and gate 3 refused a moment later" (#1751, mutation-verified
// via tools/mutate.sh). Gate 2's own, independently provable property is
// contention avoidance, not write safety — see
// hook_reverify_gate_internal_test.go's header and
// TestRepairGrantedHookInstall_DoesNotContendForEffectMuWhenNotGranted.
package services

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/outbound"
)

// Defaults for the production loop. The cadence is deliberately unhurried: the
// cost of a late repair is one degraded turn, the cost of a hot loop is writing
// to a user's config file all day.
const (
	defaultReverifyInterval    = 5 * time.Minute
	defaultReverifyBackoffBase = 5 * time.Minute
	defaultReverifyBackoffMax  = time.Hour

	// reverifyStormThreshold is the number of consecutive repairs after which
	// the loop says out loud that it is losing. Repeated repairs are a signal
	// in their own right — a config being rewritten every few seconds is a
	// different problem from one clobbered once — and it is the one thing the
	// counters alone would not put in front of a reader of events.log.
	reverifyStormThreshold = 3

	// reverifyNoPassYet is the last_outcome published for a target the loop has
	// not yet examined. Not a ReverifyOutcome: nothing counts it, because no
	// decision was made.
	reverifyNoPassYet = "no_pass_yet"
)

// ReverifyOutcome is what one pass decided about one target. The set is closed,
// which is why the counters below can be a fixed array rather than a map: same
// shape as hookjson's PathConfiner rejection counters (#1361).
type ReverifyOutcome string

const (
	// ReverifyIntact: the entries are present and current. The common answer.
	ReverifyIntact ReverifyOutcome = "intact"
	// ReverifyRepaired: entries were missing or stale and were re-installed.
	ReverifyRepaired ReverifyOutcome = "repaired"
	// ReverifyRepairFailed: the re-install was attempted and errored. Also
	// recorded as the permission's EffectError (#1362).
	ReverifyRepairFailed ReverifyOutcome = "repair_failed"
	// ReverifyNoConsent: the permission is not granted, so nothing was read and
	// nothing was written. Counted rather than silent, because "not watched"
	// and "watched and healthy" are different answers to the same question.
	ReverifyNoConsent ReverifyOutcome = "skipped_no_consent"
	// ReverifyBackoff: a repair is in force's cooling-off window, so this pass
	// did not even read the file.
	ReverifyBackoff ReverifyOutcome = "skipped_backoff"
	// ReverifyUnreadable: the config could not be parsed. Not damage and not
	// health — repairing would spin against a file EnsureInstalled also refuses
	// to overwrite.
	ReverifyUnreadable ReverifyOutcome = "unreadable"
)

// reverifyOutcomes is every outcome in a fixed order, so each has a stable
// counter slot. An array (not a slice) so len() is a constant.
var reverifyOutcomes = [...]ReverifyOutcome{
	ReverifyIntact, ReverifyRepaired, ReverifyRepairFailed,
	ReverifyNoConsent, ReverifyBackoff, ReverifyUnreadable,
}

// HookEntryHealth is one target's row in the diagnostics bundle.
//
// Every declared target gets a row whether or not anything has happened to it,
// for the reason #1368's Snapshot gives: a missing row is indistinguishable
// from a bundle collected before the feature existed.
type HookEntryHealth struct {
	Adapter    string `json:"adapter"`
	Permission string `json:"permission"`
	// ConfigPath is the file being verified. Without it the finding is not
	// actionable — the reader cannot go look.
	ConfigPath string `json:"config_path,omitempty"`
	// Watched is whether the last pass actually examined this target — i.e.
	// consent was granted then. False also covers "no pass has run yet", which
	// LastOutcome distinguishes. A false row is published, not omitted.
	Watched bool `json:"watched"`
	// Missing and Stale are the last verdict's detail, empty when intact.
	Missing []string `json:"missing,omitempty"`
	Stale   []string `json:"stale,omitempty"`
	// Repairs is the lifetime count for this target in this process.
	Repairs uint64 `json:"repairs"`
	// ConsecutiveRepairs is how many passes in a row have had to repair. A
	// number climbing here is the "something is fighting us" signal; it resets
	// on the first intact pass.
	ConsecutiveRepairs int    `json:"consecutive_repairs"`
	LastOutcome        string `json:"last_outcome,omitempty"`
	// BackoffSeconds is the deferral currently in force, not the time left in
	// it: a remaining-time field would need a clock in Snapshot and would read
	// as zero whenever the bundle happened to be collected late.
	BackoffSeconds int `json:"backoff_seconds,omitempty"`
	// LastError is the reason the last repair or read failed, if it did.
	LastError string `json:"last_error,omitempty"`
}

// HookEntryReverifySnapshot is the whole loop's view at one instant.
type HookEntryReverifySnapshot struct {
	Targets  []HookEntryHealth
	Outcomes map[ReverifyOutcome]uint64
}

// HookEntryReverifyConfig holds the loop's tunables and collaborators.
type HookEntryReverifyConfig struct {
	// Interval is the pass cadence. <= 0 takes the default; Run does nothing
	// when there are no targets.
	Interval time.Duration
	// BackoffBase is the deferral after a single repair; it doubles per
	// consecutive repair up to BackoffMax.
	BackoffBase time.Duration
	BackoffMax  time.Duration

	// Agents is the registry projection the targets are derived from, so a new
	// hook adapter is covered by declaring Verify and nothing else.
	Agents []agent.Agent

	// Granted is the consent gate, consulted before every read. Nil reads as
	// "nothing is granted", which disables the loop rather than opening it —
	// the safe direction for a gate.
	Granted func(agentName, key string) bool

	// Repair re-runs the install behind the service's own consent re-check. It
	// reports whether the write was ATTEMPTED and, if so, whether it worked.
	// Nil disables repair while leaving verification and reporting intact.
	Repair func(agentName, key string) (bool, error)

	Log outbound.Logger
	// Now is injectable for tests; nil means time.Now.
	Now func() time.Time
}

// reverifyTarget is one hook install to watch. Resolved once at construction:
// the declaration is static and the config path is HOME-derived, so re-deriving
// it per pass would buy nothing and could disagree with itself.
type reverifyTarget struct {
	adapter    string
	permKey    string
	configPath string
	verify     func() (agent.HookEntryStatus, error)
}

func (t reverifyTarget) id() string { return t.adapter + "/" + t.permKey }

// reverifyState is one target's episode memory.
type reverifyState struct {
	missing            []string
	stale              []string
	repairs            uint64
	consecutiveRepairs int
	// consecutiveUnreadable escalates the deferral for a config that cannot be
	// parsed. It is a SEPARATE counter from consecutiveRepairs, which is
	// published as a repair count: an unreadable config is never repaired, so
	// borrowing that field to drive the back-off would put a number in the
	// diagnostics bundle claiming writes that never happened.
	consecutiveUnreadable int
	lastOutcome           ReverifyOutcome
	backoff               time.Duration
	nextEligible          time.Time
	lastError             string
}

// reset returns a target to "nothing is going on", used when consent goes away
// and when an intact pass ends an episode. Accumulated back-off is forgotten
// rather than banked: turns spent while the channel was legitimately absent are
// not evidence against it — the same argument #1368's disarm path makes.
func (s *reverifyState) reset() {
	s.missing, s.stale = nil, nil
	s.consecutiveRepairs = 0
	s.consecutiveUnreadable = 0
	s.backoff = 0
	s.nextEligible = time.Time{}
	s.lastError = ""
}

// HookEntryVerifier re-verifies installed hook entries on a schedule and
// repairs them through the permission service.
//
// It needs a mutex for the same reason the liveness watchdog does: its writer
// is the loop goroutine, but Snapshot is read from the diagnostics HTTP
// handler on another.
type HookEntryVerifier struct {
	mu      sync.Mutex
	cfg     HookEntryReverifyConfig
	targets []reverifyTarget
	state   map[string]*reverifyState
	counts  [len(reverifyOutcomes)]atomic.Uint64

	// grantedFn and repairFn are the permission service's two entry points.
	// They live outside cfg because the daemon builds this verifier before the
	// permission service exists — the same startup ordering HookLivenessWatchdog
	// solves with SetChannelReady — and because they are then read from the loop
	// goroutine while SetConsent may still be writing them.
	grantedFn func(agentName, key string) bool
	repairFn  func(agentName, key string) (bool, error)
}

// SetConsent wires the permission service's gate and repair entry points. Until
// it is called the verifier reports every target unwatched and writes nothing,
// which is the honest reading: before the permission service exists, nothing
// has been consented to.
func (v *HookEntryVerifier) SetConsent(granted func(agentName, key string) bool, repair func(agentName, key string) (bool, error)) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.grantedFn, v.repairFn = granted, repair
}

// consent returns the two collaborators as one atomic read, so a pass cannot
// use a gate from before SetConsent with a repair from after it.
func (v *HookEntryVerifier) consent() (func(string, string) bool, func(string, string) (bool, error)) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.grantedFn, v.repairFn
}

// NewHookEntryVerifier builds the loop and resolves its targets from the agent
// registry: every modify-kind permission declaring a HookInstall with a Verify.
//
// An adapter that declares a hook install but no Verify is SKIPPED rather than
// faulted here — one adapter's missing declaration must not disable the loop
// for the others. TestEveryHookInstallDeclaresAVerifier is what stops that
// omission being permanent, and it fires in the adapter package, before the
// adapter has a test of its own.
func NewHookEntryVerifier(cfg HookEntryReverifyConfig) *HookEntryVerifier {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultReverifyInterval
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = defaultReverifyBackoffBase
	}
	// Two clamps, not a copy-paste. The first supplies the default for an unset
	// or nonsensical cap; the second catches the case the default cannot serve —
	// a caller passing a BackoffBase LARGER than the default cap, where leaving
	// the default in place would make the cap smaller than the base and
	// backoffFor's guard would return the cap on the very first repair.
	if cfg.BackoffMax < cfg.BackoffBase {
		cfg.BackoffMax = defaultReverifyBackoffMax
	}
	if cfg.BackoffMax < cfg.BackoffBase {
		cfg.BackoffMax = cfg.BackoffBase
	}

	v := &HookEntryVerifier{
		cfg:       cfg,
		state:     map[string]*reverifyState{},
		grantedFn: cfg.Granted,
		repairFn:  cfg.Repair,
	}
	for _, a := range cfg.Agents {
		for _, p := range a.Permissions {
			// Verify is the precise gate, not the hooks key: it is the only
			// field that says re-verification is possible at all. #1383
			// widened Writes to every managed user file, so the
			// instruction-block and kitty permissions reach here too — they
			// declare no Verify and drop out, which is the correct answer
			// rather than a coincidence.
			if p.Writes == nil || p.Writes.Verify == nil {
				continue
			}
			t := reverifyTarget{adapter: a.Identity.Name, permKey: p.Key, verify: p.Writes.Verify}
			if p.Writes.Path != nil {
				// Best effort: an unresolvable path costs the row its
				// actionability, not the loop its function.
				if path, err := p.Writes.Path(); err == nil {
					t.configPath = path
				}
			}
			v.targets = append(v.targets, t)
			v.state[t.id()] = &reverifyState{}
		}
	}
	sort.Slice(v.targets, func(i, j int) bool { return v.targets[i].id() < v.targets[j].id() })
	return v
}

// Run drives one pass per Interval until ctx is cancelled. Its own goroutine
// and its own ticker — shape (A) of the two periodic-loop shapes in this
// package (see PIDManager.SweepDeadPIDs) — because it is an independent concern
// measured in wall time, unlike the liveness watchdog, which rides the
// detector's event loop because it counts turns.
func (v *HookEntryVerifier) Run(ctx context.Context) {
	if v == nil || len(v.targets) == 0 {
		return
	}
	// One pass immediately, before the first tick. Two reasons: a clobber that
	// happened while the daemon was down is repaired at once rather than up to
	// an Interval later, and — the reporting one — a bundle collected in the
	// first few minutes of uptime would otherwise show every row in its zero
	// state, which reads as "not granted, nothing wrong" for a permission that
	// may be granted and an install that may be gone.
	//
	// Safe to run here: the caller starts this only after PermissionService.Start
	// has synchronously re-applied every granted install, so this pass observes
	// the boot repair rather than racing it.
	v.pass(v.now())

	ticker := time.NewTicker(v.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			v.pass(v.now())
		}
	}
}

// RunPassForTest runs exactly one pass synchronously at the given instant. The
// package convention (RunPIDLivenessSweepForTest, RunStaleSessionRefreshForTest)
// is a plain synchronous seam onto the same function the ticker calls, rather
// than a fake clock.
func (v *HookEntryVerifier) RunPassForTest(now time.Time) { v.pass(now) }

func (v *HookEntryVerifier) now() time.Time {
	if v.cfg.Now != nil {
		return v.cfg.Now()
	}
	return time.Now()
}

// pass is THE decision point: the one place a target's outcome is decided, so
// that every counter, log line and diagnostics row describes the same verdict
// rather than several near-simultaneous ones that could disagree.
//
// The order of the guards is the contract, not an optimization. Consent is
// asked FIRST, before the back-off check and before the file is opened, so a
// revoked permission cannot be read even once — and so a target sitting in a
// long back-off window still notices its consent went away.
func (v *HookEntryVerifier) pass(now time.Time) {
	if v == nil {
		return
	}
	granted, repair := v.consent()
	for _, t := range v.targets {
		v.checkTarget(t, now, granted, repair)
	}
}

func (v *HookEntryVerifier) checkTarget(t reverifyTarget, now time.Time, granted func(string, string) bool, repair func(string, string) (bool, error)) {
	if granted == nil || !granted(t.adapter, t.permKey) {
		v.mu.Lock()
		st := v.state[t.id()]
		st.reset()
		st.lastOutcome = ReverifyNoConsent
		v.mu.Unlock()
		v.count(ReverifyNoConsent)
		return
	}

	v.mu.Lock()
	st := v.state[t.id()]
	deferred := !st.nextEligible.IsZero() && now.Before(st.nextEligible)
	v.mu.Unlock()
	if deferred {
		v.count(ReverifyBackoff)
		return
	}

	status, err := t.verify()
	if err != nil {
		v.recordUnreadable(t, err, now)
		return
	}
	if status.Intact() {
		v.recordIntact(t)
		return
	}
	v.repair(t, status, now, repair)
}

func (v *HookEntryVerifier) recordUnreadable(t reverifyTarget, err error, now time.Time) {
	v.mu.Lock()
	st := v.state[t.id()]
	first := st.lastOutcome != ReverifyUnreadable
	st.lastOutcome = ReverifyUnreadable
	st.lastError = err.Error()
	// Defer the next read as well: a config we cannot parse will not become
	// parseable by being read again in five minutes, and this is the same spin
	// the storm back-off prevents, arriving by a different route.
	//
	// nextEligible is what checkTarget actually reads — setting only backoff
	// would publish a deferral in the diagnostics bundle that does not exist,
	// which is a worse failure than not deferring at all.
	st.consecutiveUnreadable++
	st.backoff = v.backoffFor(st.consecutiveUnreadable)
	st.nextEligible = now.Add(st.backoff)
	v.mu.Unlock()
	v.count(ReverifyUnreadable)
	if first && v.cfg.Log != nil {
		v.cfg.Log.LogError("hooks", "", fmt.Sprintf(
			"%s/%s: cannot re-verify hook entries in %s: %v (#1372). Nothing was written — "+
				"the installer refuses to overwrite a config it cannot parse, so this needs a human "+
				"to fix the file.", t.adapter, t.permKey, t.configPath, err))
	}
}

func (v *HookEntryVerifier) recordIntact(t reverifyTarget) {
	v.mu.Lock()
	st := v.state[t.id()]
	recovered := st.consecutiveRepairs
	st.reset()
	st.lastOutcome = ReverifyIntact
	v.mu.Unlock()
	v.count(ReverifyIntact)
	if recovered > 0 && v.cfg.Log != nil {
		v.cfg.Log.LogInfo("hooks", "", fmt.Sprintf(
			"%s/%s: hook entries verified intact after %d repair(s) — back-off cleared (#1372)",
			t.adapter, t.permKey, recovered))
	}
}

// repair asks the permission service to re-install. It never writes itself.
func (v *HookEntryVerifier) repair(t reverifyTarget, status agent.HookEntryStatus, now time.Time, doRepair func(string, string) (bool, error)) {
	if doRepair == nil {
		v.recordDamageWithoutRepair(t, status)
		return
	}

	attempted, err := doRepair(t.adapter, t.permKey)
	if !attempted {
		// The service declined: consent is no longer granted as of the moment
		// it took its lock. That is a consent outcome, not a failed repair —
		// counting it as a failure would put a fabricated error in the bundle,
		// and backing off from it would be backing off from the user's own
		// decision.
		v.mu.Lock()
		st := v.state[t.id()]
		st.reset()
		st.lastOutcome = ReverifyNoConsent
		v.mu.Unlock()
		v.count(ReverifyNoConsent)
		return
	}

	v.mu.Lock()
	st := v.state[t.id()]
	// Captured before lastOutcome is overwritten: the failure branch below logs
	// only on the FIRST failure of an episode, the same once-per-episode rule
	// recordUnreadable and logRepair follow. Without it a permanently failing
	// repair — the shape a CLI below the #1365 version floor produces — writes
	// an error line every pass for the life of the daemon.
	firstFailure := st.lastOutcome != ReverifyRepairFailed
	st.repairs++
	st.consecutiveRepairs++
	st.missing = append([]string(nil), status.Missing...)
	st.stale = append([]string(nil), status.Stale...)
	st.backoff = v.backoffFor(st.consecutiveRepairs)
	st.nextEligible = now.Add(st.backoff)
	n, backoff := st.consecutiveRepairs, st.backoff
	if err != nil {
		st.lastOutcome, st.lastError = ReverifyRepairFailed, err.Error()
	} else {
		st.lastOutcome, st.lastError = ReverifyRepaired, ""
	}
	v.mu.Unlock()

	if err != nil {
		v.count(ReverifyRepairFailed)
		if firstFailure && v.cfg.Log != nil {
			v.cfg.Log.LogError("hooks", "", fmt.Sprintf(
				"%s/%s: failed to re-install hook entries in %s (%s): %v (#1372). The permission now "+
					"carries this as its effect_error (#1362); re-granting it in the wizard retries.",
				t.adapter, t.permKey, t.configPath, status.Damage(), err))
		}
		return
	}

	v.count(ReverifyRepaired)
	v.logRepair(t, status, n, backoff)
}

// recordDamageWithoutRepair is the wiring-incomplete case: verification is on,
// repair is not. It reports rather than pretending health, because a row saying
// "missing: Stop, repairs: 0" is the only thing that would make that visible.
func (v *HookEntryVerifier) recordDamageWithoutRepair(t reverifyTarget, status agent.HookEntryStatus) {
	v.mu.Lock()
	st := v.state[t.id()]
	st.missing = append([]string(nil), status.Missing...)
	st.stale = append([]string(nil), status.Stale...)
	st.lastOutcome = ReverifyRepairFailed
	st.lastError = "no repair function is wired"
	v.mu.Unlock()
	v.count(ReverifyRepairFailed)
}

// logRepair reports the first repair of an episode and one storm notice, then
// goes quiet.
//
// A channel under sustained attack must not write a log line per pass forever —
// the same argument #1364 makes for reporting an unrecognized event name once
// and #1368 for reporting a silent channel once. The counters and the
// diagnostics row carry the volume; events.log carries the fact and the shape.
func (v *HookEntryVerifier) logRepair(t reverifyTarget, status agent.HookEntryStatus, n int, backoff time.Duration) {
	if v.cfg.Log == nil {
		return
	}
	switch n {
	case 1:
		v.cfg.Log.LogError("hooks", "", fmt.Sprintf(
			"%s/%s: irrlicht's hook entries were %s in %s and have been re-installed (#1372). "+
				"Something outside irrlicht rewrote that file — an agent settings UI that syncs by "+
				"omission does this on any config change, and the permission stays green throughout.",
			t.adapter, t.permKey, status.Damage(), t.configPath))
	case reverifyStormThreshold:
		v.cfg.Log.LogError("hooks", "", fmt.Sprintf(
			"%s/%s: hook entries have now been re-installed %d times in a row in %s (#1372) — "+
				"something is rewriting that file faster than we repair it. Backing off to at most one "+
				"repair per %s; further repairs are counted in the diagnostics bundle but not logged "+
				"until the install is found intact.",
			t.adapter, t.permKey, n, t.configPath, backoff))
	}
}

// backoffFor is the deferral after n consecutive repairs: base doubled per
// repair, capped. Exponential rather than fixed because the two failure shapes
// need different answers from one rule — a config clobbered once wants the next
// pass to be soon, a config rewritten every few seconds wants the loop to stop
// fighting it — and a cap rather than unbounded growth because the back-off is
// a ceiling, not a latch: an install clobbered once an hour must still be fixed
// once an hour.
func (v *HookEntryVerifier) backoffFor(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	d := v.cfg.BackoffBase
	for i := 1; i < n; i++ {
		if d >= v.cfg.BackoffMax/2 {
			return v.cfg.BackoffMax
		}
		d *= 2
	}
	if d > v.cfg.BackoffMax {
		return v.cfg.BackoffMax
	}
	return d
}

// count bumps one outcome's counter. Linear scan over a six-element array, the
// same shape hookjson.PathConfiner uses — a map would need a lock on the hot
// path to buy nothing.
func (v *HookEntryVerifier) count(o ReverifyOutcome) {
	for i, name := range reverifyOutcomes {
		if name == o {
			v.counts[i].Add(1)
			return
		}
	}
}

// Snapshot returns one row per target plus the outcome counters, with zero
// counters OMITTED — the idiom #1361 and #1364 established in hookjson. A map
// full of zeros reads as an all-clear from a loop that may never have run once.
func (v *HookEntryVerifier) Snapshot() HookEntryReverifySnapshot {
	if v == nil {
		return HookEntryReverifySnapshot{}
	}
	out := HookEntryReverifySnapshot{Outcomes: map[ReverifyOutcome]uint64{}}
	for i, name := range reverifyOutcomes {
		if n := v.counts[i].Load(); n > 0 {
			out.Outcomes[name] = n
		}
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	out.Targets = make([]HookEntryHealth, 0, len(v.targets))
	for _, t := range v.targets {
		st := v.state[t.id()]
		row := HookEntryHealth{
			Adapter:    t.adapter,
			Permission: t.permKey,
			ConfigPath: t.configPath,
		}
		if st != nil {
			// Derived, never stored. hook_liveness.go's hookChannelState carries
			// the same rule and the reason: a second field that has to agree with
			// the first forever eventually does not, and the row it then prints
			// ("watched": true beside "last_outcome": "skipped_no_consent") is an
			// assertion no pass ever made.
			row.Watched = st.lastOutcome != "" && st.lastOutcome != ReverifyNoConsent
			// An empty lastOutcome means no pass has run yet. Published as its
			// own word rather than omitted: every other field is then at its
			// zero value too, and "watched:false, repairs:0, nothing missing"
			// is indistinguishable from a healthy unconsented row unless
			// something says the loop has not looked.
			if st.lastOutcome == "" {
				row.LastOutcome = reverifyNoPassYet
			}
			row.Missing = append([]string(nil), st.missing...)
			row.Stale = append([]string(nil), st.stale...)
			row.Repairs = st.repairs
			row.ConsecutiveRepairs = st.consecutiveRepairs
			row.LastOutcome = string(st.lastOutcome)
			row.BackoffSeconds = int(st.backoff / time.Second)
			row.LastError = st.lastError
		}
		out.Targets = append(out.Targets, row)
	}
	return out
}
