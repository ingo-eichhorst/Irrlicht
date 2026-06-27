// Package backchannel defines the event→action rules that auto-drive a
// controllable agent session when a lifecycle event fires (issue #724). A rule
// pairs a trigger (a state transition, or a context-pressure crossing) with an
// ordered list of actions (text to inject, or an interrupt). The engine that
// evaluates rules lives in application/services; this package is the pure
// data model shared by the engine, the persistence store, and the HTTP API.
package backchannel

// Trigger event names.
const (
	EventWaiting         = "waiting" // session entered the waiting state
	EventReady           = "ready"   // session entered the ready state
	EventWorking         = "working" // session entered the working state
	EventContextPressure = "context_pressure"
)

// Action kinds.
const (
	ActionInput     = "input"     // inject Data (or a Preset's command) into the session
	ActionInterrupt = "interrupt" // deliver an interrupt (Data ignored)
)

// Preset ids. A preset is an agent-agnostic semantic command (issue #754): the
// rule stores a preset id, and the daemon translates it into the concrete
// command for the session's agent (adapter-declared) and appends the submit
// sequence the session's terminal backend needs. Start small — Compact is the
// only one mapped today; an agent with no mapping degrades gracefully (the rule
// doesn't fire). Interrupt is already semantic via ActionInterrupt.
const (
	PresetCompact = "compact" // compact / summarize the conversation (Claude Code: /compact)
)

// DefaultPressureThreshold is the context-utilization percentage (0–100) at
// which a context_pressure trigger fires when a rule sets no Threshold.
const DefaultPressureThreshold = 85.0

// DefaultCooldownSeconds bounds how often one rule may fire for one session.
const DefaultCooldownSeconds = 60

// Trigger says when a rule fires.
type Trigger struct {
	// Event is one of the Event* constants.
	Event string `json:"event"`
	// Threshold is the context-utilization percentage (0–100) for a
	// context_pressure trigger; ignored for state triggers. Zero means
	// DefaultPressureThreshold.
	Threshold float64 `json:"threshold,omitempty"`
}

// Action is one response step, fired in order.
type Action struct {
	Kind string `json:"kind"` // ActionInput | ActionInterrupt
	// Preset, when set on an ActionInput, names an agent-agnostic command the
	// daemon translates per the session's agent + terminal backend (issue
	// #754). One of the Preset* ids. When empty, Data is sent verbatim (Custom).
	Preset string `json:"preset,omitempty"`
	// Data is the raw text injected for a Custom ActionInput (Preset empty),
	// sent byte-for-byte including any submit sequence the user typed. Ignored
	// when Preset is set.
	Data string `json:"data,omitempty"`
}

// Rule is one configured event→action automation.
type Rule struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	// Name is a human label for the Settings UI (optional).
	Name    string  `json:"name,omitempty"`
	Trigger Trigger `json:"trigger"`
	// Actions fire in order when the trigger matches.
	Actions []Action `json:"actions"`
	// Adapter, when set, scopes the rule to sessions of that adapter
	// (e.g. "claude-code"). Empty means all controllable sessions.
	Adapter string `json:"adapter,omitempty"`
	// CooldownSeconds overrides DefaultCooldownSeconds for this rule.
	CooldownSeconds int `json:"cooldown_seconds,omitempty"`
}

// Cooldown returns the effective cooldown in seconds (default-filled).
func (r Rule) Cooldown() int {
	if r.CooldownSeconds > 0 {
		return r.CooldownSeconds
	}
	return DefaultCooldownSeconds
}

// PressureThreshold returns the effective context-pressure threshold.
func (t Trigger) PressureThreshold() float64 {
	if t.Threshold > 0 {
		return t.Threshold
	}
	return DefaultPressureThreshold
}
