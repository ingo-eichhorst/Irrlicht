package services_test

import (
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
		bg := repo.states["s"].Background
		if bg == nil {
			t.Fatal("background not captured")
		}
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
		pm.SetLauncherEnvReader(func(pid int) *session.Launcher {
			return &session.Launcher{TTY: "/dev/ttys003"}
		})
		pm.SetBackgroundReader(func(pid int) *session.BackgroundAgent {
			return &session.BackgroundAgent{Name: "bg job"}
		})

		pm.HandlePIDAssigned(42, "s")
		bg := repo.states["s"].Background
		if bg == nil {
			t.Fatal("background not captured")
		}
		if bg.Detached {
			t.Error("Detached: got true, want false (launcher TTY present)")
		}
	})

	t.Run("nil reader is a no-op", func(t *testing.T) {
		repo := newMockRepo()
		repo.states["s"] = newReady()
		pm := newPIDManagerForTest(repo)
		pm.HandlePIDAssigned(42, "s")
		if repo.states["s"].Background != nil {
			t.Error("Background set without a reader installed")
		}
	})

	t.Run("nil result leaves interactive sessions unmarked", func(t *testing.T) {
		repo := newMockRepo()
		repo.states["s"] = newReady()
		pm := newPIDManagerForTest(repo)
		pm.SetBackgroundReader(func(pid int) *session.BackgroundAgent { return nil })
		pm.HandlePIDAssigned(42, "s")
		if repo.states["s"].Background != nil {
			t.Error("Background set for an unrecognized (nil-result) PID")
		}
	})
}
