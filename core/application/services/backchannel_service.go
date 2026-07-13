package services

import (
	"context"
	"strings"
	"sync"
	"time"

	"irrlicht/core/domain/backchannel"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// maxActionsPerSessionPerMinute is a global backstop against a misconfigured
// or oscillating rule spamming a session, independent of per-rule cooldowns.
const maxActionsPerSessionPerMinute = 6

// ruleLister supplies the current rule set (satisfied by the filesystem
// rules store).
type ruleLister interface {
	Rules() []backchannel.Rule
}

// inputForwarder is the write path a fired rule uses (satisfied by
// *InputService, which re-checks the master-toggle + consent + controllability
// on every call).
type inputForwarder interface {
	SendInput(sessionID string, data []byte) error
	SendCommand(sessionID, command string) error
	Interrupt(sessionID string) error
}

// BackchannelEngine fires event→action rules when a session crosses a
// configured lifecycle edge (issue #724). It consumes the push stream as a
// read-only observer; every fire flows through InputService, so a disabled
// backchannel or missing consent makes rules inert regardless of this engine.
//
// Safety: triggers are edge-detected (fire once per transition, not while in a
// state), context-pressure fires only on the rising crossing (inherent
// hysteresis via prevUtil), each rule has a per-session cooldown, and a global
// per-session per-minute cap backstops oscillation. Actions run off the
// consume loop so a slow backend can't make the engine miss other messages.
type BackchannelEngine struct {
	rules   ruleLister
	input   inputForwarder
	presets map[string]map[string]string // adapter → (preset id → command text)
	push    outbound.PushBroadcaster
	enabled func() bool
	logger  outbound.Logger
	now     func() time.Time

	mu         sync.Mutex
	prevState  map[string]string      // sessionID → last observed state
	prevUtil   map[string]float64     // sessionID → last observed context utilization
	prevTokens map[string]int64       // sessionID → last observed total context tokens
	lastFired  map[string]time.Time   // ruleID\x00sessionID → last fire time
	recent     map[string][]time.Time // sessionID → recent fire times (global cap)
}

// NewBackchannelEngine constructs an engine. enabled reports the backchannel
// master-toggle (firing is suppressed when off, but edge bookkeeping continues
// so re-enabling never causes a spurious fire).
func NewBackchannelEngine(rules ruleLister, input inputForwarder, presets map[string]map[string]string, push outbound.PushBroadcaster, enabled func() bool, logger outbound.Logger) *BackchannelEngine {
	return &BackchannelEngine{
		rules:      rules,
		input:      input,
		presets:    presets,
		push:       push,
		enabled:    enabled,
		logger:     logger,
		now:        time.Now,
		prevState:  map[string]string{},
		prevUtil:   map[string]float64{},
		prevTokens: map[string]int64{},
		lastFired:  map[string]time.Time{},
		recent:     map[string][]time.Time{},
	}
}

// Run subscribes to the push stream and evaluates rules until ctx is cancelled.
func (e *BackchannelEngine) Run(ctx context.Context) {
	ch := e.push.Subscribe()
	defer e.push.Unsubscribe(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if msg.Type == outbound.PushTypeDeleted {
				if msg.Session != nil {
					e.forget(msg.Session.SessionID)
				}
				continue
			}
			if msg.Type != outbound.PushTypeUpdated && msg.Type != outbound.PushTypeCreated {
				continue
			}
			for _, r := range e.evaluate(msg.Session) {
				go e.runActions(r, msg.Session.SessionID, msg.Session.Adapter)
			}
		}
	}
}

