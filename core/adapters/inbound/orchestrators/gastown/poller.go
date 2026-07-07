// Package gastown — poller invokes the gt CLI to collect rig, polecat, dog,
// and boot state, enriches it with session data, and maps it to the standardised
// orchestrator.State model.
package gastown

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"irrlicht/core/domain/orchestrator"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// sessionLister provides read access to active sessions for CWD matching.
type sessionLister interface {
	ListAll() ([]*session.SessionState, error)
}

// defaultFetchTimeout bounds each gt CLI subprocess call in production.
const defaultFetchTimeout = 5 * time.Second

// jsonFlag requests machine-readable output from gt CLI subcommands.
const jsonFlag = "--json"

// poller queries the gt CLI for rig, polecat, dog, and boot state and maps
// the results to the standardised orchestrator.State model.
type poller struct {
	collector *collector
	gtBin     string
	sessions  sessionLister
	// fetchTimeout bounds each gt CLI call. Defaults to defaultFetchTimeout;
	// the replay test raises it so a load-starved fake-gt subprocess can't
	// falsely time out into the fallback path (#586).
	fetchTimeout time.Duration
	// logger records gt fetch timeouts so silent fallback to cached/empty
	// state is observable in production. May be nil (e.g. tests).
	logger outbound.Logger
	// timeoutMu guards timingOut, which tracks per-fetch consecutive-timeout
	// streaks so a wedged gt binary logs once per streak, not every tick (#625).
	timeoutMu sync.Mutex
	timingOut map[string]bool
}

// newPoller creates a poller that reads from the given collector and
// shells out to gtBin for rig/polecat state.
func newPoller(collector *collector, gtBin string, sessions sessionLister) *poller {
	return &poller{
		collector:    collector,
		gtBin:        gtBin,
		sessions:     sessions,
		fetchTimeout: defaultFetchTimeout,
		timingOut:    make(map[string]bool),
	}
}

// BuildOrchestratorState fetches current gt state and returns the standardised
// orchestrator.State model. Called by Adapter on each poll tick.
func (p *poller) BuildOrchestratorState(ctx context.Context) *orchestrator.State {
	daemon := p.collector.daemonState()
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
	var rigs []rigState
	var polecats []polecatState
	var dogs []dogState
	var boot *bootStatus

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
func (p *poller) mapToOrchestratorState(
	rigs []rigState,
	polecats []polecatState,
	dogs []dogState,
	boot *bootStatus,
	running bool,
	now time.Time,
) *orchestrator.State {
	gtRoot := p.collector.Root()

	icons := make(map[string]string, len(roleMeta))
	for role, meta := range roleMeta {
		icons[role] = meta.Icon
	}
	state := &orchestrator.State{
		Adapter:   "gastown",
		Running:   running,
		Root:      gtRoot,
		UpdatedAt: now,
		RoleIcons: icons,
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
	polecatsByRig := make(map[string][]polecatState)
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
			ri := deriveRole(s.CWD, gtRoot)
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
func (p *poller) gtCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, p.gtBin, args...)
	cmd.Dir = p.collector.Root()
	return cmd
}

// noteFetchTimeout logs a gt fetch timeout exactly once per consecutive-timeout
// streak so a wedged gt binary can't spam the log every tick (#625). fetch names
// the timed-out call (e.g. "rig list") and fellBackTo describes the degraded
// result it composed instead ("cached rigs" / "empty"). A non-timeout error
// (bad JSON, gt missing) clears the streak via clearFetchTimeout so the next
// genuine timeout logs again.
func (p *poller) noteFetchTimeout(fetch, fellBackTo string) {
	if p.logger == nil {
		return
	}
	p.timeoutMu.Lock()
	already := p.timingOut[fetch]
	p.timingOut[fetch] = true
	p.timeoutMu.Unlock()
	if already {
		return
	}
	p.logger.LogError("gastown-poller", "",
		"gt "+fetch+" timed out after "+p.fetchTimeout.String()+
			"; falling back to "+fellBackTo+" state (rig/polecat counts may be stale)")
}

// clearFetchTimeout resets a fetch's timeout streak after a non-timeout outcome
// (success or a non-deadline error) so a later timeout is logged afresh.
func (p *poller) clearFetchTimeout(fetch string) {
	p.timeoutMu.Lock()
	delete(p.timingOut, fetch)
	p.timeoutMu.Unlock()
}

// recordFetch inspects a failed gt fetch: on a deadline timeout it logs the
// degraded fallback (rate-limited per streak); otherwise it clears the streak.
func (p *poller) recordFetch(ctx context.Context, fetch, fellBackTo string) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		p.noteFetchTimeout(fetch, fellBackTo)
		return
	}
	p.clearFetchTimeout(fetch)
}

func (p *poller) fetchRigs(ctx context.Context) []rigState {
	ctx, cancel := context.WithTimeout(ctx, p.fetchTimeout)
	defer cancel()

	out, err := p.gtCommand(ctx, "rig", "list", jsonFlag).Output()
	if err != nil {
		// Fallback to collector's cached rigs from rigs.json.
		p.recordFetch(ctx, "rig list", "cached rigs.json")
		return p.collector.Rigs()
	}
	p.clearFetchTimeout("rig list")
	var rigs []rigState
	if err := json.Unmarshal(out, &rigs); err != nil {
		return p.collector.Rigs()
	}
	return rigs
}

func (p *poller) fetchPolecats(ctx context.Context) []polecatState {
	ctx, cancel := context.WithTimeout(ctx, p.fetchTimeout)
	defer cancel()

	out, err := p.gtCommand(ctx, "polecat", "list", "--all", jsonFlag).Output()
	if err != nil {
		p.recordFetch(ctx, "polecat list", "empty")
		return nil
	}
	p.clearFetchTimeout("polecat list")
	var polecats []polecatState
	if err := json.Unmarshal(out, &polecats); err != nil {
		return nil
	}
	return polecats
}

func (p *poller) fetchDogs(ctx context.Context) []dogState {
	ctx, cancel := context.WithTimeout(ctx, p.fetchTimeout)
	defer cancel()

	out, err := p.gtCommand(ctx, "dog", "list", jsonFlag).Output()
	if err != nil {
		p.recordFetch(ctx, "dog list", "empty")
		return nil
	}
	p.clearFetchTimeout("dog list")
	var dogs []dogState
	if err := json.Unmarshal(out, &dogs); err != nil {
		return nil
	}
	return dogs
}

func (p *poller) fetchBootStatus(ctx context.Context) *bootStatus {
	ctx, cancel := context.WithTimeout(ctx, p.fetchTimeout)
	defer cancel()

	out, err := p.gtCommand(ctx, "boot", "status", jsonFlag).Output()
	if err != nil {
		p.recordFetch(ctx, "boot status", "empty")
		return nil
	}
	p.clearFetchTimeout("boot status")
	var boot bootStatus
	if err := json.Unmarshal(out, &boot); err != nil {
		return nil
	}
	return &boot
}
