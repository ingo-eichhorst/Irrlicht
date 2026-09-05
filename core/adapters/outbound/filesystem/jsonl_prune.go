package filesystem

import (
	"bufio"
	"fmt"
	"os"
	"time"
)

// jsonlScanBuf sizes the line scanner shared by every append-only JSONL store
// in this package: a 64 KiB starting buffer with a 4 MiB ceiling, so one
// pathologically long row cannot abort a whole file's scan.
const (
	jsonlScanBufInitial = 64 * 1024
	jsonlScanBufMax     = 4 * 1024 * 1024
)

// pruneJSONLFile rewrites path keeping only the lines for which keep returns
// true, via a temp file plus an atomic rename; the file is removed outright
// when nothing survives. A missing file is not an error.
//
// This is the cost log's prune idiom (#369), lifted out of CostTracker so the
// autonomy span log (#1905) reuses it rather than growing a second, subtly
// different copy — the two stores share a shape (one append-only JSONL file
// per project, pruned to the same 400 days at the same startup call site), and
// the interesting part is exactly the part worth having once: never truncate
// the live file, write a sibling and rename over it, so a crash mid-prune
// leaves the original intact.
//
// The caller is responsible for holding whatever per-file lock keeps a
// concurrent appender out; this function does no locking of its own.
func pruneJSONLFile(path string, keep func(line []byte) bool) error {
	in, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer in.Close()

	tmpPath := fmt.Sprintf("%s.tmp.%d.%d", path, os.Getpid(), time.Now().UnixNano())
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(out)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, jsonlScanBufInitial), jsonlScanBufMax)
	kept, err := copyKeptLines(scanner, w, keep)
	if err == nil {
		err = w.Flush()
	}
	if err != nil {
		out.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if kept == 0 {
		os.Remove(tmpPath)
		return os.Remove(path)
	}
	return os.Rename(tmpPath, path)
}

// copyKeptLines streams scanner, writing every line keep accepts to w, and
// returns how many survived. Empty lines are dropped without consulting keep.
func copyKeptLines(scanner *bufio.Scanner, w *bufio.Writer, keep func(line []byte) bool) (kept int, err error) {
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if !keep(line) {
			continue
		}
		if _, err := w.Write(line); err != nil {
			return kept, err
		}
		if err := w.WriteByte('\n'); err != nil {
			return kept, err
		}
		kept++
	}
	return kept, scanner.Err()
}
