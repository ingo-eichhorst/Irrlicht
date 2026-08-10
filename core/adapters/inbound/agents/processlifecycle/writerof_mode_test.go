//go:build darwin

package processlifecycle

import "testing"

// A transcript held open for READ-WRITE is held open for writing. lsof spells
// that mode 'u', and it is what Codex 0.147 does with its rollout file:
//
//	codex 8387 ingo 59u REG 1,18 65631 ... /…/sessions/…/rollout-….jsonl
//
// WriterOf compared the LAST BYTE of the FD column against 'w', so every 'u'
// handle read as "not a writer" and DiscoverPIDByTranscriptWriter returned 0.
// The daemon then emitted no pid_discovered for the codex session, curate
// dropped the proc-<pid> presession that hangs off it, and every codex cell's
// expected.jsonl failed at its first phase (#1388).
//
// The Linux observer never had this bug: fdWritable accepts O_WRONLY *and*
// O_RDWR (flags&3 != 0), so the two platforms disagreed about the same
// process. macOS was the outlier.
func TestWriterPIDFromLsof_AcceptsReadWriteHandles(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want int
	}{
		{
			name: "write-only handle is a writer",
			out: "COMMAND  PID USER  FD  TYPE DEVICE SIZE/OFF NODE NAME\n" +
				"codex  24454 ingo  14w REG  1,18   3330     123  /tmp/rollout.jsonl\n",
			want: 24454,
		},
		{
			name: "read-write handle is a writer (codex 0.147)",
			out: "COMMAND  PID USER  FD  TYPE DEVICE SIZE/OFF NODE NAME\n" +
				"codex   8387 ingo  59u REG  1,18   65631    144  /tmp/rollout.jsonl\n",
			want: 8387,
		},
		{
			name: "locked read-write handle is a writer (mode letter is not last)",
			out: "COMMAND  PID USER  FD   TYPE DEVICE SIZE/OFF NODE NAME\n" +
				"codex   8387 ingo  59uW REG  1,18   65631    144  /tmp/rollout.jsonl\n",
			want: 8387,
		},
		// Vacuity guard: a predicate that simply returned the first row would
		// pass every case above while being wrong. A pure reader is not a
		// writer, and must still be rejected.
		{
			name: "read-only handle is NOT a writer",
			out: "COMMAND  PID USER  FD  TYPE DEVICE SIZE/OFF NODE NAME\n" +
				"tail   99999 ingo  3r  REG  1,18   3330     123  /tmp/rollout.jsonl\n",
			want: 0,
		},
		{
			name: "reader first, writer second — the writer wins",
			out: "COMMAND  PID USER  FD  TYPE DEVICE SIZE/OFF NODE NAME\n" +
				"tail   99999 ingo  3r  REG  1,18   3330     123  /tmp/rollout.jsonl\n" +
				"codex   8387 ingo  59u REG  1,18   65631    144  /tmp/rollout.jsonl\n",
			want: 8387,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// self=1 so no row is filtered as our own process.
			if got := writerPIDFromLsof(tc.out, 1); got != tc.want {
				t.Errorf("writerPIDFromLsof() = %d, want %d", got, tc.want)
			}
		})
	}
}
