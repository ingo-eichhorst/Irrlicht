package shellout

import "testing"

// TestCappedBufferStopsAtItsLimit is the unit half of the cap, so a change to
// its enforcement is caught without spawning a flooding child.
//
// It lives here rather than beside either consumer because the two consumers
// want OPPOSITE things from the same overflow — core/pkg/cliprobe truncates and
// keeps reading, core/adapters/outbound/git reports a non-answer — and the
// arithmetic they share is what this pins.
//
// MUTATION EVIDENCE, both run and both red:
//
//   - Keying the overflow on `len(w.buf) >= w.Limit` (the spelling #1543's
//     first draft shipped): fails "a complete read of exactly the limit was
//     reported as truncated", because the buffer merely REACHING the cap is
//     not the same as bytes being dropped.
//   - Returning `len(w.buf)` instead of `len(p)` from Write: fails
//     "Write past the limit"; io.Copy would report ErrShortWrite for a buffer
//     behaving exactly as designed, and exec treats that as fatal to the
//     command.
func TestCappedBufferStopsAtItsLimit(t *testing.T) {
	var w CappedBuffer
	w.Limit = 8

	if n, err := w.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("Write = (%d, %v), want (3, nil)", n, err)
	}
	if w.Overflowed() {
		t.Error("3 bytes into a limit of 8 reported an overflow")
	}

	// Exactly the limit is NOT an overflow: nothing was dropped. This is the
	// boundary, and the direction that matters — a false overflow blanks a
	// read that completed successfully.
	if n, err := w.Write([]byte("defgh")); n != 5 || err != nil {
		t.Fatalf("Write to exactly the limit = (%d, %v), want (5, nil)", n, err)
	}
	if w.Overflowed() {
		t.Error("a complete read of exactly the limit was reported as truncated")
	}
	if w.String() != "abcdefgh" {
		t.Fatalf("kept %q, want abcdefgh", w.String())
	}

	// One byte past drops it, and reports the FULL length anyway: returning
	// short would make exec's copier treat it as a write error and fail the
	// command for the wrong reason.
	if n, err := w.Write([]byte("i")); n != 1 || err != nil {
		t.Fatalf("Write past the limit = (%d, %v), want (1, nil)", n, err)
	}
	if !w.Overflowed() {
		t.Error("writing past the limit did not record an overflow")
	}
	if string(w.Bytes()) != "abcdefgh" {
		t.Errorf("kept %q, want the FIRST 8 bytes", w.Bytes())
	}
}

// TestCappedBufferOverflowsOnASingleOversizeWrite covers the other arithmetic
// path: one write larger than the whole limit, rather than an accumulation
// that crosses it. A guard keyed on the ACCUMULATED length handles the second
// and can miss the first.
func TestCappedBufferOverflowsOnASingleOversizeWrite(t *testing.T) {
	var w CappedBuffer
	w.Limit = 4

	if n, err := w.Write([]byte("abcdefgh")); n != 8 || err != nil {
		t.Fatalf("Write = (%d, %v), want (8, nil)", n, err)
	}
	if !w.Overflowed() {
		t.Error("a single write of 8 bytes into a limit of 4 did not report an overflow")
	}
	if w.String() != "abcd" {
		t.Errorf("kept %q, want abcd", w.String())
	}
}

// TestCappedBufferZeroLimitKeepsNothing pins the degenerate case rather than
// leaving it to be discovered: a zero Limit is a buffer that retains nothing
// and calls every non-empty write an overflow. No caller sets one today, and
// the doc says so — this is what makes that statement checkable.
func TestCappedBufferZeroLimitKeepsNothing(t *testing.T) {
	var w CappedBuffer
	if n, err := w.Write([]byte("x")); n != 1 || err != nil {
		t.Fatalf("Write = (%d, %v), want (1, nil)", n, err)
	}
	if !w.Overflowed() {
		t.Error("a zero limit did not report an overflow")
	}
	if len(w.Bytes()) != 0 {
		t.Errorf("kept %q, want nothing", w.Bytes())
	}
	// An empty write drops nothing, so it is not an overflow even here.
	var empty CappedBuffer
	if _, err := empty.Write(nil); err != nil {
		t.Fatalf("Write(nil): %v", err)
	}
	if empty.Overflowed() {
		t.Error("an empty write reported an overflow")
	}
}