// evaluate updates edge bookkeeping for the session and returns the rules that
// should fire now (cooldown + global-cap accounting applied). Pure of any I/O
// so the trigger/cooldown/hysteresis logic is unit-testable.
func (e *BackchannelEngine) evaluate(s *session.SessionState) []backchannel.Rule {
	if s == nil || s.SessionID == "" {
		return nil
	}
	sid := s.SessionID
	cur := s.State
	util := 0.0
	var tokens int64
	if s.Metrics != nil {
		util = s.Metrics.ContextUtilization
		tokens = s.Metrics.TotalTokens
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	prev, seen := e.prevState[sid]
	prevUtil := e.prevUtil[sid]
	prevTokens := e.prevTokens[sid]
	e.prevState[sid] = cur
	e.prevUtil[sid] = util
	e.prevTokens[sid] = tokens

	// First observation establishes the baseline; never fire on it (otherwise
	// a session already in `waiting` or already high-pressure would fire the
	// instant the daemon sees it). Membership in prevState is the baseline
	// marker — a real session's State is never the empty string.
	if !seen {
		return nil
	}

	now := e.now()
	// tr bundles the before/after state, utilization, and token values every
	// rule's trigger check needs, so shouldFireLocked/triggered take it as
	// one value instead of six separate params (go:S107).
	tr := sessionTransition{prev: prev, cur: cur, prevUtil: prevUtil, util: util, prevTokens: prevTokens, tokens: tokens}
	var fire []backchannel.Rule
	for _, r := range e.rules.Rules() {
		if e.shouldFireLocked(r, s.Adapter, sid, tr, now) {
			fire = append(fire, r)
		}
	}
	return fire
}

// sessionTransition bundles a session's before/after state, context-window
// utilization, and token count — the values every trigger evaluation needs
// together, threaded through shouldFireLocked/triggered as one value instead
// of six separate params (go:S107).
type sessionTransition struct {
	prev       string
	cur        string
	prevUtil   float64
	util       float64
	prevTokens int64
	tokens     int64
}

// shouldFireLocked evaluates a single rule against this transition and, if it
// should fire, records the fire bookkeeping (lastFired + recent). Caller
// holds e.mu. Mirrors evaluate's original inline per-rule loop body exactly.
func (e *BackchannelEngine) shouldFireLocked(r backchannel.Rule, adapter, sid string, tr sessionTransition, now time.Time) bool {
	if !r.Enabled {
		return false
	}
	if r.Adapter != "" && r.Adapter != adapter {
		return false
	}
	if !triggered(r.Trigger, tr) {
		return false
	}
	// Firing is gated on the master-toggle here (after bookkeeping, so a
	// later enable doesn't replay a stale edge).
	if e.enabled == nil || !e.enabled() {
		return false
	}
	key := r.ID + "\x00" + sid
	if last, ok := e.lastFired[key]; ok && now.Sub(last) < time.Duration(r.Cooldown())*time.Second {
		return false
	}
	if e.overCapLocked(sid, now) {
		e.logger.LogInfo("backchannel", sid, "rule "+r.ID+" suppressed: per-session action cap reached")
		return false
	}
	e.lastFired[key] = now
	e.recordFireLocked(sid, now)
	return true
}

// forget drops all per-session bookkeeping when a session ends, so the maps
// don't grow unbounded over a long-lived daemon cycling through many sessions
// (PR #731 review). Keyed-by-(rule,session) cooldown entries are matched by
// their session suffix.
func (e *BackchannelEngine) forget(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.prevState, sessionID)
	delete(e.prevUtil, sessionID)
	delete(e.prevTokens, sessionID)
	delete(e.recent, sessionID)
	suffix := "\x00" + sessionID
	for k := range e.lastFired {
		if strings.HasSuffix(k, suffix) {
			delete(e.lastFired, k)
		}
	}
}

