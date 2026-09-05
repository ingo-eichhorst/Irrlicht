package main

import (
	"strings"
	"testing"
)

func TestActiveStretches(t *testing.T) {
	cases := []struct {
		name   string
		series []int64
		gap    int64
		want   []stretch
	}{
		{"empty", nil, 180, nil},
		{"a single row is an instant, not a stretch", []int64{100}, 180, []stretch{{100, 100}}},
		{"a contiguous run is one stretch", []int64{100, 160, 220, 280}, 180, []stretch{{100, 280}}},
		{"a long quiet period splits it", []int64{100, 160, 500, 560}, 180, []stretch{{100, 160}, {500, 560}}},
		// The boundary is the whole judgement call the threshold encodes, so
		// both sides of it are pinned rather than one.
		{"a gap exactly at the threshold does NOT split", []int64{100, 280}, 180, []stretch{{100, 280}}},
		{"one second past the threshold splits", []int64{100, 281}, 180, []stretch{{100, 100}, {281, 281}}},
		{"three stretches", []int64{0, 60, 1000, 2000, 2060}, 180, []stretch{{0, 60}, {1000, 1000}, {2000, 2060}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := activeStretches(tc.series, tc.gap)
			if len(got) != len(tc.want) {
				t.Fatalf("activeStretches(%v, %d) = %v, want %v", tc.series, tc.gap, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("activeStretches(%v, %d) = %v, want %v", tc.series, tc.gap, got, tc.want)
				}
			}
		})
	}
}

// Same policy as the event log: damage is survived AND counted. A cost row
// missing the fields this tool needs counts as malformed even if the daemon
// wrote it deliberately — a silently changed row shape must not read as a
// quiet month.
func TestReadCostLogCountsWhatItCannotUse(t *testing.T) {
	dir := t.TempDir()
	writeLogFixture(t, dir, "cost/proj.jsonl", strings.Join([]string{
		`{"ts":100,"project":"proj","session":"s1","cost":0}`,
		`{"ts":160,"project":"proj","session":"s1","cost":0.5}`,
		``,
		`{"ts":170,"project":"proj"`, // truncated
		`nonsense`,
		`{"ts":0,"project":"proj","session":"s1"}`,   // no usable timestamp
		`{"ts":200,"project":"","session":"s1"}`,     // nothing to file it under
		`{"ts":200,"project":"proj","session":""}`,   // no session to attribute it to
		`{"ts":100,"project":"proj","session":"s1"}`, // an exact duplicate second
	}, "\n")+"\n")

	cl, err := readCostLog(dir)
	if err != nil {
		t.Fatalf("readCostLog: %v", err)
	}
	if got, want := cl.Stats.Malformed, 5; got != want {
		t.Errorf("malformed = %d, want %d", got, want)
	}
	series := cl.Series[sessionKey{Project: "proj", Session: "s1"}]
	if len(series) != 2 {
		t.Fatalf("series = %v, want two entries after de-duplication", series)
	}
	if series[0] != 100 || series[1] != 160 {
		t.Fatalf("series = %v, want [100 160] sorted ascending", series)
	}
}

// The cost log is sharded per project, so the same session id under two
// projects is two series. Merging them would join two unrelated timelines into
// one impossibly long run.
func TestReadCostLogKeepsProjectsApart(t *testing.T) {
	dir := t.TempDir()
	writeLogFixture(t, dir, "cost/a.jsonl", `{"ts":100,"project":"a","session":"shared"}`+"\n")
	writeLogFixture(t, dir, "cost/b.jsonl", `{"ts":100000,"project":"b","session":"shared"}`+"\n")

	cl, err := readCostLog(dir)
	if err != nil {
		t.Fatalf("readCostLog: %v", err)
	}
	if len(cl.Series) != 2 {
		t.Fatalf("series count = %d, want 2 — one per (project, session)", len(cl.Series))
	}
}

func TestReadCostLogRefusesAMissingLog(t *testing.T) {
	if _, err := readCostLog(t.TempDir()); err == nil {
		t.Fatal("readCostLog on a directory with no cost files returned no error")
	}
}

func TestDedupeSorted(t *testing.T) {
	cases := []struct {
		name string
		in   []int64
		want []int64
	}{
		{"empty", nil, nil},
		{"one", []int64{1}, []int64{1}},
		{"no duplicates", []int64{1, 2, 3}, []int64{1, 2, 3}},
		{"runs collapse", []int64{1, 1, 1, 2, 2, 3}, []int64{1, 2, 3}},
		{"all the same", []int64{7, 7, 7}, []int64{7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dedupeSorted(append([]int64(nil), tc.in...))
			if len(got) != len(tc.want) {
				t.Fatalf("dedupeSorted(%v) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("dedupeSorted(%v) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}
