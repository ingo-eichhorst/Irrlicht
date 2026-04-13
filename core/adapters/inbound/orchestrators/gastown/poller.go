// Package gastown — Poller invokes the gt CLI to collect rig, polecat, dog,
// and boot state, enriches it with session data, and maps it to the standardised
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

	"irrlicht/core/domain/orchestrator"
	"irrlicht/core/domain/session"
)

// SessionLister provides read access to active sessions for CWD matching.
type SessionLister interface {
	ListAll() ([]*session.SessionState, error)
}

// Poller queries the gt CLI for rig, polecat, dog, and boot state and maps
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

	// Fetch rig list, polecat list, dog list, and boot status in parallel.
	var wg sync.WaitGroup
	var rigs []RigState
	var polecats []PolecatState
	var dogs []DogState
	var boot *BootStatus

	wg.Add(4)
	go func() { defer wg.Done(); rigs = p.fetchRigs(ctx) }()
	go func() { defer wg.Done(); polecats = p.fetchPolecats(ctx) }()
	go func() { defer wg.Done(); dogs = p.fetchDogs(ctx) }()
	go func() { defer wg.Done(); boot = p.fetchBootStatus(ctx) }()
	wg.Wait()

	running := daemon != nil && daemon.Running
	return p.mapToOrchestratorState(rigs, polecats, dogs, boot, running, now)
}

