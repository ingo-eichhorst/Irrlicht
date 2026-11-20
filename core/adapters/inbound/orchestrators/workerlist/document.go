package workerlist

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"irrlicht/core/domain/orchestrator"
)

// Schema v1 of the status file (#2148 triage). Unknown fields are ignored; a
// missing required field, a bad timestamp or `schema` other than 1 makes the
// whole document unreadable, which the adapter reports loudly rather than
// showing a partial pool.
//
//	{
//	  "schema": 1,                          required, must be 1
//	  "name": "Build pool",                 optional group header
//	  "heartbeat_interval_seconds": 60,     optional, > 0, default 60
//	  "teams": [{                           required
//	    "name": "team-a",                   required, unique
//	    "updated": "<RFC 3339>",            required, stamped by the team's own writer
//	    "workers": [{                       required
//	      "name": "team-a-m1-1",            required: <team>-<machine id>-<slot>
//	      "busy": true,                     required
//	      "work_item": "proj-a1b2",         optional
//	      "last_heartbeat": "<RFC 3339>",   optional
//	      "last_run": {"end": "<RFC 3339>", "exit": 0}   optional; exit may be null
//	    }]
//	  }]
//	}
//
// A top-level `updated`, a team's `last_tick`, a worker's `activity`,
// `last_run.start` and `last_run.note` are accepted and ignored: freshness is
// per team, so a file that merges several teams cannot hide one team's dead
// scheduler behind another team's fresh write.
type document struct {
	Schema                   *int    `json:"schema"`
	Name                     string  `json:"name"`
	HeartbeatIntervalSeconds *int    `json:"heartbeat_interval_seconds"`
	Teams                    *[]team `json:"teams"`
}

type team struct {
	Name    string     `json:"name"`
	Updated *time.Time `json:"updated"`
	Workers *[]worker  `json:"workers"`
}

type worker struct {
	Name          string     `json:"name"`
	Busy          *bool      `json:"busy"`
	WorkItem      string     `json:"work_item"`
	LastHeartbeat *time.Time `json:"last_heartbeat"`
	LastRun       *lastRun   `json:"last_run"`
}

type lastRun struct {
	End  *time.Time `json:"end"`
	Exit *int       `json:"exit"`
}

const (
	schemaVersion           = 1
	defaultHeartbeatSeconds = 60
	// staleMultiplier is how many heartbeat intervals may pass without a
	// heartbeat (or a team write) before a busy slot shows as stale.
	staleMultiplier = 3
	defaultName     = "Worker pool"

	workerRole        = "worker"
	workerIcon        = "\U0001F527" // 🔧
	workerDescription = "Pool worker slot"

	// Worker and team status tokens. Deliberately none of the four session
	// state names: a slot is not a session, and clients paint those names in
	// session colours.
	workerBusy        = "busy"
	workerIdle        = "idle"
	statusOperational = "operational"
	statusStale       = "stale"
	statusUnreadable  = "unreadable"
)

// decodeDocument parses and validates one status file.
func decodeDocument(data []byte) (*document, error) {
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if err := doc.validate(); err != nil {
		return nil, err
	}
	return &doc, nil
}

func (d *document) validate() error {
	switch {
	case d.Schema == nil:
		return errors.New("missing schema")
	case *d.Schema != schemaVersion:
		return fmt.Errorf("unsupported schema %d (want %d)", *d.Schema, schemaVersion)
	case d.HeartbeatIntervalSeconds != nil && *d.HeartbeatIntervalSeconds <= 0:
		return fmt.Errorf("heartbeat_interval_seconds %d is not positive", *d.HeartbeatIntervalSeconds)
	case d.Teams == nil:
		return errors.New("missing teams")
	}
	seen := make(map[string]bool, len(*d.Teams))
	for i, t := range *d.Teams {
		if err := t.validate(); err != nil {
			return fmt.Errorf("teams[%d]: %w", i, err)
		}
		// One sub-group per team name: a repeated name would draw one
		// client group twice.
		if seen[t.Name] {
			return fmt.Errorf("teams[%d]: duplicate team name %q", i, t.Name)
		}
		seen[t.Name] = true
	}
	return nil
}

