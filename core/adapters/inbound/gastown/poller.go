// Package gastown — Poller periodically invokes gt CLI to collect rig and
// polecat state, combining it with the daemon heartbeat into a Snapshot.
package gastown

import (
	"context"
	"encoding/json"
	"os/exec"
	"sync"
	"time"

	"irrlicht/core/domain/gastown"
)

// Poller periodically queries gt CLI for rig and polecat state.
type Poller struct {
	collector *Collector
	gtBin     string // resolved path to gt binary
	interval  time.Duration

	mu       sync.RWMutex
	snapshot *gastown.Snapshot
}

// NewPoller creates a Poller that reads from the given collector and
// shells out to gtBin for rig/polecat state every interval.
func NewPoller(collector *Collector, gtBin string, interval time.Duration) *Poller {
	return &Poller{
		collector: collector,
		gtBin:     gtBin,
		interval:  interval,
	}
}

// Snapshot returns the latest combined Gas Town snapshot.
func (p *Poller) Snapshot() *gastown.Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.snapshot != nil {
		cp := *p.snapshot
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
	snap := gastown.Snapshot{
		Detected:  p.collector.Detected(),
		Daemon:    p.collector.DaemonState(),
		UpdatedAt: time.Now().UTC(),
	}

	if !snap.Detected || p.gtBin == "" {
		p.mu.Lock()
		p.snapshot = &snap
		p.mu.Unlock()
		return
	}

	// Fetch rig list and polecat list in parallel.
	var wg sync.WaitGroup
	var rigs []gastown.RigState
	var polecats []gastown.PolecatState

	wg.Add(2)
	go func() {
		defer wg.Done()
		rigs = p.fetchRigs(ctx)
	}()
	go func() {
		defer wg.Done()
		polecats = p.fetchPolecats(ctx)
	}()
	wg.Wait()

	snap.Rigs = rigs
	snap.Polecats = polecats

	p.mu.Lock()
	p.snapshot = &snap
	p.mu.Unlock()
}

func (p *Poller) fetchRigs(ctx context.Context) []gastown.RigState {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, p.gtBin, "rig", "list", "--json").Output()
	if err != nil {
		return nil
	}
	var rigs []gastown.RigState
	if err := json.Unmarshal(out, &rigs); err != nil {
		return nil
	}
	return rigs
}

func (p *Poller) fetchPolecats(ctx context.Context) []gastown.PolecatState {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, p.gtBin, "polecat", "list", "--all", "--json").Output()
	if err != nil {
		return nil
	}
	var polecats []gastown.PolecatState
	if err := json.Unmarshal(out, &polecats); err != nil {
		return nil
	}
	return polecats
}
