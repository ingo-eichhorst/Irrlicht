// Package gastown — Poller periodically invokes gt CLI to collect rig and
// polecat state, enriches it with session data, and builds the gastown_state model.
package gastown

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"irrlicht/core/domain/gastown"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// SessionLister provides read access to active sessions for CWD matching.
type SessionLister interface {
	ListAll() ([]*session.SessionState, error)
}

// Poller periodically queries gt CLI for rig, polecat, and convoy state,
// and builds the enriched gastown_state by joining with active sessions.
type Poller struct {
	collector   *Collector
	gtBin       string // resolved path to gt binary
	interval    time.Duration
	sessions    SessionLister
	broadcaster outbound.PushBroadcaster

	mu       sync.RWMutex
	snapshot *gastown.Snapshot // legacy flat snapshot
	state    *gastown.State   // enriched gastown_state
}

// NewPoller creates a Poller that reads from the given collector and
// shells out to gtBin for rig/polecat state every interval.
// sessions and broadcaster may be nil.
func NewPoller(collector *Collector, gtBin string, interval time.Duration, sessions SessionLister, broadcaster outbound.PushBroadcaster) *Poller {
	return &Poller{
		collector:   collector,
		gtBin:       gtBin,
		interval:    interval,
		sessions:    sessions,
		broadcaster: broadcaster,
	}
}

// Snapshot returns the latest combined Gas Town snapshot (legacy format).
func (p *Poller) Snapshot() *gastown.Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.snapshot != nil {
		cp := *p.snapshot
		return &cp
	}
	return nil
}

// State returns the latest enriched Gas Town state.
func (p *Poller) State() *gastown.State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.state != nil {
		cp := *p.state
		return &cp
	}
	return nil
}

// Run starts the polling loop. It blocks until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) error {
	// Initial poll immediately.
	p.poll(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	daemon := p.collector.DaemonState()
	now := time.Now().UTC()

	snap := gastown.Snapshot{
		Detected:  p.collector.Detected(),
		Daemon:    daemon,
		UpdatedAt: now,
	}

	if !snap.Detected || p.gtBin == "" {
		p.mu.Lock()
		p.snapshot = &snap
		p.state = &gastown.State{
			Type:      outbound.PushTypeGasTownState,
			Running:   false,
			GTRoot:    p.collector.Root(),
			UpdatedAt: now,
		}
		p.mu.Unlock()
		p.broadcastState()
		return
	}

	// Fetch rig list, polecat list, and convoy list in parallel.
	var wg sync.WaitGroup
	var rigs []gastown.RigState
	var polecats []gastown.PolecatState
	var convoys []gastown.ConvoyState

	wg.Add(3)
	go func() {
		defer wg.Done()
		rigs = p.fetchRigs(ctx)
	}()
	go func() {
		defer wg.Done()
		polecats = p.fetchPolecats(ctx)
	}()
	go func() {
		defer wg.Done()
		convoys = p.fetchConvoys(ctx)
	}()
	wg.Wait()

	snap.Rigs = rigs
	snap.Polecats = polecats

	// Build enriched state.
	running := daemon != nil && daemon.Running
	state := p.buildState(rigs, polecats, convoys, running, now)

	p.mu.Lock()
	p.snapshot = &snap
	p.state = state
	p.mu.Unlock()

	p.broadcastState()
}