func (t team) validate() error {
	switch {
	case t.Name == "":
		return errors.New("missing name")
	case t.Updated == nil:
		return errors.New("missing updated")
	case t.Workers == nil:
		return errors.New("missing workers")
	}
	for i, w := range *t.Workers {
		switch {
		case w.Name == "":
			return fmt.Errorf("workers[%d]: missing name", i)
		case w.Busy == nil:
			return fmt.Errorf("workers[%d]: missing busy", i)
		}
	}
	return nil
}

// staleAfter is the age past which a heartbeat or team write is stale:
// staleMultiplier heartbeat intervals.
func (d *document) staleAfter() time.Duration {
	secs := defaultHeartbeatSeconds
	if d.HeartbeatIntervalSeconds != nil {
		secs = *d.HeartbeatIntervalSeconds
	}
	return time.Duration(staleMultiplier*secs) * time.Second
}

// buildState maps a validated document to orchestrator.State as of now. It is
// a pure function of (doc, now) — the surface the replay goldens lock.
//
// Every slot is a named worker, so the dashboard lists each one — free or
// busy — as an unattached worker; none carries a SessionID (#2149 joins
// sessions). Root stays empty so the dashboard's Gas Town path fallback never
// runs for a pool.
func buildState(doc *document, now time.Time) *orchestrator.State {
	name := doc.Name
	if name == "" {
		name = defaultName
	}
	st := &orchestrator.State{Adapter: Name, Running: true, Name: name, UpdatedAt: now}
	threshold := doc.staleAfter()
	for _, t := range *doc.Teams {
		st.Codebases = append(st.Codebases, buildTeam(t, now, threshold))
	}
	return st
}

// buildTeam maps one team to a codebase holding one path-less worktree of
// slots. A team whose own `updated` is older than threshold is "stale" —
// how a dead scheduler shows; it never sets Running=false, which would drop
// the whole pool from the dashboard.
func buildTeam(t team, now time.Time, threshold time.Duration) orchestrator.Codebase {
	teamStale := now.Sub(*t.Updated) > threshold
	cb := orchestrator.Codebase{Name: t.Name, Status: statusOperational}
	if teamStale {
		cb.Status = statusStale
	}
	workers := make([]orchestrator.Worker, 0, len(*t.Workers))
	for _, w := range *t.Workers {
		workers = append(workers, buildWorker(w, *t.Updated, teamStale, now, threshold))
	}
	cb.Worktrees = []orchestrator.Worktree{{Workers: workers}}
	return cb
}

// buildWorker maps one slot. A busy slot is stale when its team is stale or
// its last heartbeat — the team's `updated` when it has none, as at the start
// of a tick — is older than threshold; a free slot never is. LastExit is set
// once a run has ended with an exit code.
func buildWorker(w worker, teamUpdated time.Time, teamStale bool, now time.Time, threshold time.Duration) orchestrator.Worker {
	out := orchestrator.Worker{
		Role:        workerRole,
		Icon:        workerIcon,
		Description: workerDescription,
		Name:        w.Name,
		ID:          w.WorkItem,
		State:       workerIdle,
	}
	if *w.Busy {
		out.State = workerBusy
		ref := teamUpdated
		if w.LastHeartbeat != nil {
			ref = *w.LastHeartbeat
		}
		out.Stale = teamStale || now.Sub(ref) > threshold
	}
	if w.LastRun != nil && w.LastRun.End != nil && w.LastRun.Exit != nil {
		exit := *w.LastRun.Exit
		out.LastExit = &exit
	}
	return out
}

// markUnreadable returns st with every team's status set to "unreadable":
// the last good snapshot, shown while the current file cannot be read.
func markUnreadable(st *orchestrator.State) *orchestrator.State {
	for i := range st.Codebases {
		st.Codebases[i].Status = statusUnreadable
	}
	return st
}
