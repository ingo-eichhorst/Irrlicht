//go:build windows

// Command irrlicht is the Windows tray companion for the irrlicht daemon.
// Mirrors the macOS app's behavior: spawn irrlichd from the install
// directory, render a state-driven tray icon, and open the dashboard on
// click. Built with raw Win32 syscalls (no third-party tray library) to
// keep dependencies minimal and avoid abandoned-library risk.
package main

import (
	"context"
	"log"
	"os"
	"runtime"

	"irrlicht/platforms/windows/internal/daemon"
	"irrlicht/platforms/windows/internal/state"
	"irrlicht/platforms/windows/internal/tray"
	"irrlicht/platforms/windows/internal/win32"
)

const singleInstanceMutex = `Global\io.irrlicht.app`

func main() {
	// Single-instance guard — a duplicate launch from a Run-key autostart
	// or a manual click should be a no-op rather than two competing trays.
	first, err := win32.AcquireSingleInstanceMutex(singleInstanceMutex)
	if err != nil {
		log.Printf("warning: single-instance mutex: %v", err)
	} else if !first {
		log.Println("another irrlicht instance is already running")
		os.Exit(0)
	}

	dm := daemon.New()
	dm.Start()
	defer dm.Stop()

	sub := state.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sub.Run(ctx)

	updates := make(chan state.Snapshot, 8)
	sub.Subscribe(func(s state.Snapshot) {
		select {
		case updates <- s:
		default:
			// Drop updates when the consumer is slow — coalescing is fine.
		}
	})

	openDashboard := func() {
		// v1: open the user's default browser. WebView2 popover is a
		// follow-up: see plan-implementation-radiant-hanrahan.md "Phase 2".
		if err := win32.ShellExecute("open", daemon.DaemonURL("/")); err != nil {
			log.Printf("open dashboard: %v", err)
		}
	}
	onQuit := func() {
		cancel()
		dm.Stop()
		close(updates)
	}

	// Win32 GUI must run on a single OS thread — lock to be safe even
	// though main goroutine is already on the initial thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	t := tray.New(openDashboard, onQuit)
	if err := t.Run(state.TrayOffline, updates); err != nil {
		log.Fatalf("tray: %v", err)
	}
}
