// Package dsh provides an inbound adapter for DeepSeek Harness session logs.
// DeepSeek Harness 0.1.5-rc.2 writes concatenated, checksummed zstd frames to
// $DSH_HOME/sessions/<cwd-key>/session-<uuid>/session.v3.jsonl.zstd. The shared
// tailer decodes those frames before this package parses the JSONL records.
package dsh

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
)

const (
	AdapterName            = "dsh"
	defaultRootDir         = ".dsh/sessions"
	dshHomeEnvVar          = "DSH_HOME"
	sessionLockFilename    = "session.lock"
	processCmdPattern      = `(^|/)dsh(\s|$)`
	supportedFormatVersion = 3
	transcriptNamePrefix   = "session.v"
	transcriptNameSuffix   = ".jsonl.zstd"
)

var (
	processCmdRegex   = regexp.MustCompile(processCmdPattern)
	sessionIDPattern  = regexp.MustCompile(`^session-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	transcriptPattern = regexp.MustCompile(`^session\.v([0-9]+)\.jsonl\.zstd$`)
)

func sessionsDir() string {
	return agentpaths.FromEnv("dsh", dshHomeEnvVar, defaultRootDir, "sessions")
}

// sessionIDFromPath accepts only the highest numeric transcript generation in
// a session directory. DeepSeek Harness can leave older generations beside a
// migrated log. The running 0.1.5-rc.2 install has only v3; the side-by-side
// selection rule comes from its persistence implementation, pinned in #1980.
func sessionIDFromPath(path string) string {
	name := filepath.Base(path)
	generation, ok := transcriptGeneration(name)
	if !ok {
		return ""
	}
	id := filepath.Base(filepath.Dir(path))
	if !sessionIDPattern.MatchString(id) {
		return ""
	}

	highest, ok := highestTranscriptGeneration(filepath.Dir(path))
	if !ok || compareGenerations(generation, highest) != 0 {
		return ""
	}
	return id
}

func transcriptGeneration(name string) (string, bool) {
	match := transcriptPattern.FindStringSubmatch(name)
	if match == nil {
		return "", false
	}
	generation := strings.TrimLeft(match[1], "0")
	if generation == "" {
		generation = "0"
	}
	return generation, true
}

func highestTranscriptGeneration(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var highest string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		generation, ok := transcriptGeneration(entry.Name())
		if !ok {
			continue
		}
		if highest == "" || compareGenerations(generation, highest) > 0 {
			highest = generation
		}
	}
	return highest, highest != ""
}

func compareGenerations(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
