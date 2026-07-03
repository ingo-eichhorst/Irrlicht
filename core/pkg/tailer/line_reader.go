package tailer

import (
	"bufio"
	"errors"
	"io"
)

// maxTranscriptLineSize caps a single JSONL line at 64 MB. Lines beyond this
// are skipped so a malformed or pathological transcript can never wedge the
// tailer (issue #270).
const maxTranscriptLineSize = 64 * 1024 * 1024

var (
	errLineTooLong  = errors.New("transcript line exceeds size cap")
	errPartialAtEOF = errors.New("transcript ends mid-line")
)

// readLineCapped reads a single '\n'-terminated line from r. The returned
// slice does not include the trailing '\n' (or '\r' before it); consumed is
// the total bytes drawn from r — the caller advances the file offset by that
// amount whenever the result represents a fully-handled line (success or
// errLineTooLong).
//
// Outcomes:
//   - (line, consumed, nil): full line read.
//   - (nil, consumed, errLineTooLong): line exceeded max and ended with '\n';
//     bytes were discarded and consumed reflects the skip distance.
//   - (nil, 0, io.EOF): clean EOF, no bytes read.
//   - (nil, 0, errPartialAtEOF): EOF reached before '\n' with bytes pending —
//     either an in-progress line below the cap or one that has already grown
//     past the cap but the writer hasn't flushed '\n' yet. Caller stops
//     without advancing so the bytes are re-read once more data is appended;
//     when the line eventually completes it is reported as either a success
//     or errLineTooLong with a single accurate consumed count.
//   - (nil, 0, err): other I/O error.
func readLineCapped(r *bufio.Reader, max int64) ([]byte, int64, error) {
	var (
		buf      []byte
		consumed int64
		skipping bool
	)
	for {
		chunk, err := r.ReadSlice('\n')
		consumed += int64(len(chunk))

		switch err {
		case nil:
			if skipping {
				return nil, consumed, errLineTooLong
			}
			line := chunk[:len(chunk)-1] // drop '\n'
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if buf == nil {
				out := make([]byte, len(line))
				copy(out, line)
				return out, consumed, nil
			}
			buf = append(buf, line...)
			return buf, consumed, nil
		case bufio.ErrBufferFull:
			if skipping {
				continue
			}
			if consumed > max {
				buf = nil // release accumulated bytes for GC
				skipping = true
				continue
			}
			buf = append(buf, chunk...)
		case io.EOF:
			if consumed == 0 {
				return nil, 0, io.EOF
			}
			return nil, 0, errPartialAtEOF
		default:
			return nil, 0, err
		}
	}
}
