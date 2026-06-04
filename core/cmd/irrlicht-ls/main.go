package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/session"
)

// filterSpec holds the per-session filter flags.
type filterSpec struct {
	idPrefix string
	state    string
	project  string
	adapter  string
}

func main() {
	var (
		watch  bool
		format string
		f      filterSpec
	)
	flag.BoolVar(&watch, "w", false, "watch mode — refresh every second")
	flag.BoolVar(&watch, "watch", false, "watch mode — refresh every second")
	flag.StringVar(&format, "format", "text", `output format: "text" or "json"`)
	flag.StringVar(&f.idPrefix, "id", "", "filter: session ID prefix")
	flag.StringVar(&f.state, "state", "", "filter: session state (working|waiting|ready)")
	flag.StringVar(&f.project, "project", "", "filter: project name substring")
	flag.StringVar(&f.adapter, "adapter", "", "filter: agent adapter (e.g. claude-code, codex)")
	flag.Parse()

	if format != "text" && format != "json" {
		fmt.Fprintf(os.Stderr, "error: unknown format %q (want text or json)\n", format)
		os.Exit(1)
	}
	if f.state != "" && f.state != session.StateWorking && f.state != session.StateWaiting && f.state != session.StateReady {
		fmt.Fprintf(os.Stderr, "error: unknown state %q (want working, waiting or ready)\n", f.state)
		os.Exit(1)
	}

	repo, err := filesystem.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	useColor := format == "text" && stdoutIsTTY() && os.Getenv("NO_COLOR") == ""

	for {
		sessions, err := repo.ListAll()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		sessions = filterSessions(sessions, f)
		sort.SliceStable(sessions, func(i, j int) bool {
			return sessions[i].UpdatedAt > sessions[j].UpdatedAt
		})
		groups := session.BuildDashboard(sessions, nil)

		if watch && format != "json" {
			fmt.Print("\033[H\033[2J") // clear screen — never into a JSON stream
		}

		if format == "json" {
			if groups == nil {
				groups = []*session.AgentGroup{}
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(map[string]any{"groups": groups}); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
		} else if len(groups) == 0 {
			fmt.Println("no sessions")
		} else {
			renderGroups(os.Stdout, groups, useColor)
		}

		if !watch {
			return
		}
		time.Sleep(1 * time.Second)
	}
}

// filterSessions returns the sessions matching every set filter. A child
// whose parent is filtered out is promoted to a top-level row by
// BuildDashboard (it only nests children whose parent is in the input set).
func filterSessions(sessions []*session.SessionState, f filterSpec) []*session.SessionState {
	if f.idPrefix == "" && f.state == "" && f.project == "" && f.adapter == "" {
		return sessions
	}
	out := make([]*session.SessionState, 0, len(sessions))
	for _, s := range sessions {
		if f.idPrefix != "" && !strings.HasPrefix(strings.ToLower(s.SessionID), strings.ToLower(f.idPrefix)) {
			continue
		}
		if f.state != "" && s.State != f.state {
			continue
		}
		if f.project != "" && !strings.Contains(strings.ToLower(projectName(s)), strings.ToLower(f.project)) {
			continue
		}
		if f.adapter != "" && adapterName(s) != f.adapter {
			continue
		}
		out = append(out, s)
	}
	return out
}

// stdoutIsTTY reports whether stdout is a terminal (vs a pipe or file).
func stdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
