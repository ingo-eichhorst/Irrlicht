package sensors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"time"
)

// PTY tails a file containing raw PTY output — typically produced by
// invoking the agent under `script -F <path>` or `tmux pipe-pane -IO`.
// The bytes preserve ANSI control sequences; Phase 3 synthesis parses
// them to detect screen-clear, cursor moves, color codes, etc.
//
// The sensor is intentionally agnostic about who writes the file. The
// recorder is responsible for setting up the writer (script wrapper or
// pipe-pane redirect) before this sensor starts.
//
// Kind: "chunk". Payload:
//
//	{"offset": <int>, "len": <int>, "bytes_b64": "<base64>"}
//
// `offset` is the byte offset in the source file where the chunk began,
// so Phase 3 can reassemble or align chunks across sensors. `bytes_b64`
// is base64 (standard, padded) because raw bytes include control codes
// and zero bytes that don't round-trip through JSON strings.
type PTY struct {
	// Path is the file the PTY stream is written to.
	Path string
	// PollInterval defaults to 100ms.
	PollInterval time.Duration
	// MaxChunkBytes caps a single emitted chunk. Defaults to 16 KiB.
	MaxChunkBytes int
}

const ptyName = "pty"

// Name implements Sensor.
func (p *PTY) Name() string { return ptyName }

// Run implements Sensor.
func (p *PTY) Run(ctx context.Context) <-chan Signal {
	out := make(chan Signal, 16)
	poll := p.PollInterval
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	maxChunk := p.MaxChunkBytes
	if maxChunk <= 0 {
		maxChunk = 16 * 1024
	}
	go func() {
		defer close(out)

		var f *os.File
		for f == nil {
			var err error
			f, err = os.Open(p.Path)
			if err == nil {
				break
			}
			if !os.IsNotExist(err) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(poll):
			}
		}
		defer f.Close()

		buf := make([]byte, maxChunk)
		var offset int64
		ticker := time.NewTicker(poll)
		defer ticker.Stop()

		for {
			for {
				n, err := f.Read(buf)
				if n > 0 {
					chunk := make([]byte, n)
					copy(chunk, buf[:n])
					payload, _ := MarshalPayload(struct {
						Offset   int64  `json:"offset"`
						Len      int    `json:"len"`
						BytesB64 string `json:"bytes_b64"`
					}{
						Offset:   offset,
						Len:      n,
						BytesB64: base64.StdEncoding.EncodeToString(chunk),
					})
					select {
					case out <- Signal{
						Ts:      time.Now().UTC(),
						Sensor:  ptyName,
						Kind:    "chunk",
						Payload: json.RawMessage(payload),
					}:
					case <-ctx.Done():
						return
					}
					offset += int64(n)
				}
				if err == io.EOF || err == nil && n == 0 {
					break
				}
				if err != nil {
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return out
}
