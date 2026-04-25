//go:build windows

package processlifecycle

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stillActiveExitCode is the value GetExitCodeProcess returns for a running
// process. Defined locally because golang.org/x/sys/windows does not export
// the STILL_ACTIVE constant.
const stillActiveExitCode = 259

// findProcesses returns PIDs of running processes whose executable name
// matches name (case-insensitive, with the optional .exe suffix stripped).
// Uses the Toolhelp32 snapshot rather than shelling out to tasklist.exe so
// the polling loop doesn't fork a process every second.
func findProcesses(name string) ([]int, error) {
	target := strings.TrimSuffix(strings.ToLower(name), ".exe")
	if target == "" {
		return nil, nil
	}

	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		// ERROR_NO_MORE_FILES on an empty snapshot — return nil, not an error.
		if err == windows.ERROR_NO_MORE_FILES {
			return nil, nil
		}
		return nil, err
	}

	var pids []int
	for {
		exe := windows.UTF16ToString(entry.ExeFile[:])
		if exe != "" {
			candidate := strings.TrimSuffix(strings.ToLower(exe), ".exe")
			if candidate == target {
				pids = append(pids, int(entry.ProcessID))
			}
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				break
			}
			return pids, err
		}
	}
	return pids, nil
}

// processCWD is a stub on Windows. Reading another process's working
// directory requires NtQueryInformationProcess + a PEB read, which is
// non-trivial and may need elevation against processes from other users.
// Callers (scanner.poll, DiscoverPIDByCWD) treat empty CWD as "skip" so
// pre-session detection is degraded but session detection via the file
// watcher still works once the agent writes its first transcript line.
func processCWD(pid int) (string, error) {
	return "", nil
}

// processTTY is a stub on Windows. Windows has no controlling-terminal
// concept analogous to POSIX TTYs.
func processTTY(pid int) string {
	return ""
}

// PidAlive reports whether the process with the given pid is alive.
// Implemented via OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION) +
// GetExitCodeProcess; STILL_ACTIVE (259) means the process is running.
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActiveExitCode
}

// readProcessEnv is not implemented on Windows for v1. Reading a remote
// process's environment requires NtQueryInformationProcess + a PEB read;
// not currently needed for Windows session detection.
func readProcessEnv(pid int) (map[string]string, error) {
	return nil, nil
}

// resolveTermProgramFromAncestry is a darwin-only fallback; Windows does
// not need it because no Windows agent ships hardened-runtime binaries
// that hide their environment.
func resolveTermProgramFromAncestry(pid int) string { return "" }
