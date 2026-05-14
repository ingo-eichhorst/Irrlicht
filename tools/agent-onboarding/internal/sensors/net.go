package sensors

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Net polls open network connections for the agent's process tree and
// emits a Signal when a connection appears or disappears. Uses lsof,
// which ships with macOS by default and is widely available on Linux.
//
// Polling cadence is 1s — much slower than transcript/pane to keep
// signals.jsonl small; agent network behavior is usually low-frequency
// (a handful of HTTP requests per turn).
//
// Kind: "open" or "close". Payload:
//
//	{"pid": 12345, "proto": "TCP", "host": "1.2.3.4", "port": 443, "state": "ESTABLISHED"}
type Net struct {
	// RootPID is the agent process. Connections of every descendant are tracked.
	RootPID int
	// PollInterval defaults to 1s.
	PollInterval time.Duration
}

const netName = "net"

// Name implements Sensor.
func (n *Net) Name() string { return netName }

type netConn struct {
	PID   int    `json:"pid"`
	Proto string `json:"proto"`
	Host  string `json:"host"`
	Port  int    `json:"port"`
	State string `json:"state,omitempty"`
}

// connKey makes a netConn comparable for set-diff.
func (c netConn) key() string {
	return fmt.Sprintf("%d|%s|%s|%d", c.PID, c.Proto, c.Host, c.Port)
}

// Run implements Sensor.
func (n *Net) Run(ctx context.Context) <-chan Signal {
	out := make(chan Signal, 16)
	poll := n.PollInterval
	if poll <= 0 {
		poll = time.Second
	}
	go func() {
		defer close(out)
		if _, err := exec.LookPath("lsof"); err != nil {
			return
		}
		ticker := time.NewTicker(poll)
		defer ticker.Stop()

		known := map[string]netConn{}
		for {
			tree, err := listDescendants(ctx, n.RootPID)
			if err != nil || len(tree) == 0 {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				continue
			}
			pids := make([]string, 0, len(tree))
			for pid := range tree {
				pids = append(pids, strconv.Itoa(pid))
			}
			conns, err := lsofConns(ctx, strings.Join(pids, ","))
			if err != nil {
				return
			}
			current := map[string]netConn{}
			for _, c := range conns {
				current[c.key()] = c
			}
			// Opens.
			for k, c := range current {
				if _, ok := known[k]; !ok {
					if err := emitNet(ctx, out, "open", c); err != nil {
						return
					}
				}
			}
			// Closes.
			for k, c := range known {
				if _, ok := current[k]; !ok {
					if err := emitNet(ctx, out, "close", c); err != nil {
						return
					}
				}
			}
			known = current
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return out
}

func emitNet(ctx context.Context, out chan<- Signal, kind string, c netConn) error {
	payload, _ := MarshalPayload(c)
	select {
	case out <- Signal{
		Ts:      time.Now().UTC(),
		Sensor:  netName,
		Kind:    kind,
		Payload: json.RawMessage(payload),
	}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// lsofConns runs `lsof -nP -iTCP -iUDP -p <pids>` and parses each line into
// a netConn record. -n suppresses DNS, -P suppresses port-name resolution
// so we record IPs and numeric ports verbatim (synthesis can resolve
// later if needed).
func lsofConns(ctx context.Context, pids string) ([]netConn, error) {
	cmd := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-iUDP", "-p", pids)
	out, err := cmd.Output()
	if err != nil {
		// lsof exits 1 when there are no matches. Treat that as empty.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	var conns []netConn
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "COMMAND") || strings.TrimSpace(line) == "" {
			continue
		}
		// Columns (space-sep, variable width):
		// COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
		// NAME contains the address; everything after column 8 is NAME.
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		pidStr := fields[1]
		nodeProto := fields[7] // "TCP" or "UDP"
		nameAndState := strings.Join(fields[8:], " ")
		c, ok := parseLsofName(pidStr, nodeProto, nameAndState)
		if !ok {
			continue
		}
		conns = append(conns, c)
	}
	return conns, nil
}

// parseLsofName parses the trailing portion of an lsof line.
// Example inputs:
//
//	192.168.1.5:54321->1.2.3.4:443 (ESTABLISHED)
//	*:50125
//	127.0.0.1:8080->127.0.0.1:54321 (CLOSE_WAIT)
//
// We always record the REMOTE endpoint (after "->"). If there is no remote
// endpoint (listening socket), we record the local address as the host.
func parseLsofName(pidStr, proto, name string) (netConn, bool) {
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return netConn{}, false
	}
	// Trim "(STATE)" trailing parens if present.
	state := ""
	if i := strings.LastIndex(name, " ("); i > 0 && strings.HasSuffix(name, ")") {
		state = strings.TrimSuffix(name[i+2:], ")")
		name = name[:i]
	}
	target := name
	if i := strings.Index(name, "->"); i >= 0 {
		target = name[i+2:]
	}
	// target shape "host:port" — host may contain colons (IPv6).
	idx := strings.LastIndex(target, ":")
	if idx < 0 {
		return netConn{}, false
	}
	host := target[:idx]
	port, err := strconv.Atoi(target[idx+1:])
	if err != nil {
		return netConn{}, false
	}
	return netConn{PID: pid, Proto: proto, Host: host, Port: port, State: state}, true
}
