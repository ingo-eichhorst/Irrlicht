package services_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"irrlicht/core/application/services"
)

// healthyHookSnapshot is a daemon with everything working: every target
// watched and intact, every channel armed and delivering. It is the baseline
// each case below perturbs by exactly one field, so a case that reports a
// fault reports it because of THAT field and nothing else.
func healthyHookSnapshot() services.HookHealthSnapshot {
	return services.HookHealthSnapshot{
		Channels: []services.HookChannelHealth{
			{Adapter: "claude-code", Armed: true, Receipts: 12, Silent: false},
			{Adapter: "codex", Armed: true, Receipts: 3, Silent: false},
		},
		EntryReverification: services.HookEntryReverifySnapshot{
			Targets: []services.HookEntryHealth{
				{Adapter: "claude-code", Permission: "hooks", ConfigPath: "/home/u/.claude/settings.json", Watched: true},
				{Adapter: "codex", Permission: "hooks", ConfigPath: "/home/u/.codex/config.toml", Watched: true},
			},
		},
	}
}

// TestDaemonErrors_HealthyDaemonReportsNothing is the case the `omitempty` tag
// depends on. If a healthy daemon produced an empty non-nil slice, the field
// would serialize as `"daemon_errors":[]` and every existing client's payload
// would change shape for no reason.
func TestDaemonErrors_HealthyDaemonReportsNothing(t *testing.T) {
	got := services.DaemonErrors(healthyHookSnapshot())
	if got != nil {
		t.Errorf("healthy daemon: want nil, got %+v", got)
	}
}

func TestDaemonErrors_ReportsTheTwoClientInvisibleDiagnoses(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*services.HookHealthSnapshot)
		wantKind string
		// wantIn is text that must appear in the message or detail. These are
		// the parts a user acts on — which adapter, and what the machine said.
		wantIn []string
	}{
		{
			name: "entries went missing (#1372)",
			mutate: func(h *services.HookHealthSnapshot) {
				h.EntryReverification.Targets[0].Missing = []string{"Stop", "Notification"}
			},
			wantKind: services.DaemonErrorHookEntriesMissing,
			wantIn:   []string{"claude-code", "Stop", "Notification", "/home/u/.claude/settings.json"},
		},
		{
			name: "the config could not be read back at all",
			mutate: func(h *services.HookHealthSnapshot) {
				h.EntryReverification.Targets[1].LastError = "permission denied"
			},
			wantKind: services.DaemonErrorHookEntriesMissing,
			wantIn:   []string{"codex", "permission denied", "/home/u/.codex/config.toml"},
		},
		{
			name: "entries present but nothing arriving (#1368)",
			mutate: func(h *services.HookHealthSnapshot) {
				h.Channels[0].Silent = true
				h.Channels[0].TurnsSinceReceipt = 7
			},
			wantKind: services.DaemonErrorHookChannelSilent,
			wantIn:   []string{"claude-code"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := healthyHookSnapshot()
			tc.mutate(&h)
			got := services.DaemonErrors(h)
			if len(got) != 1 {
				t.Fatalf("want exactly 1 fault, got %d: %+v", len(got), got)
			}
			if got[0].Kind != tc.wantKind {
				t.Errorf("kind: got %q, want %q", got[0].Kind, tc.wantKind)
			}
			if got[0].Scope == "" {
				t.Error("scope is empty — a fault the user cannot attribute is not actionable")
			}
			blob := got[0].Message + " | " + got[0].Detail
			for _, want := range tc.wantIn {
				if !strings.Contains(blob, want) {
					t.Errorf("fault text %q does not mention %q", blob, want)
				}
			}
		})
	}
}

// TestDaemonErrors_UnwatchedAndDisarmedRowsAreNotFaults pins the skip that
// keeps the banner credible. A target nobody granted consent for, and a
// channel the watchdog was never armed on, are user decisions or a cold start
// — not Irrlicht failing. Reporting them would put a permanent banner on every
// daemon whose user declined one adapter, and a banner that is always on is a
// banner nobody reads.
func TestDaemonErrors_UnwatchedAndDisarmedRowsAreNotFaults(t *testing.T) {
	h := healthyHookSnapshot()
	// Same perturbations as the cases above, on rows that are not being
	// watched. Every one would be a fault if Watched/Armed were ignored.
	h.EntryReverification.Targets[0].Watched = false
	h.EntryReverification.Targets[0].Missing = []string{"Stop"}
	h.EntryReverification.Targets[1].Watched = false
	h.EntryReverification.Targets[1].LastError = "permission denied"
	h.Channels[0].Armed = false
	h.Channels[0].Silent = true
	h.Channels[1].Armed = false
	h.Channels[1].Silent = true

	if got := services.DaemonErrors(h); got != nil {
		t.Errorf("unwatched/disarmed rows must not be reported as faults, got %+v", got)
	}
}

