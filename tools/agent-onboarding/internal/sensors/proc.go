package sensors

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Proc polls the process tree rooted at RootPID, emitting one Signal
// whenever a descendant process appears or disappears. Detects external
// tools the agent shells out to (Bash subshells, MCP servers, language
// runtimes) that don't show up in transcript signals.
//
// Implementation: shell out to `ps -ax -o pid,ppid,args` and walk the
// parent chain. Works on macOS + Linux without build tags. Slower than
// /proc polling but portable.
//
// Kind: "spawn" or "exit". Payload:
//
//	{"pid": 12345, "ppid": 1234, "args": ["bash", "-c", "..."]}
//
// `args` is split on whitespace from the `ps` `args` column; quoted
// substrings are preserved as single elements when ps emits them, but ps
// doesn't always do so reliably, so callers should treat args as a
// best-effort hint rather than ground truth.
type Proc struct {
	// RootPID is the agent process. Descendants of this PID are tracked.
	RootPID int
	// PollInterval defaults to 500ms.
	PollInterval time.Duration
}

const procName = "proc"

// Name implements Sensor.
func (p *Proc) Name() string { return procName }

type procEntry struct {
	PID  int      `json:"pid"`
	PPID int      `json:"ppid"`
	Args []string `json:"args"`
}

// Run implements Sensor.
func (p *Proc) Run(ctx context.Context) <-chan Signal {
	out := make(chan Signal, 16)
	poll := p.PollInterval
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	go func() {
		defer close(out)
		ticker := time.NewTicker(poll)
		defer ticker.Stop()

		known := map[int]procEntry{}
		for {
			tree, err := listDescendants(ctx, p.RootPID)
			if err != nil {
				// ps unavailable or transient failure — exit cleanly so the
				// recorder can continue without this sensor.
				return
			}
			// Detect spawns.
			for pid, e := range tree {
				if _, ok := known[pid]; !ok {
					if err := emit(ctx, out, "spawn", e); err != nil {
						return
					}
				}
			}
			// Detect exits.
			for pid, e := range known {
				if _, ok := tree[pid]; !ok {
					if err := emit(ctx, out, "exit", e); err != nil {
						return
					}
				}
			}
			known = tree
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return out
}

func emit(ctx context.Context, out chan<- Signal, kind string, e procEntry) error {
	payload, _ := MarshalPayload(e)
	select {
	case out <- Signal{
		Ts:      time.Now().UTC(),
		Sensor:  procName,
		Kind:    kind,
		Payload: json.RawMessage(payload),
	}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// listDescendants runs `ps -ax -o pid,ppid,args` and returns every entry
// whose parent chain leads back to root.
func listDescendants(ctx context.Context, root int) (map[int]procEntry, error) {
	cmd := exec.CommandContext(ctx, "ps", "-ax", "-o", "pid=,ppid=,args=")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	// First pass: collect every process.
	all := map[int]procEntry{}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // long argv lines
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Format: "<pid> <ppid> <args...>" (ps right-aligns numeric cols).
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		args := fields[2:]
		all[pid] = procEntry{PID: pid, PPID: ppid, Args: args}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Second pass: walk the tree from root.
	descendants := map[int]procEntry{}
	frontier := []int{root}
	for len(frontier) > 0 {
		parent := frontier[0]
		frontier = frontier[1:]
		for pid, e := range all {
			if e.PPID == parent {
				descendants[pid] = e
				frontier = append(frontier, pid)
			}
		}
	}
	// Include root itself if it's still alive.
	if e, ok := all[root]; ok {
		descendants[root] = e
	}
	return descendants, nil
}
