package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/session"
)

func main() {
	watch := len(os.Args) > 1 && (os.Args[1] == "-w" || os.Args[1] == "--watch")

	repo, err := filesystem.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	for {
		sessions, err := repo.ListAll()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		if watch {
			fmt.Print("\033[H\033[2J") // clear screen
		}

		if len(sessions) == 0 {
			fmt.Println("no sessions")
		} else {
			printSessions(sessions)
		}

		if !watch {
			return
		}
		time.Sleep(1 * time.Second)
	}
}

func printSessions(sessions []*session.SessionState) {
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})

	for _, s := range sessions {
		age := time.Since(time.Unix(s.UpdatedAt, 0)).Truncate(time.Second)
		project := s.ProjectName
		if project == "" {
			project = filepath.Base(s.CWD)
		}

		model := ""
		ctx := ""
		if s.Metrics != nil && s.Metrics.ModelName != "" {
			model = " " + s.Metrics.ModelName
			if s.Metrics.ContextWindow > 0 {
				ctx = fmt.Sprintf(" %dk", s.Metrics.ContextWindow/1000)
			}
		}

		fmt.Printf("%-8s %-20s %s%s%s  (%s ago)\n",
			s.StringState(), project, s.SessionID[:8], model, ctx, age)
	}
}