// TestDaemonErrors_OrderIsStable guards the client's re-announce suppression:
// the banner keys on its own rendered content and skips a rebuild when it has
// not changed, so an order that varied between polls would re-announce the same
// standing fault to a screen reader on every refresh.
//
// IT SHUFFLES THE INPUT, and that is the whole test. The first version called
// DaemonErrors 21 times on ONE fixed input and compared the runs — which Go
// slices satisfy by construction whether or not the sort exists. It passed with
// `sort.SliceStable` deleted (verified by mutation in review): a green that was
// never red. Feeding a permuted snapshot and demanding byte-identical output is
// what actually pins the ordering.
func TestDaemonErrors_OrderIsStable(t *testing.T) {
	build := func(reversed bool) services.HookHealthSnapshot {
		h := healthyHookSnapshot()
		h.Channels[0].Silent = true
		h.Channels[1].Silent = true
		h.EntryReverification.Targets[0].Missing = []string{"Stop"}
		h.EntryReverification.Targets[1].Missing = []string{"Stop"}
		if reversed {
			slices.Reverse(h.Channels)
			slices.Reverse(h.EntryReverification.Targets)
		}
		return h
	}

	first := services.DaemonErrors(build(false))
	if len(first) != 4 {
		t.Fatalf("want 4 faults, got %d: %+v", len(first), first)
	}
	want, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Guard the guard: the two orderings must genuinely differ as INPUT, or
	// "the outputs match" would be measuring nothing.
	a := build(false)
	b := build(true)
	if a.Channels[0].Adapter == b.Channels[0].Adapter {
		t.Fatalf("the fixture is not actually being permuted — both orderings start with %q", a.Channels[0].Adapter)
	}

	for i, reversed := range []bool{true, false, true} {
		got, err := json.Marshal(services.DaemonErrors(build(reversed)))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(got) != string(want) {
			t.Fatalf("run %d (reversed=%v) differs from the baseline:\n got %s\nwant %s", i+1, reversed, got, want)
		}
	}
}

// TestDaemonErrors_NoFieldMovesWhileTheFaultStands is the other half of the
// same protection, and the one that catches the defect review found: ordering
// can be perfectly stable while a fault's own CONTENT changes on every poll.
//
// The silent-channel fault's obvious detail is TurnsSinceReceipt, and using it
// made every poll look like a new fault to the client — re-announcing the whole
// role="alert" banner roughly every 30s for as long as the channel stayed dead.
// Every field is part of the fault's identity, so nothing that moves under a
// standing fault may appear in one.
func TestDaemonErrors_NoFieldMovesWhileTheFaultStands(t *testing.T) {
	at := func(turns int, repairs uint64) []byte {
		h := healthyHookSnapshot()
		h.Channels[0].Silent = true
		h.Channels[0].TurnsSinceReceipt = turns
		h.Channels[0].Receipts = uint64(turns)
		h.EntryReverification.Targets[0].Missing = []string{"Stop"}
		h.EntryReverification.Targets[0].Repairs = repairs
		h.EntryReverification.Targets[0].ConsecutiveRepairs = int(repairs)
		b, err := json.Marshal(services.DaemonErrors(h))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return b
	}

	// The same two faults, observed later: more turns have gone by with no
	// receipt, and the repair loop has run more times. Nothing the user could
	// act on has changed.
	early := at(3, 1)
	late := at(97, 42)
	if string(early) != string(late) {
		t.Errorf("a standing fault changed while nothing about it did:\n at turn 3:  %s\n at turn 97: %s\nA field that moves under an unchanged fault makes the client re-announce it on every poll.", early, late)
	}
}

// TestDaemonErrors_DoesNotDuplicateTheUnappliedGrantsDiagnosis is a scope lock.
// permission_service.go keeps three hook diagnoses deliberately apart — an
// install that FAILED (#1362, already on the wire as unapplied_grants and
// already carrying its own banner), entries that went MISSING (#1372), and
// entries that are present but DEAD (#1368). This function must only ever
// speak for the second and third; folding in the first would put two banners
// on screen for one fault and make the user fix it twice.
func TestDaemonErrors_DoesNotDuplicateTheUnappliedGrantsDiagnosis(t *testing.T) {
	for _, e := range services.DaemonErrors(healthyHookSnapshot()) {
		t.Errorf("unexpected fault on a healthy daemon: %+v", e)
	}
	// An install failure leaves no trace in the hook-health snapshot at all —
	// it lives in the permission service's effectErrs — so there is nothing
	// here to accidentally report. Assert the vocabulary stays closed to the
	// two kinds, so a future author adding a third has to come read this.
	h := healthyHookSnapshot()
	h.Channels[0].Silent = true
	h.EntryReverification.Targets[0].Missing = []string{"Stop"}
	for _, e := range services.DaemonErrors(h) {
		switch e.Kind {
		case services.DaemonErrorHookEntriesMissing, services.DaemonErrorHookChannelSilent:
		default:
			t.Errorf("unexpected daemon-error kind %q — see permission_service.go's scope comment before adding one", e.Kind)
		}
	}
}
