package main

import "time"

// transitionCause distinguishes why a state evaluation happened.
type transitionCause string

const (
	causeInit             transitionCause = "init"
	causeEvent            transitionCause = "event"
	causeDebounceCoalesce transitionCause = "debounce_coalesce"
	causeHook             transitionCause = "hook"
)

// transition is a single recorded state change emitted by the replay.
type transition struct {
	Index         int             `json:"index"`
	EventIndex    int             `json:"event_index"`
	VirtualTime   time.Time       `json:"virtual_time"`
	Cause         transitionCause `json:"cause"`
	PrevState     string          `json:"prev_state"`
	NewState      string          `json:"new_state"`
	Reason        string          `json:"reason"`
	LastEventType string          `json:"last_event_type"`
	HasOpenTool   bool            `json:"has_open_tool"`
	OpenToolNames []string        `json:"open_tool_names,omitempty"`
	IsAgentDone   bool            `json:"is_agent_done"`
	NeedsAttn     bool            `json:"needs_user_attention"`
	WaitingQuery  bool            `json:"waiting_for_user_input"`
	LastTextHead  string          `json:"last_assistant_text_head,omitempty"`
}

// replayReport is the top-level structure written to the output file.
type replayReport struct {
	SchemaVersion    int            `json:"schema_version"`
	SourceTranscript string         `json:"source_transcript"`
	GeneratedAt      time.Time      `json:"generated_at"`
	Settings         reportSettings `json:"settings"`
	Summary          reportSummary  `json:"summary"`
	Transitions      []transition   `json:"transitions"`

	// Sessions is populated when a sidecar is present and provides per-session
	// aggregate statistics (event counts, state durations, PID discovery lag,
	// debounce stats). Nil for transcript-only replays.
	Sessions []sessionTimeline `json:"sessions,omitempty"`

	// extendedCheck diffs the replayed state transitions against the recorded
	// ones so fixtures act as regression tests for the detector.
	ExtendedCheck *extendedCheck `json:"extended_check,omitempty"`
}

// sessionTimeline is a per-session summary within the report, populated from
// the lifecycle sidecar when available.
type sessionTimeline struct {
	SessionID       string           `json:"session_id"`
	Adapter         string           `json:"adapter,omitempty"`
	ParentSessionID string           `json:"parent_session_id,omitempty"`
	FirstSeen       time.Time        `json:"first_seen"`
	LastSeen        time.Time        `json:"last_seen"`
	DurationMs      int64            `json:"duration_ms"`
	EventCount      int              `json:"event_count"`
	StateChanges    int              `json:"state_changes"`
	FinalState      string           `json:"final_state,omitempty"`
	PID             int              `json:"pid,omitempty"`
	PIDDiscoveryMs  int64            `json:"pid_discovery_lag_ms,omitempty"`
	DebounceEvents  int              `json:"debounce_coalesced_events"`
	StateDurations  map[string]int64 `json:"state_durations_ms"`
}

// extendedCheck compares the replayed state transitions against a committed
// lifecycle recording (.events.jsonl sidecar produced by `irrlichd --record`).
type extendedCheck struct {
	SidecarPath         string               `json:"sidecar_path"`
	RecordedCount       int                  `json:"recorded_transition_count"`
	ReplayedCount       int                  `json:"replayed_transition_count"`
	OrderedMatches      int                  `json:"ordered_matches"`
	OrderedMismatches   []transitionMismatch `json:"ordered_mismatches,omitempty"`
	RecordedUniqueKinds []string             `json:"recorded_unique_kinds"`
	ReplayedUniqueKinds []string             `json:"replayed_unique_kinds"`
	MissingKinds        []string             `json:"missing_kinds,omitempty"`
	ExtraKinds          []string             `json:"extra_kinds,omitempty"`
}

// transitionMismatch is a single divergence between replayed and recorded
// state transitions.
type transitionMismatch struct {
	Index    int    `json:"index"`
	Kind     string `json:"kind"` // "missing_in_replay" | "extra_in_replay" | "state_differs"
	Recorded string `json:"recorded,omitempty"`
	Replayed string `json:"replayed,omitempty"`
}

type reportSettings struct {
	Adapter            string        `json:"adapter"`
	SessionFilter      string        `json:"session_filter,omitempty"`
	DebounceWindow     time.Duration `json:"debounce_window"`
	FlickerMaxDuration time.Duration `json:"flicker_max_duration"`
}

type reportSummary struct {
	TotalEvents       int                      `json:"total_events"`
	ConsumedEvents    int                      `json:"consumed_events"`
	TotalTransitions  int                      `json:"total_transitions"`
	FirstEventTime    time.Time                `json:"first_event_time"`
	LastEventTime     time.Time                `json:"last_event_time"`
	WallClockDuration time.Duration            `json:"wall_clock_session_duration"`
	StateDurations    map[string]time.Duration `json:"state_durations"`

	FlickerCount       int            `json:"flicker_count"`
	FlickersByCategory map[string]int `json:"flickers_by_category"`
	FlickersByReason   map[string]int `json:"flickers_by_reason"`

	EstimatedCostUSD       float64 `json:"estimated_cost_usd,omitempty"`
	CumInputTokens         int64   `json:"cum_input_tokens,omitempty"`
	CumOutputTokens        int64   `json:"cum_output_tokens,omitempty"`
	CumCacheReadTokens     int64   `json:"cum_cache_read_tokens,omitempty"`
	CumCacheCreationTokens int64   `json:"cum_cache_creation_tokens,omitempty"`
	ModelName              string  `json:"model_name,omitempty"`
}
