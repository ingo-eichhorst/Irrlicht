// Package frames captures periodic snapshots of the agent's tmux pane and
// indexes them so the Phase 7 viewer can scrub through a recording at
// configurable speeds.
//
// Phase 1 ships TEXT frames (`frames/<ts>.txt`) — the raw output of
// `tmux capture-pane`. Phase 7's viewer renders text to canvas at the
// chosen speed (1× / 2× / 5× / 10× / 20× / 25× / 100×). Text frames are
// smaller, faster to write, selectable in the viewer, and trivially
// diffable for the side-by-side mode.
//
// The issue spec mentions WebP/PNG as the preferred format; the format
// field in frames.jsonl exists so a future renderer can write `<ts>.webp`
// alongside (or in place of) the `.txt` files without changing the index
// schema.
package frames

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FrameIndex is one row in frames.jsonl.
type FrameIndex struct {
	Ts     time.Time `json:"ts"`
	Path   string    `json:"path"`   // path relative to the recording dir
	Format string    `json:"format"` // "text" today; "png" / "webp" reserved
}

// Renderer captures one frame from a source (tmux pane) and writes it to disk.
// Phase 1 ships TextRenderer; future PNGRenderer / WebPRenderer can
// implement the same interface and be swapped in without touching the
// recorder's call sites.
type Renderer interface {
	// Capture renders one frame to outDir at ts. Returns the index entry
	// (with path relative to outDir) or an error if capture fails.
	Capture(ctx context.Context, outDir string, ts time.Time) (FrameIndex, error)
}

// TextRenderer captures the tmux pane as plain text. The "image" format
// for Phase 1.
type TextRenderer struct {
	// Target identifies the tmux pane, e.g. "recorder:0.0".
	Target string
}

// Capture implements Renderer.
func (r *TextRenderer) Capture(ctx context.Context, outDir string, ts time.Time) (FrameIndex, error) {
	args := []string{"capture-pane", "-p"}
	if r.Target != "" {
		args = append(args, "-t", r.Target)
	}
	cmd := exec.CommandContext(ctx, "tmux", args...)
	output, err := cmd.Output()
	if err != nil {
		return FrameIndex{}, fmt.Errorf("tmux capture-pane: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return FrameIndex{}, err
	}
	name := fmt.Sprintf("%s.txt", ts.UTC().Format("20060102T150405.000000000Z"))
	full := filepath.Join(outDir, name)
	if err := os.WriteFile(full, []byte(strings.TrimRight(string(output), "\n")+"\n"), 0o644); err != nil {
		return FrameIndex{}, err
	}
	return FrameIndex{Ts: ts.UTC(), Path: name, Format: "text"}, nil
}

// Run drives a Renderer at 1 frame/second by default, writing frames into
// outDir/frames/ and appending an index entry to outDir/frames.jsonl after
// each capture. Returns when ctx is cancelled.
//
// A capture error doesn't terminate the loop — synthesis may want to know
// that an interval produced no frame, but we keep running so a transient
// tmux blip doesn't stop the recording.
func Run(ctx context.Context, outDir string, r Renderer, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	framesDir := filepath.Join(outDir, "frames")
	indexPath := filepath.Join(outDir, "frames.jsonl")
	idx, err := os.OpenFile(indexPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open frames.jsonl: %w", err)
	}
	defer idx.Close()
	w := bufio.NewWriter(idx)
	defer w.Flush()
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	var mu sync.Mutex // protect the index writer
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// First tick immediately, then every interval.
	for first := true; ; first = false {
		if !first {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
		ts := time.Now()
		entry, err := r.Capture(ctx, framesDir, ts)
		if err != nil {
			continue // transient — keep ticking
		}
		// Store path relative to outDir so frames.jsonl is portable.
		entry.Path = filepath.Join("frames", entry.Path)
		mu.Lock()
		if err := enc.Encode(entry); err != nil {
			mu.Unlock()
			return fmt.Errorf("encode frame index: %w", err)
		}
		_ = w.Flush()
		mu.Unlock()
	}
}
