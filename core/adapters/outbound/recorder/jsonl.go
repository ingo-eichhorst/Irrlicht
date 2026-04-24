// Package recorder implements lifecycle event recording to JSONL files
// for offline replay of full session lifecycles.
package recorder

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"irrlicht/core/domain/lifecycle"
)

const flushInterval = 5 * time.Second

// jsonlRecorder writes lifecycle events as one JSON object per line to a
// single file. It is safe for concurrent use. One file per daemon run
// captures all sessions (parent + children naturally interleaved).
type jsonlRecorder struct {
	mu     sync.Mutex
	f      *os.File
	w      *bufio.Writer
	enc    *json.Encoder
	closed bool
	done   chan struct{}
}

// NewJSONLRecorder creates a new recorder that writes to a timestamped file
// in dir. The directory is created if it does not exist. The filename
// includes a short random suffix so that sub-second daemon restarts don't
// collide and overwrite a prior recording.
func NewJSONLRecorder(dir string) (*jsonlRecorder, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create recordings dir: %w", err)
	}

	var suffixBytes [3]byte
	if _, err := rand.Read(suffixBytes[:]); err != nil {
		return nil, fmt.Errorf("generate recording suffix: %w", err)
	}
	name := fmt.Sprintf("%s-%s.jsonl",
		time.Now().Format("2006-01-02T150405"),
		hex.EncodeToString(suffixBytes[:]))
	path := filepath.Join(dir, name)

	// O_EXCL refuses to open if the file already exists — catches the
	// astronomically-unlikely case where the random suffix collides.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return nil, fmt.Errorf("create recording file: %w", err)
	}

	w := bufio.NewWriterSize(f, 64*1024)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	r := &jsonlRecorder{f: f, w: w, enc: enc, done: make(chan struct{})}
	go r.periodicFlush()
	return r, nil
}

// periodicFlush flushes buffered data to disk every flushInterval so that
// an ungraceful shutdown (SIGKILL, crash) loses at most a few seconds of
// events rather than up to 64KB of buffered data.
func (r *jsonlRecorder) periodicFlush() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-ticker.C:
			r.mu.Lock()
			// Guard against the ticker-vs-done race: Go's select picks
			// pseudo-randomly when both cases are ready, so a Close() that
			// already fired `close(r.done)` can still lose the draw and we
			// end up here after the underlying file is closed. Skip the
			// flush in that case rather than writing to a closed fd.
			if !r.closed {
				_ = r.w.Flush()
			}
			r.mu.Unlock()
		}
	}
}

// Path returns the absolute path of the recording file.
func (r *jsonlRecorder) Path() string {
	return r.f.Name()
}

// Record writes a single lifecycle event as a JSON line. It is safe for
// concurrent use.
func (r *jsonlRecorder) Record(ev lifecycle.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Best-effort: drop events on write error (don't crash the daemon).
	_ = r.enc.Encode(ev)
}

// Close stops periodic flushing, flushes remaining data, and closes the file.
func (r *jsonlRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.closed {
		r.closed = true
		close(r.done)
	}

	if err := r.w.Flush(); err != nil {
		r.f.Close()
		return err
	}
	return r.f.Close()
}