// RekeySession moves oldID's per-session bookkeeping onto newID when a
// presession (proc-<PID>) is reconciled into the real session that
// superseded it — otherwise an edge-crossing baseline already established on
// the presession (e.g. context utilization already past a rule's threshold)
// is discarded outright, and the real session re-establishes a fresh
// "first sight, never fire" baseline on its own id instead of firing on the
// crossing that already happened (issue #1002, mirroring TerminalObserver.
// RekeySession from issue #997 — see that method for the analogous fix on a
// different subsystem's per-session cache).
//
// Unlike TerminalObserver's unconditional overwrite, this only fills newID's
// entry when it doesn't already have one: if the real session already
// observed its own baseline independently (its first live push arrived
// before the reconciliation hook fired), overwriting it with the
// presession's older reading could revert already-advanced state and cause a
// rule to fire twice for one crossing — worse than the bug being fixed here.
// A no-op for any map where oldID has no tracked entry.
func (e *BackchannelEngine) RekeySession(oldID, newID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if v, ok := e.prevState[oldID]; ok {
		delete(e.prevState, oldID)
		if _, exists := e.prevState[newID]; !exists {
			e.prevState[newID] = v
		}
	}
	if v, ok := e.prevUtil[oldID]; ok {
		delete(e.prevUtil, oldID)
		if _, exists := e.prevUtil[newID]; !exists {
			e.prevUtil[newID] = v
		}
	}
	if v, ok := e.prevTokens[oldID]; ok {
		delete(e.prevTokens, oldID)
		if _, exists := e.prevTokens[newID]; !exists {
			e.prevTokens[newID] = v
		}
	}
	if v, ok := e.recent[oldID]; ok {
		delete(e.recent, oldID)
		if _, exists := e.recent[newID]; !exists {
			e.recent[newID] = v
		}
	}

	oldSuffix := "\x00" + oldID
	newSuffix := "\x00" + newID
	for k, v := range e.lastFired {
		if !strings.HasSuffix(k, oldSuffix) {
			continue
		}
		delete(e.lastFired, k)
		newKey := strings.TrimSuffix(k, oldSuffix) + newSuffix
		if _, exists := e.lastFired[newKey]; !exists {
			e.lastFired[newKey] = v
		}
	}
}

// triggered reports whether a trigger matches this transition. State triggers
// fire on the edge into the state; context_pressure / context_pressure_tokens
// fire on the rising crossing of the threshold (hysteresis is inherent in
// prevUtil→util and prevTokens→tokens respectively).
func triggered(t backchannel.Trigger, tr sessionTransition) bool {
	switch t.Event {
	case backchannel.EventWaiting, backchannel.EventReady, backchannel.EventWorking:
		return tr.cur == t.Event && tr.prev != tr.cur
	case backchannel.EventContextPressure:
		th := t.PressureThreshold()
		return tr.prevUtil < th && tr.util >= th
	case backchannel.EventContextTokens:
		th := t.TokensThreshold()
		return tr.prevTokens < th && tr.tokens >= th
	default:
		return false
	}
}

// runActions executes a fired rule's actions in order, stopping on the first
// failure. Each call re-passes InputService's gates. adapter is the session's
// agent kind, used to translate preset actions into the agent's concrete
// command (issue #754).
func (e *BackchannelEngine) runActions(r backchannel.Rule, sid, adapter string) {
	for _, a := range r.Actions {
		var err error
		switch a.Kind {
		case backchannel.ActionInput:
			if a.Preset != "" {
				cmd, ok := e.presets[adapter][a.Preset]
				if !ok {
					// Unsupported preset for this agent: don't fire the rule
					// rather than send a wrong command (issue #754).
					e.logger.LogInfo("backchannel", sid, "rule "+r.ID+" preset "+a.Preset+" unsupported for "+adapter+"; not firing")
					return
				}
				err = e.input.SendCommand(sid, cmd)
			} else {
				err = e.input.SendCommand(sid, a.Data)
			}
		case backchannel.ActionInterrupt:
			err = e.input.Interrupt(sid)
		default:
			continue
		}
		if err != nil {
			e.logger.LogError("backchannel", sid, "rule "+r.ID+" "+a.Kind+" failed: "+err.Error())
			return
		}
		e.logger.LogInfo("backchannel", sid, "rule "+r.ID+" fired "+a.Kind)
	}
}

// overCapLocked reports whether the session already hit the per-minute action
// cap. Caller holds e.mu.
func (e *BackchannelEngine) overCapLocked(sid string, now time.Time) bool {
	return len(e.prune(sid, now)) >= maxActionsPerSessionPerMinute
}

// recordFireLocked appends a fire timestamp. Caller holds e.mu.
func (e *BackchannelEngine) recordFireLocked(sid string, now time.Time) {
	e.recent[sid] = append(e.prune(sid, now), now)
}

// prune drops fire timestamps older than one minute and returns the survivors.
func (e *BackchannelEngine) prune(sid string, now time.Time) []time.Time {
	cutoff := now.Add(-time.Minute)
	kept := e.recent[sid][:0]
	for _, t := range e.recent[sid] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	e.recent[sid] = kept
	return kept
}
