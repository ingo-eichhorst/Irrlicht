// Package recorder implements lifecycle event recording to JSONL files
// for offline replay of full session lifecycles.
package recorder

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"irrlicht/core/domain/lifecycle"
)

const flushInterval = 5 * time.Second

// JSONLRecorder writes lifecycle events as one JSON object per line to a
// single file. It is safe for concurrent use. One file per daemon run
// captures all sessions (parent + children naturally interleaved).
type JSONLRecorder struct {
	mu     sync.Mutex
	f      *os.File
	w      *bufio.Writer
	enc    *json.Encoder
	closed bool
	done   chan struct{}
}

// NewJSONLRecorder creates a new recorder that writes to a timestamped file
// in dir. The directory is created if it does not exist.
func NewJSONLRecorder(dir string) (*JSONLRecorder, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create recordings dir: %w", err)
	}

	name := fmt.Sprintf("%s.jsonl", time.Now().Format("2006-01-02T150405"))
	path := filepath.Join(dir, name)

	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create recording file: %w", err)
	}

	w := bufio.NewWriterSize(f, 64*1024)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	r := &JSONLRecorder{f: f, w: w, enc: enc, done: make(chan struct{})}
	go r.periodicFlush()
	return r, nil
}

// periodicFlush flushes buffered data to disk every flushInterval so that
// an ungraceful shutdown (SIGKILL, crash) loses at most a few seconds of
// events rather than up to 64KB of buffered data.
func (r *JSONLRecorder) periodicFlush() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-ticker.C:
			r.mu.Lock()
			_ = r.w.Flush()
			r.mu.Unlock()
		}
	}
}

// Path returns the absolute path of the recording file.
func (r *JSONLRecorder) Path() string {
	return r.f.Name()
}

// Record writes a single lifecycle event as a JSON line. It is safe for
// concurrent use.
func (r *JSONLRecorder) Record(ev lifecycle.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Best-effort: drop events on write error (don't crash the daemon).
	_ = r.enc.Encode(ev)
}

// Close stops periodic flushing, flushes remaining data, and closes the file.
func (r *JSONLRecorder) Close() error {
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
