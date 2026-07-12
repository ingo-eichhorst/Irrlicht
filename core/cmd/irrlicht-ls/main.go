package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
		watch     bool
		format    string
		showYield bool
		f         filterSpec
	)
	flag.BoolVar(&watch, "w", false, "watch mode — refresh every second")
	flag.BoolVar(&watch, "watch", false, "watch mode — refresh every second")
	flag.StringVar(&format, "format", "text", `output format: "text" or "json"`)
	flag.BoolVar(&showYield, "yield", false, "show yield state and revert SHA (7-char) columns")
	flag.StringVar(&f.idPrefix, "id", "", "filter: session ID prefix")
	flag.StringVar(&f.state, "state", "", "filter: session state (working|waiting|ready)")
	flag.StringVar(&f.project, "project", "", "filter: project name substring")
	flag.StringVar(&f.adapter, "adapter", "", "filter: agent adapter (e.g. claude-code, codex)")
	flag.Parse()

	if err := validateFlags(format, f.state); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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

		if err := emitOutput(os.Stdout, groups, format, useColor, showYield); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
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

// validateFlags checks the format and state flag values, returning a
// non-nil error with a user-facing message when either is invalid.
func validateFlags(format, state string) error {
	if format != "text" && format != "json" {
		return fmt.Errorf("unknown format %q (want text or json)", format)
	}
	if state != "" && state != session.StateWorking && state != session.StateWaiting && state != session.StateReady {
		return fmt.Errorf("unknown state %q (want working, waiting or ready)", state)
	}
	return nil
}

// emitOutput writes groups to w in the requested format: JSON (one
// indented document) or the human-readable text dashboard. It returns any
// JSON-encoding error so the caller can report it and exit, matching the
// pre-extraction inline behavior.
func emitOutput(w io.Writer, groups []*session.AgentGroup, format string, useColor, showYield bool) error {
	if format == "json" {
		if groups == nil {
			groups = []*session.AgentGroup{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"groups": groups})
	}
	if len(groups) == 0 {
		fmt.Fprintln(w, "no sessions")
		return nil
	}
	renderGroups(w, groups, useColor, showYield)
	return nil
}
