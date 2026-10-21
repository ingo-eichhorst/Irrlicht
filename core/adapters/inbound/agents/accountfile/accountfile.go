// Package accountfile holds the file mechanics the per-adapter account
// readers share (#2095): a stat+read seam, an mtime+size cache that keeps the
// last good observation across a transient failure, and the bounded scan for
// a transcript's first timestamp. It performs NO consent check — each reader
// checks its own grant before calling into it, so the gate stays visible (and
// mutation-testable) in the adapter that owns the permission.
package accountfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"sync"
	"time"

	"irrlicht/core/ports/outbound"
)

// HeadBytes bounds the read TranscriptStart makes of a transcript.
const HeadBytes = 64 << 10

// FS is the file access seam; tests substitute it to count reads.
type FS struct {
	Stat func(path string) (modUnix, size int64, err error)
	Read func(path string) ([]byte, error)
	// ReadHead returns up to n leading bytes of path.
	ReadHead func(path string, n int) ([]byte, error)
}

// OSFS is the production file access.
func OSFS() FS {
	return FS{
		Stat: func(p string) (int64, int64, error) {
			fi, err := os.Stat(p)
			if err != nil {
				return 0, 0, err
			}
			return fi.ModTime().Unix(), fi.Size(), nil
		},
		Read: os.ReadFile,
		ReadHead: func(p string, n int) ([]byte, error) {
			f, err := os.Open(p)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			buf := make([]byte, n)
			m, err := io.ReadFull(f, buf)
			if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
				err = nil
			}
			if err != nil {
				return nil, err
			}
			return buf[:m], nil
		},
	}
}

type entry struct {
	mod, size int64
	obs       outbound.HomeAccountObservation
}

// Cache memoizes one observation per identity-file path, validated by
// mtime+size. Every call stats the file (cheap); an unchanged file skips the
// read and parse.
type Cache struct {
	FS FS

	mu sync.Mutex
	m  map[string]entry
}

// NewCache builds a cache over fs.
func NewCache(fs FS) *Cache { return &Cache{FS: fs, m: map[string]entry{}} }

// Lookup returns the observation for path. parse turns file bytes into an
// observation (Home and ModTime are filled in here) or reports ok=false for
// bytes it cannot read. A not-exist stat error drops the entry and reports ok
// with an empty Ref and ModTime 0; any other stat, read or parse failure
// returns the last good observation for path when there is one, else ok=false.
func (c *Cache) Lookup(path string, parse func([]byte) (outbound.HomeAccountObservation, bool)) (outbound.HomeAccountObservation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	last, haveLast := c.m[path]
	mod, size, err := c.FS.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		// The file is gone (e.g. a logout that deletes it): that is an
		// answer — no account — not a transient failure to paper over with
		// the last good observation.
		delete(c.m, path)
		return outbound.HomeAccountObservation{Home: path}, true
	}
	if err != nil {
		return last.obs, haveLast
	}
	if haveLast && last.mod == mod && last.size == size {
		return last.obs, true
	}
	data, err := c.FS.Read(path)
	if err != nil {
		return last.obs, haveLast
	}
	obs, ok := parse(data)
	if !ok {
		return last.obs, haveLast
	}
	obs.Home, obs.ModTime = path, mod
	c.m[path] = entry{mod: mod, size: size, obs: obs}
	return obs, true
}

// FirstTimestamp returns the Unix second of the first line in the first
// HeadBytes of the transcript at path carrying a top-level "timestamp" that
// parses as RFC 3339; 0 when there is none or the file is unreadable.
func FirstTimestamp(fs FS, path string) int64 {
	head, err := fs.ReadHead(path, HeadBytes)
	if err != nil {
		return 0
	}
	for _, line := range bytes.Split(head, []byte{'\n'}) {
		var ev struct {
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Timestamp == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, ev.Timestamp); err == nil {
			return t.Unix()
		}
	}
	return 0
}
