// Package workerlist implements an OrchestratorWatcher over one JSON status
// file that a worker pool writes about its own slots (issue #2148). Irrlicht
// only reads that file; it never runs anything the user names.
//
// permission.go declares the consent surface and the file location. Like Gas
// Town (gastown/permission.go), the watcher is built and started only by the
// permission's Apply effect, which the daemon wires.
package workerlist

import (
	"os"
	"path/filepath"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// Name is the orchestrator identifier: the group `type` on the wire, the
// adapter name in the permission store, and the wizard row.
const Name = "workerlist"

// PermissionKeyState gates every read of the status file.
const PermissionKeyState = "state"

// EnvFile names the environment variable that overrides the status file path.
const EnvFile = "IRRLICHT_WORKERLIST_FILE"

// DefaultFileName is the status file's name inside the daemon's data
// directory, used when EnvFile is unset. An app-spawned daemon inherits only
// the app's environment, so the default path is the one that needs no setup.
const DefaultFileName = "workerlist.json"

// ResolvePath returns the status file the watcher reads: $IRRLICHT_WORKERLIST_FILE
// when set, else DefaultFileName inside dataDir (the daemon's IRRLICHT_HOME-aware
// data directory).
func ResolvePath(dataDir string) string {
	if p := os.Getenv(EnvFile); p != "" {
		return p
	}
	return filepath.Join(dataDir, DefaultFileName)
}

// FileDetected reports whether the status file at ResolvePath(dataDir)
// exists. It stats the path and never opens the file, so it is safe before
// consent; the permission service uses it as the wizard's detection signal.
func FileDetected(dataDir string) bool {
	return fileExists(ResolvePath(dataDir))
}

// fileExists stats path without opening it.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// PermissionDeclaration returns the consent declaration for the worker list.
// start/stop are wired by the daemon: they build the watcher and start/stop
// it, so nothing reads the status file before a grant or after a revoke.
func PermissionDeclaration(start, stop func() error) agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: Name, DisplayName: "Worker list"},
		Permissions: []agent.Permission{{
			Key:             PermissionKeyState,
			Kind:            permission.KindObserve,
			Title:           "Read a worker pool's status file",
			FeatureUnlocked: "Orchestrator view: a worker pool's slots, their work items, stale heartbeats and failed runs",
			Touches:         "Reads $" + EnvFile + " (default workerlist.json in Irrlicht's data directory)",
			Detail: "Watches one JSON status file that a worker pool writes about its " +
				"slots, at $" + EnvFile + " or, when that is unset, workerlist.json " +
				"in Irrlicht's data directory (~/.local/share/irrlicht, or " +
				"$IRRLICHT_HOME). Irrlicht only reads the file and runs nothing. " +
				"Toggling off stops reading it immediately.",
			Apply:  start,
			Remove: stop,
		}},
	}
}
