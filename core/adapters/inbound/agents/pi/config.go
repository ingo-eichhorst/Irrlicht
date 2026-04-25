package pi

import (
	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/pkg/tailer"
)

// Config returns the registration record the daemon uses to wire this adapter.
func Config() agents.Config {
	return agents.Config{
		Name:        AdapterName,
		ProcessName: ProcessName,
		RootDir:     rootDir,
		NewParser:   func() tailer.TranscriptParser { return &Parser{} },
		DiscoverPID: DiscoverPID,
	}
}
