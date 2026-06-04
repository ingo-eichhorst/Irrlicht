// Package permission models the consent state behind the permission
// transparency wizard (issue #570). Every read or modification irrlicht
// performs on the user's system is declared as a per-agent permission;
// the daemon exercises nothing — no hook install, no transcript tailing,
// no DB polling — until the user grants it.
package permission

import "maps"

// State is the consent status of one agent permission.
type State string

const (
	// StatePending means the user has never answered. Treated like denied
	// for gating; differs only in wizard display (pending items re-prompt).
	StatePending State = "pending"
	StateGranted State = "granted"
	StateDenied  State = "denied"
)

// Kind classifies what exercising a permission does.
type Kind string

const (
	// KindModify covers permissions that write files outside the daemon
	// home (e.g. hook entries in ~/.claude/settings.json). Grant performs
	// the modification; revoke actively undoes it.
	KindModify Kind = "modify"
	// KindObserve covers read-only monitoring (transcript tailing, DB
	// polling). Grant starts the agent's watchers; revoke stops them.
	KindObserve Kind = "observe"
)

// Set holds the answered state of every agent × permission pair.
// Keys: agent name → permission key → state. Absent keys are pending,
// which is also the upgrade path: a permission newly declared by a later
// daemon version simply resolves to pending until answered, while
// previously answered keys are untouched.
type Set map[string]map[string]State

// Get returns the recorded state for the agent/key pair, defaulting to
// StatePending when the pair has never been answered.
func (s Set) Get(agent, key string) State {
	if st, ok := s[agent][key]; ok {
		return st
	}
	return StatePending
}

// Put records an answer for the agent/key pair.
func (s Set) Put(agent, key string, st State) {
	if s[agent] == nil {
		s[agent] = make(map[string]State)
	}
	s[agent][key] = st
}

// Clone returns a deep copy, so a caller can persist a stable snapshot
// outside the lock that guards the live set.
func (s Set) Clone() Set {
	out := make(Set, len(s))
	for agent, perms := range s {
		out[agent] = maps.Clone(perms)
	}
	return out
}
