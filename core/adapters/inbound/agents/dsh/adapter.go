// Package dsh provides an inbound adapter for DeepSeek Harness session logs.
// DeepSeek Harness 0.1.5-rc.2 writes concatenated, checksummed zstd frames to
// $DSH_HOME/sessions/<cwd-key>/session-<uuid>/session.v3.jsonl.zstd. The shared
// tailer decodes those frames before this package parses the JSONL records.
package dsh

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/klauspost/compress/zstd"

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
	maxNativeHeaderBytes   = 2 * 1024 * 1024
)

var (
	processCmdRegex   = regexp.MustCompile(processCmdPattern)
	sessionIDPattern  = regexp.MustCompile(`^session-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	childIDPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
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
	highest, ok := highestTranscriptGeneration(filepath.Dir(path))
	if ok && compareGenerations(generation, highest) == 0 {
		return sessionIDFromDirectory(path)
	}
	// fsnotify reports removal after the path is gone. Accept the removed path
	// only when no higher generation remains in the session directory.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return ""
	}
	if ok && compareGenerations(generation, highest) < 0 {
		return ""
	}
	return sessionIDFromDirectory(path)
}

// sessionIDFromDirectory accepts DSH's ordinary session-<uuid> directories
// and its native in-process child directories. A bare UUID becomes a session
// only when its durable header proves it is an origin:subagent child with the
// same id. This avoids minting sessions for unrelated UUID-named directories.
func sessionIDFromDirectory(path string) string {
	id := filepath.Base(filepath.Dir(path))
	if sessionIDPattern.MatchString(id) {
		return id
	}
	if !childIDPattern.MatchString(id) {
		return ""
	}
	childID, _ := nativeSubagentHeader(path)
	if childID == id {
		return id
	}
	// fsnotify delivers removal after the header is gone. A bare UUID is not
	// accepted while live unless its header proves native-child ownership, but
	// a missing transcript must retain its prior identity so the watcher emits
	// the matching removal event.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return id
	}
	return ""
}

// parentSessionIDFromPath returns a native in-process child's durable parent.
// It intentionally requires origin:subagent: DSH forks also carry
// parentSession, but are session lineage rather than active subagent work.
func parentSessionIDFromPath(path string) string {
	if !childIDPattern.MatchString(filepath.Base(filepath.Dir(path))) {
		return ""
	}
	childID, parentID := nativeSubagentHeader(path)
	if childID != filepath.Base(filepath.Dir(path)) || !childIDPattern.MatchString(childID) {
		return ""
	}
	return parentID
}

type nativeChildHeader struct {
	Type          string `json:"type"`
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Origin        string `json:"origin"`
	ParentSession string `json:"parentSession"`
}

// nativeSubagentHeader reads the first compressed record only. The daemon
// intends this callback for the filesystem watcher it constructs after observe
// permission is granted. Invalid, partial, or non-subagent headers fail closed
// and mint no child.
func nativeSubagentHeader(path string) (childID, parentID string) {
	if strings.Contains(path, "..") || !transcriptPattern.MatchString(filepath.Base(path)) {
		return "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	decoder, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return "", ""
	}
	defer decoder.Close()

	var header nativeChildHeader
	if err := json.NewDecoder(io.LimitReader(decoder, maxNativeHeaderBytes)).Decode(&header); err != nil || header.Type != recordSession || header.Version != supportedFormatVersion || header.Origin != "subagent" || !childIDPattern.MatchString(header.ID) || !validDSHSessionID(header.ParentSession) {
		return "", ""
	}
	return header.ID, header.ParentSession
}

func validDSHSessionID(id string) bool {
	return sessionIDPattern.MatchString(id) || childIDPattern.MatchString(id)
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
