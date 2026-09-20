package dsh

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// OwnsProviderChild confirms the narrow one-shot process shapes used by the
// pinned DSH Codex and Claude providers. Unknown argv or ancestry keeps the
// native row visible. The parent check must require DSH consent and a visible
// transcript-backed session for the exact DSH PID.
func OwnsProviderChild(provider string, pid int, visibleParent func(int) bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return ownsProviderChildVia(ctx, provider, pid, processlifecycle.ReadArgv,
		processlifecycle.ParentPIDOf, visibleParent)
}

func ownsProviderChildVia(ctx context.Context, provider string, pid int,
	argvOf func(int) []string, parentOf func(context.Context, int) (int, error),
	visibleParent func(int) bool) bool {
	if pid <= 1 || visibleParent == nil {
		return false
	}
	var launcherPID int
	switch provider {
	case "codex":
		launcherPID = codexLauncherPID(ctx, pid, argvOf, parentOf)
	case "claude-code":
		launcherPID = claudeLauncherPID(ctx, pid, argvOf, parentOf)
	default:
		return false
	}
	return launcherPID > 1 && dshLauncherArgv(argvOf(launcherPID)) && visibleParent(launcherPID)
}

func codexLauncherPID(ctx context.Context, pid int, argvOf func(int) []string,
	parentOf func(context.Context, int) (int, error)) int {
	argv := argvOf(pid)
	if len(argv) < 3 || filepath.Base(argv[0]) != "codex" || argv[1] != "app-server" || argv[2] != "--stdio" {
		return 0
	}
	wrapperPID, err := parentOf(ctx, pid)
	if err != nil || wrapperPID <= 1 || wrapperPID == pid || !codexWrapperArgv(argvOf(wrapperPID)) {
		return 0
	}
	return parentPID(ctx, wrapperPID, parentOf)
}

func claudeLauncherPID(ctx context.Context, pid int, argvOf func(int) []string,
	parentOf func(context.Context, int) (int, error)) int {
	argv := argvOf(pid)
	if len(argv) == 0 || filepath.Base(argv[0]) != "claude" ||
		!hasPair(argv, "--input-format", "stream-json") ||
		!hasPair(argv, "--output-format", "stream-json") ||
		!hasArg(argv, "--no-session-persistence") {
		return 0
	}
	return parentPID(ctx, pid, parentOf)
}

func parentPID(ctx context.Context, pid int, parentOf func(context.Context, int) (int, error)) int {
	parent, err := parentOf(ctx, pid)
	if err != nil || parent <= 1 || parent == pid {
		return 0
	}
	return parent
}

func codexWrapperArgv(argv []string) bool {
	return len(argv) >= 4 && filepath.Base(argv[0]) == "node" &&
		strings.HasSuffix(filepath.ToSlash(argv[1]), "/@openai/codex/bin/codex.js") &&
		argv[2] == "app-server" && argv[3] == "--stdio"
}

func dshLauncherArgv(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	if filepath.Base(argv[0]) == "dsh" {
		return true
	}
	return len(argv) > 1 && filepath.Base(argv[0]) == "node" && filepath.Base(argv[1]) == "dsh"
}

func hasArg(argv []string, want string) bool {
	for _, arg := range argv[1:] {
		if arg == want {
			return true
		}
	}
	return false
}

func hasPair(argv []string, key, value string) bool {
	for i := 1; i+1 < len(argv); i++ {
		if argv[i] == key && argv[i+1] == value {
			return true
		}
	}
	return false
}
