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
	argv := argvOf(pid)
	switch provider {
	case "codex":
		if len(argv) < 3 || filepath.Base(argv[0]) != "codex" || argv[1] != "app-server" || argv[2] != "--stdio" {
			return false
		}
		wrapperPID, err := parentOf(ctx, pid)
		if err != nil || wrapperPID <= 1 || wrapperPID == pid || !codexWrapperArgv(argvOf(wrapperPID)) {
			return false
		}
		pid = wrapperPID
	case "claude-code":
		if len(argv) == 0 || filepath.Base(argv[0]) != "claude" ||
			!hasPair(argv, "--input-format", "stream-json") ||
			!hasPair(argv, "--output-format", "stream-json") ||
			!hasArg(argv, "--no-session-persistence") {
			return false
		}
	default:
		return false
	}
	parentPID, err := parentOf(ctx, pid)
	if err != nil || parentPID <= 1 || parentPID == pid || !dshLauncherArgv(argvOf(parentPID)) {
		return false
	}
	return visibleParent(parentPID)
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