// buildState constructs the enriched gastown_state from collected data and sessions.
func (p *Poller) buildState(rigs []gastown.RigState, polecats []gastown.PolecatState, convoys []gastown.ConvoyState, running bool, now time.Time) *gastown.State {
	gtRoot := p.collector.Root()

	state := &gastown.State{
		Type:      outbound.PushTypeGasTownState,
		Running:   running,
		GTRoot:    gtRoot,
		UpdatedAt: now,
	}

	// Get all sessions for CWD matching.
	var sessions []*session.SessionState
	if p.sessions != nil {
		sessions, _ = p.sessions.ListAll()
	}

	// Build session index: CWD → session for fast lookup.
	type sessionInfo struct {
		sessionID string
		state     string
	}
	cwdToSession := make(map[string]sessionInfo)
	for _, s := range sessions {
		if s.CWD == "" {
			continue
		}
		cwdToSession[filepath.Clean(s.CWD)] = sessionInfo{
			sessionID: s.SessionID,
			state:     s.State,
		}
	}

	// matchSession checks if any session has a CWD under the given path.
	matchSession := func(basePath string) (string, string) {
		basePath = filepath.Clean(basePath)
		for cwd, info := range cwdToSession {
			rel, err := filepath.Rel(basePath, cwd)
			if err != nil {
				continue
			}
			if len(rel) > 0 && rel[0] != '.' {
				return info.sessionID, info.state
			}
			if rel == "." {
				return info.sessionID, info.state
			}
		}
		return "", ""
	}

	// Find global agents (mayor, deacon).
	globalAgents := []gastown.GlobalAgent{}
	if mayorSID, mayorState := matchSession(filepath.Join(gtRoot, "mayor")); mayorSID != "" {
		globalAgents = append(globalAgents, gastown.GlobalAgent{
			Role:      gastown.RoleMayor,
			SessionID: mayorSID,
			State:     mayorState,
		})
	} else {
		// Mayor might exist but have no active session.
		globalAgents = append(globalAgents, gastown.GlobalAgent{
			Role:  gastown.RoleMayor,
			State: "idle",
		})
	}
	if deaconSID, deaconState := matchSession(filepath.Join(gtRoot, "deacon")); deaconSID != "" {
		globalAgents = append(globalAgents, gastown.GlobalAgent{
			Role:      gastown.RoleDeacon,
			SessionID: deaconSID,
			State:     deaconState,
		})
	} else {
		globalAgents = append(globalAgents, gastown.GlobalAgent{
			Role:  gastown.RoleDeacon,
			State: "idle",
		})
	}
	state.GlobalAgents = globalAgents

	// Group polecats by rig for easy lookup.
	polecatsByRig := make(map[string][]gastown.PolecatState)
	for _, pc := range polecats {
		polecatsByRig[pc.Rig] = append(polecatsByRig[pc.Rig], pc)
	}

	// Build codebases from rigs.
	codebases := make([]gastown.Codebase, 0, len(rigs))
	for _, rig := range rigs {
		cb := gastown.Codebase{
			Rig:    rig.Name,
			Status: rig.Status,
		}

		// Main worktree: witness + refinery + crew agents.
		mainWorktree := gastown.Worktree{
			Path:   filepath.Join(gtRoot, rig.Name),
			IsMain: true,
		}

		mainAgents := []gastown.Agent{}

		// Witness agent.
		witnessAgent := gastown.Agent{
			Role:  gastown.RoleWitness,
			State: rig.Witness,
		}
		if sid, sState := matchSession(filepath.Join(gtRoot, rig.Name, "witness")); sid != "" {
			witnessAgent.SessionID = sid
			witnessAgent.State = sState
		}
		mainAgents = append(mainAgents, witnessAgent)

		// Refinery agent.
		refineryAgent := gastown.Agent{
			Role:  gastown.RoleRefinery,
			State: rig.Refinery,
		}
		if sid, sState := matchSession(filepath.Join(gtRoot, rig.Name, "refinery")); sid != "" {
			refineryAgent.SessionID = sid
			refineryAgent.State = sState
		}
		mainAgents = append(mainAgents, refineryAgent)

		// Crew agents: scan sessions with CWD under $GT_ROOT/<rig>/crew/*.
		for _, s := range sessions {
			if s.CWD == "" {
				continue
			}
			ri := gastown.DeriveRole(s.CWD, gtRoot)
			if ri == nil || ri.Role != gastown.RoleCrew || ri.Rig != rig.Name {
				continue
			}
			mainAgents = append(mainAgents, gastown.Agent{
				Role:      gastown.RoleCrew,
				Name:      ri.Name,
				SessionID: s.SessionID,
				State:     s.State,
			})
		}

		mainWorktree.Agents = mainAgents
		worktrees := []gastown.Worktree{mainWorktree}

		// Polecat worktrees: one per polecat.
		rigPolecats := polecatsByRig[rig.Name]
		for _, pc := range rigPolecats {
			pcWorktree := gastown.Worktree{
				Path:   filepath.Join(gtRoot, rig.Name, "polecats", pc.Name),
				IsMain: false,
			}

			pcAgent := gastown.Agent{
				Role:   gastown.RolePolecat,
				Name:   pc.Name,
				BeadID: pc.Issue,
				State:  pc.State,
			}

			if sid, sState := matchSession(filepath.Join(gtRoot, rig.Name, "polecats", pc.Name)); sid != "" {
				pcAgent.SessionID = sid
				pcAgent.State = sState
			}

			pcWorktree.Agents = []gastown.Agent{pcAgent}
			worktrees = append(worktrees, pcWorktree)
		}

		cb.Worktrees = worktrees
		codebases = append(codebases, cb)
	}

	// Sort codebases by rig name for stable ordering.
	sort.Slice(codebases, func(i, j int) bool {
		return codebases[i].Rig < codebases[j].Rig
	})
	state.Codebases = codebases

	// Build work units from convoys.
	workUnits := make([]gastown.WorkUnit, 0, len(convoys))
	for _, c := range convoys {
		workUnits = append(workUnits, gastown.WorkUnit{
			ID:     c.ID,
			Type:   gastown.WorkUnitConvoy,
			Name:   c.Title,
			Source: gastown.SourceGasTown,
			Total:  c.Total,
			Done:   c.Completed,
		})
	}
	state.WorkUnits = workUnits

	return state
}

func (p *Poller) broadcastState() {
	if p.broadcaster == nil {
		return
	}
	p.mu.RLock()
	state := p.state
	p.mu.RUnlock()

	if state == nil {
		return
	}

	p.broadcaster.Broadcast(outbound.PushMessage{
		Type:    outbound.PushTypeGasTownState,
		GasTown: state,
	})
}

// gtCommand creates an exec.Cmd that runs from GT_ROOT so the gt CLI
// can find its workspace context.
func (p *Poller) gtCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, p.gtBin, args...)
	cmd.Dir = p.collector.Root()
	return cmd
}

func (p *Poller) fetchRigs(ctx context.Context) []gastown.RigState {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := p.gtCommand(ctx, "rig", "list", "--json").Output()
	if err != nil {
		// Fallback to collector's cached rigs from rigs.json.
		return p.collector.Rigs()
	}
	var rigs []gastown.RigState
	if err := json.Unmarshal(out, &rigs); err != nil {
		return p.collector.Rigs()
	}
	return rigs
}

func (p *Poller) fetchPolecats(ctx context.Context) []gastown.PolecatState {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := p.gtCommand(ctx, "polecat", "list", "--all", "--json").Output()
	if err != nil {
		return nil
	}
	var polecats []gastown.PolecatState
	if err := json.Unmarshal(out, &polecats); err != nil {
		return nil
	}
	return polecats
}

func (p *Poller) fetchConvoys(ctx context.Context) []gastown.ConvoyState {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := p.gtCommand(ctx, "convoy", "list", "--json").Output()
	if err != nil {
		return nil
	}
	var convoys []gastown.ConvoyState
	if err := json.Unmarshal(out, &convoys); err != nil {
		return nil
	}
	return convoys
}
