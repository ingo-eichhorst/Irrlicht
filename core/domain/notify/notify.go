// Package notify decides whether, when, and how coalesced a session or
// daemon transition becomes a phone notification — the policy table of
// docs/mobile-notifications-arc42.md §8.4 (discussion #1346) in one place:
// the waiting/ready edges, the ready hold-down, per-(session, edge)
// cooldowns, burst summaries, and the daemon-offline watchdog grace.
//
// The package defines its own Event input and imports nothing from the rest
// of the module (arc42 §5.1): the relay maps its wire frames onto Event, and
// any future second consumer imports the same policy instead of growing
// another transition-logic copy (arc42 ADR-5).
//
// The engine is pure, synchronous and deterministic: no goroutines, no clock
// reads — every entry point takes now, which the caller must keep
// non-decreasing across calls (it passes its wall clock). It is NOT safe for
// concurrent use; the relay hub already serializes every call under its lock
// and owns that responsibility.
package notify

import "time"

// State is a session's position in the three-state machine of events.md:
// working, waiting, ready. The engine tolerates values outside these three —
// wire input is untrusted, and a future state name must degrade silent.
type State string

const (
	StateWorking State = "working"
	StateWaiting State = "waiting"
	StateReady   State = "ready"
)

// EventKind names what happened; anything unrecognized is ignored rather
// than an error, so an older engine survives a newer peer's vocabulary.
type EventKind string

const (
	EventSessionUpdate EventKind = "session_update"
	EventSessionDelete EventKind = "session_delete"
	EventRekey         EventKind = "rekey" // presession → real session (#1002 class)
	EventDaemonUp      EventKind = "daemon_up"
	EventDaemonDown    EventKind = "daemon_down"
)

// Event is the engine's own input type; the relay maps its wire frames onto
// it. Snapshot-reconcile updates need no flag of their own: policy is
// diff-driven, so an unchanged state is a no-op and a first sighting is
// silent either way (arc42 §6.3).
type Event struct {
	Kind EventKind

	// Session events.
	SessionID string
	ParentID  string // non-empty = subagent; never notified (§8.4)
	State     State
	Label     string // human label for on-device composition; may be empty
	Project   string

	// Rekey: OldSessionID's policy state (cooldowns, pending hold-down,
	// last-known state) moves to SessionID.
	OldSessionID string

	// Daemon events.
	DaemonID    string
	DaemonLabel string
}

// PushKind names the notification shape the service worker composes from a
// Payload; the wire never carries prose (arc42 §8.2).
type PushKind string

const (
	PushSession    PushKind = "session"
	PushSummary    PushKind = "summary"
	PushDaemonDown PushKind = "daemon_down"
	PushDaemonUp   PushKind = "daemon_up"
)

// Urgency maps onto the Web Push Urgency header (RFC 8030 §5.3): high wakes
// the device promptly, normal defers to its power state.
type Urgency string

const (
	UrgencyHigh   Urgency = "high"
	UrgencyNormal Urgency = "normal"
)

// Payload is the structured, on-device-composed notification content
// (arc42 §8.2: structured data, never prose). Version guards installed
// service workers against future shape changes.
type Payload struct {
	Version     int      `json:"v"`
	Kind        PushKind `json:"kind"`
	SessionID   string   `json:"session_id,omitempty"`
	Label       string   `json:"label,omitempty"`
	Project     string   `json:"project,omitempty"`
	State       State    `json:"state,omitempty"`
	DaemonID    string   `json:"daemon_id,omitempty"`
	DaemonLabel string   `json:"daemon_label,omitempty"`
	Count       int      `json:"count,omitempty"`    // summary: distinct sessions in the burst window
	Sessions    []string `json:"sessions,omitempty"` // summary: up to 6 member labels, sorted (label, falling back to session id when empty)
	At          int64    `json:"at"`                 // unix seconds of the decision
}

// Push is one delivery decision. Topic is the LOGICAL collapse key (session
// id, "daemon:"+daemonID, or "summary"); it is not yet an RFC 8030-safe
// Topic header — the webpush sender derives that (session ids exceed the
// 32-char header limit). Renotify=false means the device should replace an
// existing notification silently (summary refreshes, daemon_up).
type Push struct {
	Topic    string
	TTL      time.Duration
	Urgency  Urgency
	Renotify bool
	Payload  Payload
}

// Config carries the §8.4 policy knobs. A zero field means "use the
// default" — see New.
type Config struct {
	HoldDown       time.Duration // ready hold-down; default 7s
	Cooldown       time.Duration // per (session, edge); default 60s
	BurstWindow    time.Duration // default 20s
	BurstThreshold int           // default 3: a 4th candidate within BurstWindow becomes a summary
	TTLWaiting     time.Duration // default 1h
	TTLReady       time.Duration // default 10m
	TTLDaemon      time.Duration // default 10m (watchdog + reconnect)
	DaemonGrace    time.Duration // default 60s before a disconnect push (§6.4)
}

// DefaultConfig returns the §8.4 policy defaults. The TTL split is
// deliberate: a stale ready is noise while a waiting stays true, so waiting
// outlives ready by a factor of six.
func DefaultConfig() Config {
	return Config{
		HoldDown:       7 * time.Second,
		Cooldown:       60 * time.Second,
		BurstWindow:    20 * time.Second,
		BurstThreshold: 3,
		TTLWaiting:     time.Hour,
		TTLReady:       10 * time.Minute,
		TTLDaemon:      10 * time.Minute,
		DaemonGrace:    60 * time.Second,
	}
}