// mapToOrchestratorState maps raw Gas Town types to the standardised model.
func (p *Poller) mapToOrchestratorState(
	rigs []RigState,
	polecats []PolecatState,
	dogs []DogState,
	boot *BootStatus,
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
	mayorMeta := roleMeta[RoleMayor]
	if mayorSID, mayorState := matchSession(filepath.Join(gtRoot, "mayor")); mayorSID != "" {
		globalAgents = append(globalAgents, orchestrator.GlobalAgent{
			Role:        RoleMayor,
			Icon:        mayorMeta.Icon,
			Description: mayorMeta.Desc,
			SessionID:   mayorSID,
			State:       mayorState,
		})
	} else {
		globalAgents = append(globalAgents, orchestrator.GlobalAgent{
			Role:        RoleMayor,
			Icon:        mayorMeta.Icon,
			Description: mayorMeta.Desc,
			State:       "idle",
		})
	}
	deaconMeta := roleMeta[RoleDeacon]
	if deaconSID, deaconState := matchSession(filepath.Join(gtRoot, "deacon")); deaconSID != "" {
		globalAgents = append(globalAgents, orchestrator.GlobalAgent{
			Role:        RoleDeacon,
			Icon:        deaconMeta.Icon,
			Description: deaconMeta.Desc,
			SessionID:   deaconSID,
			State:       deaconState,
		})
	} else {
		globalAgents = append(globalAgents, orchestrator.GlobalAgent{
			Role:        RoleDeacon,
			Icon:        deaconMeta.Icon,
			Description: deaconMeta.Desc,
			State:       "idle",
		})
	}
	// Boot agent (deacon watchdog).
	bootMeta := roleMeta[RoleBoot]
	if boot != nil {
		bootAgent := orchestrator.GlobalAgent{
			Role:        RoleBoot,
			Icon:        bootMeta.Icon,
			Description: bootMeta.Desc,
		}
		if sid, sState := matchSession(filepath.Join(gtRoot, "deacon", "dogs", "boot")); sid != "" {
			bootAgent.SessionID = sid
			bootAgent.State = sState
		} else if boot.Running || boot.SessionAlive {
			bootAgent.State = "working"
		} else {
			bootAgent.State = "idle"
		}
		globalAgents = append(globalAgents, bootAgent)
	}

	state.GlobalAgents = globalAgents

	// Group polecats by rig.
	polecatsByRig := make(map[string][]PolecatState)
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
		witnessMeta := roleMeta[RoleWitness]
		witnessWorker := orchestrator.Worker{
			Role:        RoleWitness,
			Icon:        witnessMeta.Icon,
			Description: witnessMeta.Desc,
			State:       rig.Witness,
		}
		if sid, sState := matchSession(filepath.Join(gtRoot, rig.Name, "witness")); sid != "" {
			witnessWorker.SessionID = sid
			witnessWorker.State = sState
		}
		mainWorkers = append(mainWorkers, witnessWorker)

		// Refinery worker.
		refineryMeta := roleMeta[RoleRefinery]
		refineryWorker := orchestrator.Worker{
			Role:        RoleRefinery,
			Icon:        refineryMeta.Icon,
			Description: refineryMeta.Desc,
			State:       rig.Refinery,
		}
		if sid, sState := matchSession(filepath.Join(gtRoot, rig.Name, "refinery")); sid != "" {
			refineryWorker.SessionID = sid
			refineryWorker.State = sState
		}
		mainWorkers = append(mainWorkers, refineryWorker)

		// Crew workers.
		crewMeta := roleMeta[RoleCrew]
		for _, s := range sessions {
			if s.CWD == "" {
				continue
			}
			ri := DeriveRole(s.CWD, gtRoot)
			if ri == nil || ri.Role != RoleCrew || ri.Rig != rig.Name {
				continue
			}
			mainWorkers = append(mainWorkers, orchestrator.Worker{
				Role:        RoleCrew,
				Icon:        crewMeta.Icon,
				Description: crewMeta.Desc,
				Name:        ri.Name,
				SessionID:   s.SessionID,
				State:       s.State,
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

			polecatMeta := roleMeta[RolePolecat]
			pcWorker := orchestrator.Worker{
				Role:        RolePolecat,
				Icon:        polecatMeta.Icon,
				Description: polecatMeta.Desc,
				Name:        pc.Name,
				ID:          pc.Issue,
				State:       pc.State,
			}

			if sid, sState := matchSession(filepath.Join(gtRoot, rig.Name, "polecats", pc.Name)); sid != "" {
				pcWorker.SessionID = sid
				pcWorker.State = sState
			}

			pcWorktree.Workers = []orchestrator.Worker{pcWorker}
			worktrees = append(worktrees, pcWorktree)
		}

		// Dog workers assigned to this rig.
		dogMeta := roleMeta[RoleDog]
		for _, dog := range dogs {
			if _, ok := dog.Worktrees[rig.Name]; !ok {
				continue
			}
			dogWorker := orchestrator.Worker{
				Role:        RoleDog,
				Icon:        dogMeta.Icon,
				Description: dogMeta.Desc,
				Name:        dog.Name,
				State:       dog.State,
			}
			if sid, sState := matchSession(filepath.Join(gtRoot, "deacon", "dogs", dog.Name, rig.Name)); sid != "" {
				dogWorker.SessionID = sid
				dogWorker.State = sState
			}
			mainWorkers = append(mainWorkers, dogWorker)
		}
		mainWorktree.Workers = mainWorkers

		cb.Worktrees = worktrees
		codebases = append(codebases, cb)
	}

	sort.Slice(codebases, func(i, j int) bool {
		return codebases[i].Name < codebases[j].Name
	})
	state.Codebases = codebases

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

func (p *Poller) fetchRigs(ctx context.Context) []RigState {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := p.gtCommand(ctx, "rig", "list", "--json").Output()
	if err != nil {
		// Fallback to collector's cached rigs from rigs.json.
		return p.collector.Rigs()
	}
	var rigs []RigState
	if err := json.Unmarshal(out, &rigs); err != nil {
		return p.collector.Rigs()
	}
	return rigs
}

func (p *Poller) fetchPolecats(ctx context.Context) []PolecatState {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := p.gtCommand(ctx, "polecat", "list", "--all", "--json").Output()
	if err != nil {
		return nil
	}
	var polecats []PolecatState
	if err := json.Unmarshal(out, &polecats); err != nil {
		return nil
	}
	return polecats
}

func (p *Poller) fetchDogs(ctx context.Context) []DogState {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := p.gtCommand(ctx, "dog", "list", "--json").Output()
	if err != nil {
		return nil
	}
	var dogs []DogState
	if err := json.Unmarshal(out, &dogs); err != nil {
		return nil
	}
	return dogs
}

func (p *Poller) fetchBootStatus(ctx context.Context) *BootStatus {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := p.gtCommand(ctx, "boot", "status", "--json").Output()
	if err != nil {
		return nil
	}
	var boot BootStatus
	if err := json.Unmarshal(out, &boot); err != nil {
		return nil
	}
	return &boot
}
