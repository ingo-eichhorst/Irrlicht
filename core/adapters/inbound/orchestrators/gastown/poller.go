// Package gastown — Poller invokes the gt CLI to collect rig, polecat, and
// convoy state, enriches it with session data, and maps it to the standardised
// orchestrator.State model.
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
	"irrlicht/core/domain/orchestrator"
	"irrlicht/core/domain/session"
)

// SessionLister provides read access to active sessions for CWD matching.
type SessionLister interface {
	ListAll() ([]*session.SessionState, error)
}

// Poller queries the gt CLI for rig, polecat, and convoy state and maps
// the results to the standardised orchestrator.State model.
type Poller struct {
	collector *Collector
	gtBin     string
	interval  time.Duration
	sessions  SessionLister
}

// NewPoller creates a Poller that reads from the given collector and
// shells out to gtBin for rig/polecat state.
func NewPoller(collector *Collector, gtBin string, interval time.Duration, sessions SessionLister) *Poller {
	return &Poller{
		collector: collector,
		gtBin:     gtBin,
		interval:  interval,
		sessions:  sessions,
	}
}

// BuildOrchestratorState fetches current gt state and returns the standardised
// orchestrator.State model. Called by Adapter on each poll tick.
func (p *Poller) BuildOrchestratorState(ctx context.Context) *orchestrator.State {
	daemon := p.collector.DaemonState()
	now := time.Now().UTC()
	gtRoot := p.collector.Root()

	if !p.collector.Detected() || p.gtBin == "" {
		return &orchestrator.State{
			Adapter:   "gastown",
			Running:   false,
			Root:      gtRoot,
			UpdatedAt: now,
		}
	}

	// Fetch rig list, polecat list, and convoy list in parallel.
	var wg sync.WaitGroup
	var rigs []gastown.RigState
	var polecats []gastown.PolecatState
	var convoys []gastown.ConvoyState

	wg.Add(3)
	go func() { defer wg.Done(); rigs = p.fetchRigs(ctx) }()
	go func() { defer wg.Done(); polecats = p.fetchPolecats(ctx) }()
	go func() { defer wg.Done(); convoys = p.fetchConvoys(ctx) }()
	wg.Wait()

	running := daemon != nil && daemon.Running
	return p.mapToOrchestratorState(rigs, polecats, convoys, running, now)
}

// mapToOrchestratorState maps raw Gas Town types to the standardised model.
func (p *Poller) mapToOrchestratorState(
	rigs []gastown.RigState,
	polecats []gastown.PolecatState,
	convoys []gastown.ConvoyState,
	running bool,
	now time.Time,
) *orchestrator.State {
	gtRoot := p.collector.Root()

	state := &orchestrator.State{
		Adapter:   "gastown",
		Running:   running,
		Root:      gtRoot,
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

	// Global agents (mayor, deacon).
	globalAgents := []orchestrator.GlobalAgent{}
	if mayorSID, mayorState := matchSession(filepath.Join(gtRoot, "mayor")); mayorSID != "" {
		globalAgents = append(globalAgents, orchestrator.GlobalAgent{
			Role:      gastown.RoleMayor,
			SessionID: mayorSID,
			State:     mayorState,
		})
	} else {
		globalAgents = append(globalAgents, orchestrator.GlobalAgent{
			Role:  gastown.RoleMayor,
			State: "idle",
		})
	}
	if deaconSID, deaconState := matchSession(filepath.Join(gtRoot, "deacon")); deaconSID != "" {
		globalAgents = append(globalAgents, orchestrator.GlobalAgent{
			Role:      gastown.RoleDeacon,
			SessionID: deaconSID,
			State:     deaconState,
		})
	} else {
		globalAgents = append(globalAgents, orchestrator.GlobalAgent{
			Role:  gastown.RoleDeacon,
			State: "idle",
		})
	}
	state.GlobalAgents = globalAgents

	// Group polecats by rig.
	polecatsByRig := make(map[string][]gastown.PolecatState)
	for _, pc := range polecats {
		polecatsByRig[pc.Rig] = append(polecatsByRig[pc.Rig], pc)
	}

	// Build codebases from rigs.
	codebases := make([]orchestrator.Codebase, 0, len(rigs))
	for _, rig := range rigs {
		cb := orchestrator.Codebase{
			Name:   rig.Name,
			Status: rig.Status,
		}

		// Main worktree: witness + refinery + crew workers.
		mainWorktree := orchestrator.Worktree{
			Path:   filepath.Join(gtRoot, rig.Name),
			IsMain: true,
		}

		mainWorkers := []orchestrator.Worker{}

		// Witness worker.
		witnessWorker := orchestrator.Worker{
			Role:  gastown.RoleWitness,
			State: rig.Witness,
		}
		if sid, sState := matchSession(filepath.Join(gtRoot, rig.Name, "witness")); sid != "" {
			witnessWorker.SessionID = sid
			witnessWorker.State = sState
		}
		mainWorkers = append(mainWorkers, witnessWorker)

		// Refinery worker.
		refineryWorker := orchestrator.Worker{
			Role:  gastown.RoleRefinery,
			State: rig.Refinery,
		}
		if sid, sState := matchSession(filepath.Join(gtRoot, rig.Name, "refinery")); sid != "" {
			refineryWorker.SessionID = sid
			refineryWorker.State = sState
		}
		mainWorkers = append(mainWorkers, refineryWorker)

		// Crew workers.
		for _, s := range sessions {
			if s.CWD == "" {
				continue
			}
			ri := gastown.DeriveRole(s.CWD, gtRoot)
			if ri == nil || ri.Role != gastown.RoleCrew || ri.Rig != rig.Name {
				continue
			}
			mainWorkers = append(mainWorkers, orchestrator.Worker{
				Role:      gastown.RoleCrew,
				Name:      ri.Name,
				SessionID: s.SessionID,
				State:     s.State,
			})
		}

		mainWorktree.Workers = mainWorkers
		worktrees := []orchestrator.Worktree{mainWorktree}

		// Polecat worktrees.
		rigPolecats := polecatsByRig[rig.Name]
		for _, pc := range rigPolecats {
			pcWorktree := orchestrator.Worktree{
				Path:   filepath.Join(gtRoot, rig.Name, "polecats", pc.Name),
				IsMain: false,
			}

			pcWorker := orchestrator.Worker{
				Role:  gastown.RolePolecat,
				Name:  pc.Name,
				ID:    pc.Issue,
				State: pc.State,
			}

			if sid, sState := matchSession(filepath.Join(gtRoot, rig.Name, "polecats", pc.Name)); sid != "" {
				pcWorker.SessionID = sid
				pcWorker.State = sState
			}

			pcWorktree.Workers = []orchestrator.Worker{pcWorker}
			worktrees = append(worktrees, pcWorktree)
		}

		cb.Worktrees = worktrees
		codebases = append(codebases, cb)
	}

	sort.Slice(codebases, func(i, j int) bool {
		return codebases[i].Name < codebases[j].Name
	})
	state.Codebases = codebases

	// Work units from convoys.
	workUnits := make([]orchestrator.WorkUnit, 0, len(convoys))
	for _, c := range convoys {
		workUnits = append(workUnits, orchestrator.WorkUnit{
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

// --- gt CLI helpers ----------------------------------------------------------

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
