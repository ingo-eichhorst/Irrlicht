package services_test

import (
	"os"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// TestHandlePIDAssigned_BackgroundCapture exercises the #744 capture seam:
// captureBackground flags a session from the injected reader, derives Detached
// from the controlling TTY (captured by captureLauncher just before it), and is
// a set-once no-op when no reader / no match.
func TestHandlePIDAssigned_BackgroundCapture(t *testing.T) {
	newReady := func() *session.SessionState {
		return &session.SessionState{SessionID: "s", State: session.StateReady, UpdatedAt: time.Now().Unix()}
	}

	t.Run("detached when no controlling tty", func(t *testing.T) {
		repo := newMockRepo()
		repo.states["s"] = newReady()
		pm := newPIDManagerForTest(repo)
		var calls int
		pm.SetBackgroundReader(func(pid int) *session.BackgroundAgent {
			calls++
			return &session.BackgroundAgent{Name: "bg job"}
		})

		pm.HandlePIDAssigned(42, "s")
		bg := requireBackground(t, repo, "s")
		if bg.Name != "bg job" {
			t.Errorf("name: got %q, want \"bg job\"", bg.Name)
		}
		if !bg.Detached {
			t.Error("Detached: got false, want true (no launcher TTY)")
		}

		// Set-once: a later PID with Background already set must not re-invoke.
		pm.HandlePIDAssigned(99, "s")
		if calls != 1 {
			t.Errorf("reader re-invoked: %d calls, want 1", calls)
		}
	})

	t.Run("attached when controlling tty present", func(t *testing.T) {
		repo := newMockRepo()
		repo.states["s"] = newReady()
		pm := newPIDManagerForTest(repo)
		pm.SetLauncherEnvReader(func(pid int) (*session.Launcher, bool) {
			return &session.Launcher{TTY: "/dev/ttys003"}, true
		})
		pm.SetBackgroundReader(func(pid int) *session.BackgroundAgent {
			return &session.BackgroundAgent{Name: "bg job"}
		})

		pm.HandlePIDAssigned(42, "s")
		bg := requireBackground(t, repo, "s")
		if bg.Detached {
			t.Error("Detached: got true, want false (launcher TTY present)")
		}
	})

	t.Run("nil reader is a no-op", func(t *testing.T) {
		repo := newMockRepo()
		repo.states["s"] = newReady()
		pm := newPIDManagerForTest(repo)
		pm.HandlePIDAssigned(42, "s")
		assertNoBackground(t, repo, "s", "Background set without a reader installed")
	})

	t.Run("nil result leaves interactive sessions unmarked", func(t *testing.T) {
		repo := newMockRepo()
		repo.states["s"] = newReady()
		pm := newPIDManagerForTest(repo)
		pm.SetBackgroundReader(func(pid int) *session.BackgroundAgent { return nil })
		pm.HandlePIDAssigned(42, "s")
		assertNoBackground(t, repo, "s", "Background set for an unrecognized (nil-result) PID")
	})
}

// requireBackground fetches sessionID's captured Background from repo,
// failing t fatally when it wasn't set.
func requireBackground(t *testing.T, repo *mockRepo, sessionID string) *session.BackgroundAgent {
	t.Helper()
	bg := repo.states[sessionID].Background
	if bg == nil {
		t.Fatal("background not captured")
	}
	return bg
}

// assertNoBackground fails t with msg when sessionID's Background got set.
func assertNoBackground(t *testing.T, repo *mockRepo, sessionID, msg string) {
	t.Helper()
	if repo.states[sessionID].Background != nil {
		t.Error(msg)
	}
}

// TestSeedPIDs_BackgroundDetachedFollowsTheRefreshedTTY is #1546.
//
// Launcher.TTY has two ways of being empty and they are not the same fact: the
// process genuinely has no controlling terminal, or the `ps` behind
// processTTY was killed by its 2s ceiling and never answered. captureBackground
// reads only the field, so it stamps Detached from the second as confidently as
// from the first.
//
// That would be self-correcting, because the codebase already treats an empty
// TTY as *missing* rather than absent: launcherBackfillNeedsFor returns
// `tty: l.TTY == ""` and backfillLauncher re-reads it on every seed, and
// handleAlivePIDState calls captureBackground straight afterwards saying, in a
// comment, that it does so "so Detached reflects the refreshed TTY". But
// captureBackground early-returns on `state.Background != nil`, and a record
// carrying a wrong badge is by definition one that already has a Background —
// so the machinery that repairs the input runs, and its output is discarded.
// SessionState is persisted whole, so the badge outlives the daemon that wrote
// it: amber "no open window; runs in the daemon pool" on a session with a
// perfectly good terminal, for the life of the record.
//
// The first arm is the defect. The other two are LOCKS on behaviour that must
// not change and pass by construction.
func TestSeedPIDs_BackgroundDetachedFollowsTheRefreshedTTY(t *testing.T) {
	// bgSession is a live background-agent session whose stored launcher and
	// badge are whatever the caller says they were at capture time.
	bgSession := func(l *session.Launcher, bg *session.BackgroundAgent) *session.SessionState {
		return &session.SessionState{
			SessionID:  "s",
			State:      session.StateWorking,
			PID:        os.Getpid(), // alive: handleAlivePIDState's ESRCH probe must pass
			UpdatedAt:  time.Now().Unix(),
			Launcher:   l,
			Background: bg,
		}
	}
	// answeringReader is the read that succeeds where the capture-time one
	// timed out.
	answeringReader := func(int) (*session.Launcher, bool) {
		return &session.Launcher{TermProgram: "iTerm.app", TTY: "/dev/ttys004"}, true
	}

	t.Run("a stamp made without evidence is corrected once the tty arrives", func(t *testing.T) {
		repo := newMockRepo()
		// Captured while the tty `ps` was timing out: a real host, no tty...
		repo.states["s"] = bgSession(
			&session.Launcher{TermProgram: "iTerm.app"},
			// ...and badged detached from that absence.
			&session.BackgroundAgent{Name: "nightly refactor", Detached: true},
		)
		pm := newPIDManagerForTest(repo)
		pm.SetLauncherEnvReader(answeringReader)
		pm.SetBackgroundReader(func(int) *session.BackgroundAgent {
			return &session.BackgroundAgent{Name: "nightly refactor"}
		})

		pm.SeedPIDs([]*session.SessionState{repo.states["s"]})

		got := repo.states["s"]
		// Vacuity guard. Without it, a run that reached neither the backfill
		// nor the re-derivation is indistinguishable from one that reached
		// both and found nothing to fix.
		if got.Launcher.TTY != "/dev/ttys004" {
			t.Fatalf("backfillLauncher never repaired the tty (TTY = %q), so this "+
				"run's verdict on Detached says nothing about #1546", got.Launcher.TTY)
		}
		if got.Background == nil {
			t.Fatal("the seed path dropped Background entirely")
		}
		if got.Background.Detached {
			t.Error("Detached: got true, want false — the session holds /dev/ttys004, " +
				"but the badge did not follow the repaired tty")
		}
		// The identification half stays set-once: only the derived field moves.
		if got.Background.Name != "nightly refactor" {
			t.Errorf("Name: got %q, want %q", got.Background.Name, "nightly refactor")
		}
	})

	// LOCK: passes on main by construction. A session that really has no
	// terminal must keep its detached badge — the re-derivation reads the same
	// stored TTY, which applyLauncherBackfill never clears (it copies only a
	// non-empty fresh value), so the badge can only ever move true → false.
	t.Run("a genuinely detached record keeps its badge", func(t *testing.T) {
		repo := newMockRepo()
		repo.states["s"] = bgSession(
			&session.Launcher{TermProgram: "iTerm.app"},
			&session.BackgroundAgent{Name: "nightly refactor", Detached: true},
		)
		pm := newPIDManagerForTest(repo)
		// The `ps` answers, and the answer is "no controlling terminal".
		pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
			return &session.Launcher{TermProgram: "iTerm.app"}, true
		})
		pm.SetBackgroundReader(func(int) *session.BackgroundAgent {
			return &session.BackgroundAgent{Name: "nightly refactor"}
		})

		pm.SeedPIDs([]*session.SessionState{repo.states["s"]})

		if bg := repo.states["s"].Background; bg == nil || !bg.Detached {
			t.Errorf("Detached: got %v, want true (still no controlling terminal)", bg)
		}
	})

	// LOCK: passes on main by construction. Re-deriving a field of an existing
	// badge must never conjure the badge itself — with no BackgroundReader
	// installed there is nothing that could have recognized this PID.
	//
	// It asserts on the seeded struct as well as on the repository, and the
	// first assertion is the load-bearing one. A conjured badge derives
	// Detached=false, which equals the zero value it was just given, so the
	// re-derivation reports "no change" and never saves — and the only write
	// this session takes is backfillLauncher's, which lands BEFORE the
	// re-derivation runs. Checking the persisted copy alone therefore reads a
	// snapshot taken before the thing under test happened: measured, by
	// mutating refreshBackgroundDetached to allocate a Background when it finds
	// none, which this arm passed until it also looked at the struct the seed
	// path mutates in place.
	t.Run("a session with no background badge is not given one", func(t *testing.T) {
		repo := newMockRepo()
		seeded := bgSession(&session.Launcher{TermProgram: "iTerm.app"}, nil)
		repo.states["s"] = seeded
		pm := newPIDManagerForTest(repo)
		pm.SetLauncherEnvReader(answeringReader)

		pm.SeedPIDs([]*session.SessionState{seeded})

		if seeded.Background != nil {
			t.Errorf("the seed path conjured a Background out of a re-derivation: %+v", seeded.Background)
		}
		assertNoBackground(t, repo, "s", "a conjured Background was persisted")
	})
}
