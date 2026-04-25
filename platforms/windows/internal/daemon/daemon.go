//go:build windows

// Package daemon manages the lifecycle of the embedded irrlichd binary
// shipped alongside the tray executable. Mirrors platforms/macos/Irrlicht/
// Managers/DaemonManager.swift: probe → kill stale → spawn → watch →
// restart with exponential backoff.
package daemon

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DaemonAddr is the loopback address the daemon binds to. Hard-coded
// because some Windows hosts files map "localhost" to ::1 only, and the
// daemon binds 127.0.0.1.
const DaemonAddr = "127.0.0.1:7837"

// DaemonURL returns the http URL for the local daemon at path.
func DaemonURL(path string) string { return "http://" + DaemonAddr + path }

// daemonExeName is the file name we expect alongside the tray binary.
const daemonExeName = "irrlichd.exe"

// maxRestartDelay caps the exponential backoff between restart attempts.
const maxRestartDelay = 30 * time.Second

// Manager owns the daemon subprocess. Start launches it (after probing for
// an external instance), Stop terminates gracefully.
type Manager struct {
	mu           sync.Mutex
	proc         *os.Process
	restartCount int
	stopped      bool
	cancelWatch  context.CancelFunc
}

// New returns a fresh Manager.
func New() *Manager { return &Manager{} }

// Start probes for an existing reachable daemon; if none, kills any stale
// irrlichd processes and spawns a fresh copy. Subsequent crashes trigger
// exponential-backoff restarts until Stop is called.
func (m *Manager) Start() {
	if IsDaemonReachable() {
		log.Println("daemon: external instance reachable on", DaemonAddr, "— skipping spawn")
		return
	}
	killStaleDaemons()
	m.spawn()
}

// Stop terminates the spawned daemon (if any) and prevents further restarts.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopped = true
	if m.cancelWatch != nil {
		m.cancelWatch()
	}
	proc := m.proc
	m.proc = nil
	m.mu.Unlock()
	if proc != nil {
		_ = proc.Kill()
	}
}

// IsDaemonReachable performs a fast HTTP GET against /state.
func IsDaemonReachable() bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(DaemonURL("/state"))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// daemonPath resolves the irrlichd.exe expected to live next to the tray
// executable (matching the macOS bundle layout — DaemonManager.swift:82).
func daemonPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), daemonExeName), nil
}

// killStaleDaemons removes irrlichd.exe processes from prior tray runs so
// the new daemon can bind 127.0.0.1:7837 cleanly. Mirrors `pkill -x` on
// macOS but uses the Toolhelp32 snapshot to avoid forking taskkill.
func killStaleDaemons() {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return
	}
	for {
		if exe := windows.UTF16ToString(entry.ExeFile[:]); strings.EqualFold(exe, daemonExeName) {
			h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, entry.ProcessID)
			if err == nil {
				windows.TerminateProcess(h, 0)
				windows.CloseHandle(h)
				log.Printf("daemon: terminated stale irrlichd pid=%d", entry.ProcessID)
			}
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	// Give the OS a beat to release the listening port.
	time.Sleep(500 * time.Millisecond)
}

// spawn starts irrlichd.exe with no console window, and launches a watcher
// goroutine that restarts the daemon if it exits unexpectedly.
func (m *Manager) spawn() {
	path, err := daemonPath()
	if err != nil {
		log.Printf("daemon: resolve path: %v", err)
		return
	}
	if _, err := os.Stat(path); err != nil {
		log.Printf("daemon: %s not found at %s — running tray standalone", daemonExeName, path)
		return
	}

	cmd := exec.Command(path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		log.Printf("daemon: spawn failed: %v", err)
		m.scheduleRestart()
		return
	}

	m.mu.Lock()
	m.proc = cmd.Process
	m.restartCount = 0
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelWatch = cancel
	m.mu.Unlock()

	log.Printf("daemon: spawned irrlichd pid=%d", cmd.Process.Pid)

	go m.watch(ctx, cmd)
}

// watch blocks until the spawned process exits, then schedules a restart
// unless Stop was called.
func (m *Manager) watch(ctx context.Context, cmd *exec.Cmd) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		return
	case err := <-done:
		m.mu.Lock()
		stopped := m.stopped
		m.mu.Unlock()
		if stopped {
			return
		}
		log.Printf("daemon: exited (%v) — scheduling restart", err)
		m.scheduleRestart()
	}
}

// scheduleRestart waits on an exponential-backoff timer then re-spawns.
// Capped at maxRestartDelay (30s) to avoid hammering on persistent failures.
func (m *Manager) scheduleRestart() {
	m.mu.Lock()
	m.restartCount++
	n := m.restartCount
	m.mu.Unlock()

	delay := time.Duration(1<<uint(n-1)) * time.Second
	if delay > maxRestartDelay {
		delay = maxRestartDelay
	}
	log.Printf("daemon: restart #%d in %v", n, delay)
	time.AfterFunc(delay, func() {
		m.mu.Lock()
		stopped := m.stopped
		m.mu.Unlock()
		if !stopped {
			m.spawn()
		}
	})
}
