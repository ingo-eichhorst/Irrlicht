package workerlist

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 11, 15, 40, 0, 0, time.UTC)

func rfc(t time.Time) string { return t.Format(time.RFC3339) }

// oneSlot is a one-team document with a single busy slot. heartbeat "" omits
// last_heartbeat.
func oneSlot(teamUpdated time.Time, heartbeat string) string {
	hb := ""
	if heartbeat != "" {
		hb = fmt.Sprintf(`, "last_heartbeat": %q`, heartbeat)
	}
	return fmt.Sprintf(`{"schema": 1, "heartbeat_interval_seconds": 60, "teams": [
	  {"name": "team-a", "updated": %q, "workers": [{"name": "team-a-m1-1", "busy": true, "work_item": "p-1"%s}]}]}`,
		rfc(teamUpdated), hb)
}

// The staleness threshold is 3 heartbeat intervals (180 s at 60 s), checked
// one second either side against both references: a busy slot's
// last_heartbeat, and — with no heartbeat — its team's updated (which also
// makes the team itself stale). Seen red by changing staleMultiplier from 3
// to 4: every "+1s" row then reads fresh. The exact-boundary row is in the
// stale-heartbeat replay scenario, where `>=` instead of `>` turns tick 1 red.
func TestBuildState_StalenessBoundary(t *testing.T) {
	const T = 180 * time.Second
	cases := []struct {
		name          string
		doc           string
		now           time.Time
		wantSlotStale bool
		wantTeam      string
	}{
		{"heartbeat T-1s", oneSlot(t0.Add(T), rfc(t0)), t0.Add(T - time.Second), false, statusOperational},
		{"heartbeat T+1s", oneSlot(t0.Add(T), rfc(t0)), t0.Add(T + time.Second), true, statusOperational},
		{"team updated T-1s", oneSlot(t0, ""), t0.Add(T - time.Second), false, statusOperational},
		{"team updated T+1s", oneSlot(t0, ""), t0.Add(T + time.Second), true, statusStale},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := decodeDocument([]byte(tc.doc))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			cb := buildState(doc, tc.now).Codebases[0]
			w := cb.Worktrees[0].Workers[0]
			if w.Stale != tc.wantSlotStale {
				t.Errorf("slot stale = %v, want %v", w.Stale, tc.wantSlotStale)
			}
			if cb.Status != tc.wantTeam {
				t.Errorf("team status = %q, want %q", cb.Status, tc.wantTeam)
			}
		})
	}
}

// A free slot is never stale, even in a stale team.
func TestBuildState_FreeSlotNeverStale(t *testing.T) {
	doc, err := decodeDocument([]byte(fmt.Sprintf(`{"schema": 1, "teams": [
	  {"name": "team-a", "updated": %q, "workers": [{"name": "s1", "busy": false}]}]}`, rfc(t0))))
	if err != nil {
		t.Fatal(err)
	}
	cb := buildState(doc, t0.Add(time.Hour)).Codebases[0]
	if cb.Status != statusStale || cb.Worktrees[0].Workers[0].Stale {
		t.Errorf("status=%q slot stale=%v, want stale team and a fresh free slot", cb.Status, cb.Worktrees[0].Workers[0].Stale)
	}
}

// Every way a document can be invalid is refused with a reason naming it.
func TestDecodeDocument_RefusesInvalid(t *testing.T) {
	team := func(fields string) string {
		return `{"schema": 1, "teams": [` + fields + `]}`
	}
	cases := map[string]struct{ doc, reason string }{
		"not json":             {`{"schema": 1,`, "parse"},
		"no schema":            {`{"teams": []}`, "missing schema"},
		"schema 2":             {`{"schema": 2, "teams": []}`, "unsupported schema 2"},
		"no teams":             {`{"schema": 1}`, "missing teams"},
		"zero heartbeat":       {`{"schema": 1, "heartbeat_interval_seconds": 0, "teams": []}`, "not positive"},
		"team without name":    {team(`{"updated": "2026-11-11T15:40:00Z", "workers": []}`), "missing name"},
		"team without update":  {team(`{"name": "a", "workers": []}`), "missing updated"},
		"team without workers": {team(`{"name": "a", "updated": "2026-11-11T15:40:00Z"}`), "missing workers"},
		"bad timestamp":        {team(`{"name": "a", "updated": "yesterday", "workers": []}`), "parse"},
		"worker without name":  {team(`{"name": "a", "updated": "2026-11-11T15:40:00Z", "workers": [{"busy": true}]}`), "workers[0]: missing name"},
		"worker without busy":  {team(`{"name": "a", "updated": "2026-11-11T15:40:00Z", "workers": [{"name": "s"}]}`), "workers[0]: missing busy"},
		"duplicate team": {team(`{"name": "a", "updated": "2026-11-11T15:40:00Z", "workers": []},
		                         {"name": "a", "updated": "2026-11-11T15:40:00Z", "workers": []}`), "duplicate team name"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeDocument([]byte(tc.doc))
			if err == nil {
				t.Fatal("decoded an invalid document")
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("error %q does not name %q", err, tc.reason)
			}
		})
	}
}

// LOCK: fields the schema accepts and ignores, and unknown fields, decode.
func TestDecodeDocument_IgnoresAcceptedAndUnknownFields(t *testing.T) {
	doc := `{"schema": 1, "updated": "2026-11-11T15:40:00Z", "future": {"x": 1}, "teams": [
	  {"name": "a", "updated": "2026-11-11T15:40:00Z", "last_tick": "2026-11-11T15:40:00Z", "workers": [
	    {"name": "s", "busy": true, "activity": {"text": "runs", "since": "2026-11-11T15:20:00Z"},
	     "last_run": {"start": "2026-11-11T14:10:00Z", "end": "2026-11-11T14:52:00Z", "exit": 0, "note": null}}]}]}`
	if _, err := decodeDocument([]byte(doc)); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
